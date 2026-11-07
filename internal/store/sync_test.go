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
