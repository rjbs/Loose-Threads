package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rjbs/loosethreads/internal/jmap"
	"github.com/rjbs/loosethreads/internal/patch"
)

// SyncResult reports what one sync of a collection did.
type SyncResult struct {
	Collection string
	Sent       int      // objects created, changed, or destroyed on the server
	Received   int      // objects that arrived or changed from the server
	Problems   []string // per-object failures and merge conflicts
}

func (r SyncResult) String() string {
	var parts []string
	if r.Sent > 0 {
		parts = append(parts, fmt.Sprintf("sent %d", r.Sent))
	}
	if r.Received > 0 {
		parts = append(parts, fmt.Sprintf("received %d", r.Received))
	}
	if len(parts) == 0 {
		parts = append(parts, "up to date")
	}
	s := r.Collection + ": " + strings.Join(parts, ", ")
	for _, p := range r.Problems {
		s += "\n  " + p
	}
	return s
}

// ErrNotSynced is returned when asked to sync a collection with no
// account configured.
var ErrNotSynced = errors.New("store: collection is not synced")

// resolvers merge properties changed on both sides; the description
// resolver keeps appends from both.  See DESIGN.md.
var resolvers = map[string]patch.Resolver{"description": patch.AppendResolver}

// syncRounds bounds how many out-and-in passes one sync makes.  A second
// pass is needed only when the first pass's sync-in merged duplicate
// lists, which leaves new pending changes.
const syncRounds = 3

// Sync synchronizes one collection with its JMAP account: local changes
// out, then server changes in.  Only one sync of a collection runs at a
// time; a second waits for the first, then runs, so a write made during
// a sync is never left behind.
func (s *Store) Sync(ctx context.Context, collection string) (res SyncResult, err error) {
	res = SyncResult{Collection: collection}
	ep, ok := s.config.Endpoint(collection)
	if !ok {
		return res, fmt.Errorf("%w: %s", ErrNotSynced, collection)
	}
	unlock, err := s.lockSync(ctx, collection)
	if err != nil {
		return res, err
	}
	defer unlock()
	defer func(start time.Time) {
		if rerr := s.recordSync(collection, start, err); err == nil {
			err = rerr
		}
	}(time.Now())

	c, err := jmap.Open(ctx, ep.Server, ep.Token)
	if err != nil {
		return res, err
	}
	if _, ok := c.Accounts[ep.Account]; !ok {
		return res, fmt.Errorf("store: %s: the server has no account %q for these credentials", collection, ep.Account)
	}
	sy := &syncer{s: s, c: c, collection: collection, account: ep.Account, res: &res}

	// Sync pulls before it pushes as well as after.  Rebase happens only
	// on the way in, and a pending description sent without it would
	// replace notes appended elsewhere since our last pull; pulling first
	// narrows that window to the time between two requests.  The pull
	// that rides along with the push brings back what the server made of
	// our changes.
	//
	// Pulling first is also what makes a create safe to retry.  If a
	// create's response is lost, the next sync's pull brings the object
	// back: a Task matches its pending create by uid, and a TaskList is a
	// duplicate of its pending create, which is absorbed into it.
	// -- claude, 2026-11-07
	if err := sy.in(ctx); err != nil {
		return res, err
	}
	// A list created here for a project the pull just brought in from
	// another machine is absorbed now, before it is ever sent.
	if _, err := sy.mergeDuplicates(); err != nil {
		return res, err
	}
	for range syncRounds {
		pushed, err := sy.out(ctx)
		if err != nil || !pushed {
			return res, err
		}
		merged, err := sy.mergeDuplicates()
		if err != nil {
			return res, err
		}
		if !merged {
			break
		}
	}
	return res, nil
}

