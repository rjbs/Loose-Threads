package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/loosethreads/internal/jmaptest"
	"github.com/rjbs/loosethreads/internal/patch"
	"github.com/rjbs/loosethreads/internal/thread"
)

// machine is one lt installation: its own store, syncing the personal
// collection with the personal account on srv, and work with work.
func machine(t *testing.T, srv *jmaptest.Server) *Store {
	t.Helper()
	return newStoreWithConfig(t, fmt.Sprintf(`server: %s
collections:
  personal: { account: personal }
  work: { account: work }
routes:
  - { match: "*", collection: personal }
`, srv.SessionURL()))
}

func newServer(t *testing.T) *jmaptest.Server {
	t.Helper()
	srv := jmaptest.New("personal", "work")
	t.Cleanup(srv.Close)
	return srv
}

func sync(t *testing.T, s *Store, collection string) SyncResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := s.Sync(ctx, collection)
	if err != nil {
		t.Fatalf("sync %s: %v", collection, err)
	}
	return r
}

// syncAll syncs each machine in turn, twice, so that everything any of
// them did reaches all of them.
func syncAll(t *testing.T, machines ...*Store) {
	t.Helper()
	for range 2 {
		for _, m := range machines {
			sync(t, m, "personal")
		}
	}
}

func appendTo(t *testing.T, s *Store, projectID, id, text string) {
	t.Helper()
	p := project(t, s, projectID)
	th, err := s.Get(p, id)
	if err != nil {
		t.Fatal(err)
	}
	th.Append(text, thread.OriginAgent, now)
	if err := s.Save(p, th); err != nil {
		t.Fatal(err)
	}
}