// lockSync takes an exclusive lock on the collection's sync lock file,
// waiting for it until ctx is done.
func (s *Store) lockSync(ctx context.Context, collection string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.Root, ".sync-"+collection+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("store: waiting for another sync of %s: %w", collection, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type syncer struct {
	s          *Store
	c          *jmap.Client
	collection string
	account    string
	res        *SyncResult
}

func (sy *syncer) problem(format string, args ...any) {
	sy.res.Problems = append(sy.res.Problems, fmt.Sprintf(format, args...))
}

// sent is one object as it went out: the pending sequence number it was
// sent at, and the value sent (nil for a destroy).
type sent struct {
	row   *row
	seq   int64
	value patch.Object
}

func (sy *syncer) pendingRows(typ string) ([]*row, error) {
	return queryRows(sy.s.db, `collection = ? AND type = ? AND pending > 0 ORDER BY pending`, sy.collection, typ)
}

// out sends pending changes and pulls what the server made of them.  It
// reports whether it sent anything; if not, it sends no request at all.
//
// TaskList creates and updates go first, in a request of their own, so
// that a Task filed under a new list can name it by its real id.  One
// more request then carries the Task changes, the TaskList destroys
// (last, because a list must be emptied before it is destroyed), and a
// pull.  The calls in a request run in order, so the pull sees what the
// sets did.  List changes are rare, so a push is usually one request.
// -- claude, 2026-11-07
func (sy *syncer) out(ctx context.Context) (bool, error) {
	lists, err := sy.pendingRows(typeTaskList)
	if err != nil {
		return false, err
	}
	var listChanges, listDestroys []*row
	for _, r := range lists {
		if r.local == nil {
			listDestroys = append(listDestroys, r)
		} else {
			listChanges = append(listChanges, r)
		}
	}

	sentLists := false
	listSet, err := sy.prepareSet(typeTaskList, listChanges)
	if err != nil {
		return false, err
	}
	if listSet != nil {
		rs, err := sy.c.Batch(ctx, []jmap.Call{listSet.call})
		if err != nil {
			return false, err
		}
		if err := sy.recordSet(listSet, rs[0]); err != nil {
			return false, err
		}
		sentLists = true
	}

	// Read the pending Tasks only now: recording the list creates
	// rewrote the creation references in Tasks filed under them.
	tasks, err := sy.pendingRows(typeTask)
	if err != nil {
		return false, err
	}
	var sets []*setRequest
	var groups [][]jmap.Call
	for _, p := range []struct {
		typ  string
		rows []*row
	}{{typeTask, tasks}, {typeTaskList, listDestroys}} {
		req, err := sy.prepareSet(p.typ, p.rows)
		if err != nil {
			return false, err
		}
		if req != nil {
			sets = append(sets, req)
			groups = append(groups, []jmap.Call{req.call})
		}
	}
	if len(sets) == 0 && !sentLists {
		return false, nil
	}

	steps, err := sy.pullSteps()
	if err != nil {
		return false, err
	}
	rs, err := sy.c.Batch(ctx, append(groups, callGroups(steps)...)...)
	if err != nil {
		return false, err
	}
	for i, req := range sets {
		if err := sy.recordSet(req, rs[i]); err != nil {
			return false, err
		}
	}
	return true, sy.finishPull(ctx, steps, rs[len(sets):])
}

// setRequest is one /set call and what each of its creates, updates,
// and destroys was, for recording the response.
type setRequest struct {
	typ          string
	call         jmap.Call
	byCreationID map[string]sent
	byID         map[string]sent
}

// prepareSet builds one /set for the pending rows of one type, or returns
// nil if there is nothing to send.  A pending update that turns out to
// change nothing is settled here.
func (sy *syncer) prepareSet(typ string, rows []*row) (*setRequest, error) {
	create, update := map[string]any{}, map[string]any{}
	var destroy []string
	req := &setRequest{typ: typ, byCreationID: map[string]sent{}, byID: map[string]sent{}}

	for _, r := range rows {
		switch {
		case r.server == nil && r.local != nil:
			value := patch.Clone(r.local)
			if typ == typeTask {
				if hasCreationRef(value) {
					continue // its list could not be created; try again next time
				}
			}
			cid := fmt.Sprintf("k%d", r.key)
			create[cid] = value
			req.byCreationID[cid] = sent{r, r.pending, value}
		case r.server != nil && r.local != nil:
			p := patch.Diff(r.server, r.local)
			if typ == typeTask {
				if hasCreationRef(r.local) {
					continue
				}
			}
			if len(p) == 0 {
				if err := sy.settle(r, r.pending); err != nil {
					return nil, err
				}
				continue
			}
			update[r.jmapID] = p
			req.byID[r.jmapID] = sent{r, r.pending, patch.Clone(r.local)}
		case r.local == nil:
			destroy = append(destroy, r.jmapID)
			req.byID[r.jmapID] = sent{r, r.pending, nil}
		}
	}
	if len(create)+len(update)+len(destroy) == 0 {
		return nil, nil
	}

	args := map[string]any{"accountId": sy.account}
	if len(create) > 0 {
		args["create"] = create
	}
	if len(update) > 0 {
		args["update"] = update
	}
	if len(destroy) > 0 {
		args["destroy"] = destroy
	}
	req.call = jmap.Call{Name: typ + "/set", Args: args, ID: typ + ":set"}
	return req, nil
}

// recordSet records the outcome of a /set.
func (sy *syncer) recordSet(req *setRequest, r jmap.Response) error {
	if e := r.Err(); e != nil {
		return fmt.Errorf("%s/set: %w", req.typ, e)
	}
	resp := r.Args
	return sy.s.tx(func(tx *sql.Tx) error {
		created, _ := resp["created"].(patch.Object)
		for cid, v := range created {
			out, ok := req.byCreationID[cid]
			if !ok {
				continue
			}
			props, _ := v.(patch.Object)
			server := patch.Clone(out.value)
			for k, pv := range props {
				server[k] = pv
			}
			id, _ := props["id"].(string)
			if err := sy.recordSent(tx, out, id, server); err != nil {
				return err
			}
		}
		updated, _ := resp["updated"].(patch.Object)
		for id := range updated {
			if out, ok := req.byID[id]; ok {
				if err := sy.recordSent(tx, out, id, out.value); err != nil {
					return err
				}
			}
		}
		destroyed, _ := resp["destroyed"].([]any)
		for _, v := range destroyed {
			id, _ := v.(string)
			if out, ok := req.byID[id]; ok {
				if err := sy.recordSent(tx, out, id, nil); err != nil {
					return err
				}
			}
		}
		for _, field := range []string{"notCreated", "notUpdated", "notDestroyed"} {
			failed, _ := resp[field].(patch.Object)
			for id, v := range failed {
				out, ok := req.byCreationID[id]
				if !ok {
					out, ok = req.byID[id]
				}
				if !ok {
					continue
				}
				if err := sy.recordFailure(tx, req.typ, out, v); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// recordSent records that out reached the server, which now holds value
// (nil once destroyed) under id.  If the object changed locally while
// the request was out, it stays pending.
func (sy *syncer) recordSent(tx *sql.Tx, out sent, id string, value patch.Object) error {
	sy.res.Sent++
	r, err := queryRow(tx, `key = ?`, out.row.key)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if value == nil {
		if r.local == nil {
			return remove(tx, r.key)
		}
		// Recreated locally while being destroyed: it is a create again.
		r.jmapID, r.server = "", nil
		return put(tx, r)
	}
	r.jmapID, r.server, r.err = id, value, ""
	if r.pending == out.seq {
		r.pending = 0
		r.local = patch.Clone(value)
	} else if r.local != nil {
		// Changed again while the request was out.  The local value
		// still needs the properties the server set on create, such as
		// id, or the next diff would try to remove them.
		for k, v := range value {
			if _, ok := r.local[k]; !ok {
				r.local[k] = v
			}
		}
	}
	if err := put(tx, r); err != nil {
		return err
	}
	if r.typ == typeTaskList {
		return resolveListRefs(tx, r)
	}
	return nil
}

// recordFailure notes a per-object error on the row, which stays pending
// so the change is not lost.  A destroy of something already gone is no
// failure at all.
func (sy *syncer) recordFailure(tx *sql.Tx, typ string, out sent, v any) error {
	e, _ := v.(patch.Object)
	kind, _ := e["type"].(string)
	desc, _ := e["description"].(string)
	if kind == "notFound" && out.value == nil {
		return remove(tx, out.row.key)
	}
	msg := kind
	if desc != "" {
		msg += ": " + desc
	}
	sy.problem("%s %s: %s", typ, describe(out.row), msg)
	r, err := queryRow(tx, `key = ?`, out.row.key)
	if err != nil {
		return err
	}
	r.err = msg
	return put(tx, r)
}

func describe(r *row) string {
	o := r.local
	if o == nil {
		o = r.server
	}
	switch r.typ {
	case typeTask:
		if uid, _ := o["uid"].(string); uid != "" {
			return uid
		}
	case typeTaskList:
		if p, _ := o[propProject].(string); p != "" {
			return p
		}
	}
	return fmt.Sprintf("#%d", r.key)
}

// settle clears a pending mark that turned out to have nothing to send.
func (sy *syncer) settle(r *row, seq int64) error {
	return sy.s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE objects SET pending = 0 WHERE key = ? AND pending = ?`, r.key, seq)
		return err
	})
}

// resolveListRefs rewrites the creation reference to list in every
// Task's taskListIds with the list's new JMAP id.
func resolveListRefs(tx *sql.Tx, list *row) error {
	if list.jmapID == "" {
		return nil
	}
	tasks, err := queryRows(tx, `type = ? AND list_key = ?`, typeTask, list.key)
	if err != nil {
		return err
	}
	ref := creationRef(list.key)
	for _, t := range tasks {
		ids, _ := t.local["taskListIds"].(patch.Object)
		if t.local != nil && ids[ref] == true {
			delete(ids, ref)
			ids[list.jmapID] = true
			if err := put(tx, t); err != nil {
				return err
			}
		}
	}
	return nil
}

// in pulls what changed on the server, in one request when the server
// allows enough calls per request, which it nearly always does.
func (sy *syncer) in(ctx context.Context) error {
	steps, err := sy.pullSteps()
	if err != nil {
		return err
	}
	rs, err := sy.c.Batch(ctx, callGroups(steps)...)
	if err != nil {
		return err
	}
	return sy.finishPull(ctx, steps, rs)
}

func (sy *syncer) state(typ string) (string, error) {
	var st string
	err := sy.s.db.QueryRow(`SELECT state FROM states WHERE collection = ? AND type = ?`, sy.collection, typ).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return st, err
}

// pullStep is the calls that pull one type's changes since a state: its
// /changes, then /get of what was created and of what was updated, by
// result reference.  maxChanges keeps each /get within maxObjectsInGet.
// A step with no calls stands for a resync, which a type with no state
// needs.
type pullStep struct {
	typ   string
	calls []jmap.Call
}

func (sy *syncer) pullStep(typ, since string) pullStep {
	if since == "" {
		return pullStep{typ: typ}
	}
	changes := typ + ":changes"
	ref := func(path string) map[string]any {
		return map[string]any{"resultOf": changes, "name": typ + "/changes", "path": path}
	}
	return pullStep{typ, []jmap.Call{
		{Name: typ + "/changes", ID: changes, Args: map[string]any{
			"accountId": sy.account, "sinceState": since, "maxChanges": sy.c.MaxObjectsInGet,
		}},
		{Name: typ + "/get", ID: typ + ":created", Args: map[string]any{"accountId": sy.account, "#ids": ref("/created")}},
		{Name: typ + "/get", ID: typ + ":updated", Args: map[string]any{"accountId": sy.account, "#ids": ref("/updated")}},
	}}
}

// pullSteps returns a step for each type, TaskLists first so that Tasks
// can be filed under them.
func (sy *syncer) pullSteps() ([]pullStep, error) {
	var steps []pullStep
	for _, typ := range []string{typeTaskList, typeTask} {
		since, err := sy.state(typ)
		if err != nil {
			return nil, err
		}
		steps = append(steps, sy.pullStep(typ, since))
	}
	return steps, nil
}

func callGroups(steps []pullStep) [][]jmap.Call {
	var groups [][]jmap.Call
	for _, st := range steps {
		if st.calls != nil {
			groups = append(groups, st.calls)
		}
	}
	return groups
}

// finishPull applies the responses to the steps' calls, in order.  A type
// with no state, or whose state the server can no longer calculate
// changes from, is resynced; one with more changes than a pull carries is
// pulled again until it has none left.
func (sy *syncer) finishPull(ctx context.Context, steps []pullStep, rs []jmap.Response) error {
	for _, st := range steps {
		if len(rs) < len(st.calls) {
			return fmt.Errorf("store: pulling %s: missing responses", st.typ)
		}
		var mine []jmap.Response
		mine, rs = rs[:len(st.calls)], rs[len(st.calls):]
		for {
			if st.calls == nil {
				if err := sy.resync(ctx, st.typ); err != nil {
					return err
				}
				break
			}
			more, err := sy.applyPull(st.typ, mine)
			var me *jmap.MethodError
			if errors.As(err, &me) && me.Type == "cannotCalculateChanges" {
				st = pullStep{typ: st.typ}
				continue
			}
			if err != nil {
				return err
			}
			if !more {
				break
			}
			since, err := sy.state(st.typ)
			if err != nil {
				return err
			}
			st = sy.pullStep(st.typ, since)
			if mine, err = sy.c.Batch(ctx, st.calls); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyPull applies the responses to one pullStep's calls, and reports
// whether the server has more changes to give.
func (sy *syncer) applyPull(typ string, rs []jmap.Response) (bool, error) {
	if e := rs[0].Err(); e != nil {
		return false, fmt.Errorf("%s/changes: %w", typ, e)
	}
	var fetched []patch.Object
	for _, r := range rs[1:] {
		if e := r.Err(); e != nil {
			return false, fmt.Errorf("%s/get: %w", typ, e)
		}
		fetched = append(fetched, objects(r.Args["list"])...)
	}
	changes := rs[0].Args
	newState, _ := changes["newState"].(string)
	if err := sy.apply(typ, fetched, asList(changes["destroyed"]), nil, newState); err != nil {
		return false, err
	}
	more, _ := changes["hasMoreChanges"].(bool)
	return more, nil
}

func objects(v any) []patch.Object {
	list, _ := v.([]any)
	out := make([]patch.Object, 0, len(list))
	for _, e := range list {
		if o, ok := e.(patch.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// get fetches objects by id, in requests no larger than the server
// allows.
func (sy *syncer) get(ctx context.Context, typ string, ids []string) ([]patch.Object, error) {
	var out []patch.Object
	for _, ch := range jmap.Chunks(len(ids), sy.c.MaxObjectsInGet) {
		got, err := sy.c.One(ctx, typ+"/get", map[string]any{"accountId": sy.account, "ids": ids[ch[0]:ch[1]]})
		if err != nil {
			return nil, fmt.Errorf("%s/get: %w", typ, err)
		}
		out = append(out, objects(got["list"])...)
	}
	return out, nil
}

// resync fetches every object of a type, for a first sync or when the
// server cannot say what changed since our state.  Objects the server no
// longer has are dropped, except pending creates, which it has never had.
//
// TaskLists are few and have no /query, so they come in one /get.  Tasks
// may be many, so their ids come from Task/query, a page at a time, and
// are fetched in chunks.  The state recorded is the one from before the
// first page, so anything changed during the resync is fetched again on
// the next sync rather than missed.  -- claude, 2026-11-06
func (sy *syncer) resync(ctx context.Context, typ string) error {
	var fetched []patch.Object
	var st string
	if typ == typeTaskList {
		got, err := sy.c.One(ctx, typ+"/get", map[string]any{"accountId": sy.account, "ids": nil})
		if err != nil {
			return fmt.Errorf("%s/get: %w", typ, err)
		}
		fetched = objects(got["list"])
		st, _ = got["state"].(string)
	} else {
		got, err := sy.c.One(ctx, typ+"/get", map[string]any{"accountId": sy.account, "ids": []string{}})
		if err != nil {
			return fmt.Errorf("%s/get: %w", typ, err)
		}
		st, _ = got["state"].(string)
		var ids []string
		for {
			q, err := sy.c.One(ctx, typ+"/query", map[string]any{
				"accountId": sy.account, "position": len(ids), "limit": sy.c.MaxObjectsInGet,
			})
			if err != nil {
				return fmt.Errorf("%s/query: %w", typ, err)
			}
			page := asList(q["ids"])
			ids = append(ids, page...)
			if len(page) == 0 || len(page) < sy.c.MaxObjectsInGet {
				break
			}
		}
		if fetched, err = sy.get(ctx, typ, ids); err != nil {
			return err
		}
	}
	keep := map[string]bool{}
	for _, o := range fetched {
		id, _ := o["id"].(string)
		keep[id] = true
	}
	return sy.apply(typ, fetched, nil, keep, st)
}

func asList(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// apply records fetched objects and destroyed ids from the server and the
// new state, in one transaction.  When keep is not nil, it is the full set
// of ids the server holds, and anything else it once held is dropped.
func (sy *syncer) apply(typ string, fetched []patch.Object, destroyed []string, keep map[string]bool, newState string) error {
	return sy.s.tx(func(tx *sql.Tx) error {
		for _, o := range fetched {
			if err := sy.applyOne(tx, typ, o); err != nil {
				return err
			}
		}
		for _, id := range destroyed {
			if _, err := tx.Exec(`DELETE FROM objects WHERE collection = ? AND type = ? AND jmap_id = ?`, sy.collection, typ, id); err != nil {
				return err
			}
		}
		if keep != nil {
			gone, err := queryRows(tx, `collection = ? AND type = ? AND jmap_id IS NOT NULL`, sy.collection, typ)
			if err != nil {
				return err
			}
			for _, r := range gone {
				if !keep[r.jmapID] {
					if err := remove(tx, r.key); err != nil {
						return err
					}
				}
			}
		}
		_, err := tx.Exec(`INSERT INTO states (collection, type, state) VALUES (?, ?, ?)
			ON CONFLICT (collection, type) DO UPDATE SET state = excluded.state`, sy.collection, typ, newState)
		return err
	})
}

// applyOne records one object fetched from the server.
func (sy *syncer) applyOne(tx *sql.Tx, typ string, o patch.Object) error {
	id, _ := o["id"].(string)
	r, err := queryRow(tx, `collection = ? AND type = ? AND jmap_id = ?`, sy.collection, typ, id)
	if errors.Is(err, ErrNotFound) && typ == typeTask {
		// A pending create with this uid, made here while another machine
		// created the same thread on the server: they are one object.
		uid, _ := o["uid"].(string)
		r, err = queryRow(tx, `collection = ? AND type = ? AND uid = ? AND jmap_id IS NULL`, sy.collection, typ, uid)
		if err == nil {
			r.jmapID = id
		}
	}
	if errors.Is(err, ErrNotFound) {
		r = &row{collection: sy.collection, typ: typ, jmapID: id}
	} else if err != nil {
		return err
	}
	if r.server != nil && patch.Equal(r.server, o) && r.key != 0 {
		return nil
	}
	sy.res.Received++
	return sy.rebaseOnto(tx, r, o)
}

// rebaseOnto makes o the server version of r, replaying any pending local
// change on top of it, and files a Task under its list.
func (sy *syncer) rebaseOnto(tx *sql.Tx, r *row, o patch.Object) error {
	switch {
	case r.pending == 0:
		r.local = patch.Clone(o)
	case r.local != nil:
		rebased, conflicts := patch.Rebase(r.server, r.local, o, resolvers)
		for _, prop := range conflicts {
			sy.problem("%s %s: %s was changed both here and on the server; kept the local version", r.typ, describe(r), prop)
		}
		r.local = rebased
		if patch.Equal(rebased, o) {
			r.pending = 0
		}
	}
	r.server = patch.Clone(o)
	if r.typ == typeTask {
		if err := fileTask(tx, r); err != nil {
			return err
		}
	}
	return put(tx, r)
}

// fileTask sets a Task's list_key from its taskListIds: the first list
// that is an lt project, else the first list known at all.
func fileTask(tx *sql.Tx, r *row) error {
	o := r.local
	if o == nil {
		o = r.server
	}
	r.listKey = 0
	for _, ref := range listRefs(o) {
		var l *row
		var err error
		if isCreationRef(ref) {
			l, err = queryRow(tx, `collection = ? AND type = ? AND key = ?`, r.collection, typeTaskList, strings.TrimPrefix(ref, "#k"))
		} else {
			l, err = queryRow(tx, `collection = ? AND type = ? AND jmap_id = ?`, r.collection, typeTaskList, ref)
		}
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if r.listKey == 0 {
			r.listKey = l.key
		}
		if l.local != nil && l.local[propProject] != nil {
			r.listKey = l.key
			return nil
		}
	}
	return nil
}

// mergeDuplicates finds projects with more than one live list in this
// collection, as happens when two machines start the same project
// offline, and moves everything into one: the list with the lowest JMAP
// id, or the oldest if none has one.  The rule is deterministic, so two
// machines merging at once agree on the survivor.
func (sy *syncer) mergeDuplicates() (bool, error) {
	merged := false
	err := sy.s.tx(func(tx *sql.Tx) error {
		lists, err := queryRows(tx, `collection = ? AND type = ? AND project IS NOT NULL AND local IS NOT NULL`, sy.collection, typeTaskList)
		if err != nil {
			return err
		}
		byProject := map[string][]*row{}
		for _, l := range lists {
			p := l.local[propProject].(string)
			byProject[p] = append(byProject[p], l)
		}
		for _, group := range byProject {
			if len(group) < 2 {
				continue
			}
			sort.Slice(group, func(i, j int) bool {
				a, b := group[i], group[j]
				if (a.jmapID == "") != (b.jmapID == "") {
					return a.jmapID != ""
				}
				if a.jmapID != b.jmapID {
					return a.jmapID < b.jmapID
				}
				return a.key < b.key
			})
			if err := absorb(tx, group[1:], group[0]); err != nil {
				return err
			}
			merged = true
		}
		return nil
	})
	return merged, err
}