func resolve(t *testing.T, s *Store, projectID, id string, state thread.State, note string) {
	t.Helper()
	p := project(t, s, projectID)
	th, err := s.Get(p, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := th.Resolve(state, note, thread.OriginHuman, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(p, th); err != nil {
		t.Fatal(err)
	}
}

// checkThread asserts that a thread reads the same on every machine, with
// the given state and with each of the given texts in its body, in order.
func checkThread(t *testing.T, what, projectID, id string, state thread.State, texts []string, machines ...*Store) {
	t.Helper()
	for i, s := range machines {
		p, err := s.LookupProject(projectID)
		if err != nil {
			t.Errorf("%s: machine %d: %v", what, i, err)
			continue
		}
		th, err := s.Get(p, id)
		if err != nil {
			t.Errorf("%s: machine %d: %v", what, i, err)
			continue
		}
		if th.State != state {
			t.Errorf("%s: machine %d: state %s, want %s", what, i, th.State, state)
		}
		rest := th.Body
		for _, text := range texts {
			at := strings.Index(rest, text)
			if at < 0 {
				t.Errorf("%s: machine %d: body lacks %q (in order):\n%s", what, i, text, th.Body)
				break
			}
			rest = rest[at+len(text):]
		}
	}
}

func checkServerCount(t *testing.T, srv *jmaptest.Server, acct, typ string, want int) {
	t.Helper()
	if got := len(srv.Objects(acct, typ)); got != want {
		t.Errorf("server %s has %d %s objects, want %d", acct, got, typ, want)
	}
}

func checkNothingPending(t *testing.T, s *Store) {
	t.Helper()
	var n int
	s.db.QueryRow(`SELECT count(*) FROM objects WHERE pending > 0`).Scan(&n)
	if n != 0 {
		t.Errorf("%d objects still pending", n)
	}
}

func TestSyncRoundTrip(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)

	th := addThread(t, a, "p", "Title\n\nBody text.\n")
	syncAll(t, a, b)
	checkThread(t, "after sync", "p", th.ID, thread.Open, []string{"Title", "Body text."}, a, b)
	checkServerCount(t, srv, "personal", "Task", 1)
	checkServerCount(t, srv, "personal", "TaskList", 1)
	checkNothingPending(t, a)
	checkNothingPending(t, b)

	if r := sync(t, a, "personal"); r.Sent != 0 || r.Received != 0 {
		t.Errorf("a quiet sync did something: %s", r)
	}
}

func TestSyncConcurrentAppends(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	th := addThread(t, a, "p", "Title\n\nBody.\n")
	syncAll(t, a, b)

	appendTo(t, a, "p", th.ID, "note from A")
	appendTo(t, b, "p", th.ID, "note from B")
	syncAll(t, a, b)
	checkThread(t, "both notes", "p", th.ID, thread.Open, []string{"Body.", "note from A", "note from B"}, a, b)
}

func TestSyncStateAndAppend(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	th := addThread(t, a, "p", "Title\n")
	syncAll(t, a, b)

	resolve(t, b, "p", th.ID, thread.Done, "fixed it")
	appendTo(t, a, "p", th.ID, "more context")
	syncAll(t, a, b)
	checkThread(t, "closed with both notes", "p", th.ID, thread.Done, []string{"more context", "Done: fixed it"}, a, b)
}

func TestSyncForeignEdit(t *testing.T) {
	srv := newServer(t)
	a := machine(t, srv)
	th := addThread(t, a, "p", "Title\n\nBody.\n")
	sync(t, a, "personal")

	task := srv.Objects("personal", "Task")[0]
	srv.Update("personal", "Task", task["id"].(string), patch.Patch{"title": "Retitled", "priority": float64(3)})
	appendTo(t, a, "p", th.ID, "a note")
	sync(t, a, "personal")

	checkThread(t, "retitled, with note", "p", th.ID, thread.Open, []string{"Retitled", "a note"}, a)
	if got := srv.Objects("personal", "Task")[0]; got["priority"] != float64(3) {
		t.Errorf("foreign property lost: %v", got)
	}
}

func TestSyncDuplicateProjectLists(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	ta := addThread(t, a, "p", "From A\n")
	tb := addThread(t, b, "p", "From B\n")
	syncAll(t, a, b)

	checkServerCount(t, srv, "personal", "TaskList", 1)
	checkServerCount(t, srv, "personal", "Task", 2)
	checkTitles(t, a, "p", "From A", "From B")
	checkTitles(t, b, "p", "From A", "From B")
	checkThread(t, "a's thread", "p", ta.ID, thread.Open, nil, a, b)
	checkThread(t, "b's thread", "p", tb.ID, thread.Open, nil, a, b)
}

func TestSyncSameUIDTwice(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	// The same thread imported on two machines, as a re-run migration
	// would do.
	addThreadWithID(t, a, "p", "2026-09-04-same01", "Imported\n")
	addThreadWithID(t, b, "p", "2026-09-04-same01", "Imported\n")
	syncAll(t, a, b)
	checkServerCount(t, srv, "personal", "Task", 1)
	checkTitles(t, a, "p", "Imported")
	checkTitles(t, b, "p", "Imported")
}

func TestSyncWithoutChanges(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	th := addThread(t, a, "p", "Title\n")
	syncAll(t, a, b)

	appendTo(t, a, "p", th.ID, "after the server forgot")
	srv.ForgetChanges = true
	syncAll(t, a, b)
	checkThread(t, "full resync", "p", th.ID, thread.Open, []string{"after the server forgot"}, a, b)
}

func TestSyncMoveProject(t *testing.T) {
	srv := newServer(t)
	a := machine(t, srv)
	th := addThread(t, a, "p", "Moving\n")
	sync(t, a, "personal")

	if _, err := a.MoveProject("p", "work"); err != nil {
		t.Fatal(err)
	}
	sync(t, a, "work")
	sync(t, a, "personal")
	checkServerCount(t, srv, "personal", "Task", 0)
	checkServerCount(t, srv, "personal", "TaskList", 0)
	checkServerCount(t, srv, "work", "Task", 1)
	checkThread(t, "moved", "p", th.ID, thread.Open, []string{"Moving"}, a)
	checkNothingPending(t, a)
}

func TestSyncMoveThread(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	th := addThread(t, a, "one", "Wanderer\n")
	addThread(t, a, "two", "Resident\n")
	syncAll(t, a, b)

	if err := a.Move(project(t, a, "one"), project(t, a, "two"), th.ID); err != nil {
		t.Fatal(err)
	}
	syncAll(t, a, b)
	checkTitles(t, b, "two", "Wanderer", "Resident")
	checkTitles(t, b, "one")
	checkServerCount(t, srv, "personal", "Task", 2)
}

func TestSyncUnsyncedCollection(t *testing.T) {
	srv := newServer(t)
	a := machine(t, srv)
	ctx := context.Background()
	if _, err := a.Sync(ctx, "local"); err == nil {
		t.Error("syncing the local collection should fail")
	}
}

func TestSyncManyThreads(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	// b syncs while there is nothing, so that it later pulls everything
	// as changes, more than one pull can carry.
	sync(t, b, "personal")
	var want []string
	for i := range 2*jmaptest.MaxObjectsInGet + 1 {
		title := fmt.Sprintf("Thread %02d", i)
		addThread(t, a, "p", title+"\n")
		want = append(want, title)
	}
	syncAll(t, a, b)
	checkTitles(t, b, "p", want...)

	// A machine that starts from nothing, with a server that cannot say
	// what changed, both fetch everything in pages and chunks.
	srv.ForgetChanges = true
	c := machine(t, srv)
	sync(t, c, "personal")
	checkTitles(t, c, "p", want...)
	checkServerCount(t, srv, "personal", "Task", len(want))
}

// checkRequests asserts how many API requests a sync makes after change,
// against a server that allows 16 calls in a request, the RFC's suggested
// minimum.  change is given a store already synced, with project p
// holding thread id.
func checkRequests(t *testing.T, what string, change func(s *Store, id string), want int) {
	t.Helper()
	srv := newServer(t)
	srv.MaxCallsInRequest = 16
	a := machine(t, srv)
	th := addThread(t, a, "p", "Title\n")
	sync(t, a, "personal")

	change(a, th.ID)
	before := srv.Requests
	sync(t, a, "personal")
	if got := srv.Requests - before; got != want {
		t.Errorf("%s: sync made %d requests, want %d", what, got, want)
	}
}

func TestSyncRequests(t *testing.T) {
	checkRequests(t, "nothing to do", func(s *Store, id string) {}, 1)
	checkRequests(t, "a note", func(s *Store, id string) { appendTo(t, s, "p", id, "a note") }, 2)
	checkRequests(t, "a new thread", func(s *Store, id string) { addThread(t, s, "p", "New\n") }, 2)
	checkRequests(t, "a new project", func(s *Store, id string) { addThread(t, s, "q", "New\n") }, 3)
}

// checkLostResponse asserts that when the response to the nth request of
// a sync is lost, the next sync puts things right: a new thread in a new
// project reaches the server once, and nothing is left pending.
func checkLostResponse(t *testing.T, n int) {
	t.Helper()
	srv := newServer(t)
	srv.MaxCallsInRequest = 16
	a := machine(t, srv)
	addThread(t, a, "p", "Old\n")
	sync(t, a, "personal")

	addThread(t, a, "q", "New\n")
	srv.DropRequest = srv.Requests + n
	if _, err := a.Sync(context.Background(), "personal"); err == nil {
		t.Errorf("request %d: sync succeeded despite the lost response", n)
	}
	sync(t, a, "personal")
	checkServerCount(t, srv, "personal", "TaskList", 2)
	checkServerCount(t, srv, "personal", "Task", 2)
	checkTitles(t, a, "q", "New")
	checkNothingPending(t, a)
}

func TestSyncLostResponse(t *testing.T) {
	checkLostResponse(t, 1) // the pull
	checkLostResponse(t, 2) // the TaskList create
	checkLostResponse(t, 3) // the Task create, and the pull after it
}

// checkToken asserts whether a machine configured with a token can sync
// with a server that requires one.
func checkToken(t *testing.T, required, given string, ok bool) {
	t.Helper()
	srv := newServer(t)
	srv.Token = required
	a := newStoreWithConfig(t, fmt.Sprintf(`server: %s
token: %q
collections:
  work: { account: work }
routes:
  - { match: "*", collection: work }
`, srv.SessionURL(), given))
	addThread(t, a, "p", "Title\n")
	_, err := a.Sync(context.Background(), "work")
	if (err == nil) != ok {
		t.Errorf("server wants %q, machine gives %q: sync error %v", required, given, err)
	}
	if ok {
		checkServerCount(t, srv, "work", "Task", 1)
	}
}

func TestSyncToken(t *testing.T) {
	checkToken(t, "secret", "secret", true)
	checkToken(t, "secret", "wrong", false)
	checkToken(t, "secret", "", false)
	checkToken(t, "", "", true)
}

// checkPublish asserts that threads kept in the local collection reach
// no server until their project is moved into a collection that syncs,
// and then reach it with their notes and states intact.
func checkPublish(t *testing.T, collection string) {
	t.Helper()
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	p, err := a.ProjectIn("private", "Private", LocalCollection)
	if err != nil {
		t.Fatal(err)
	}
	th := &thread.Thread{State: thread.Open, Body: "Kept here\n"}
	if err := a.Create(p, th, now); err != nil {
		t.Fatal(err)
	}
	appendTo(t, a, "private", th.ID, "a local note")
	sync(t, a, collection)
	checkServerCount(t, srv, collection, "Task", 0)

	if _, err := a.MoveProject("private", collection); err != nil {
		t.Fatal(err)
	}
	sync(t, a, collection)
	sync(t, b, collection)
	checkServerCount(t, srv, collection, "Task", 1)
	checkThread(t, "published", "private", th.ID, thread.Open, []string{"Kept here", "a local note"}, a, b)
	checkNothingPending(t, a)
}

func TestSyncPublishLocalProject(t *testing.T) {
	checkPublish(t, "personal")
	checkPublish(t, "work")
}
