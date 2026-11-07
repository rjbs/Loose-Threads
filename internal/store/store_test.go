package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/loosethreads/internal/patch"
	"github.com/rjbs/loosethreads/internal/thread"
)

var now = time.Date(2026, 9, 4, 12, 40, 0, 0, time.FixedZone("EDT", -4*3600))

func newStore(t *testing.T) *Store {
	t.Helper()
	return newStoreWithConfig(t, "")
}

func newStoreWithConfig(t *testing.T, config string) *Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	if config != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, configFile), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// addThread creates a thread in project id with the given body and returns
// it, so tests read as a sequence of declarations.  Each call is stamped a
// day after the previous one so that ids sort in call order.
var addClock = now

func addThread(t *testing.T, s *Store, projectID, body string) *thread.Thread {
	t.Helper()
	return addThreadWithID(t, s, projectID, "", body)
}

func addThreadWithID(t *testing.T, s *Store, projectID, id, body string) *thread.Thread {
	t.Helper()
	p, err := s.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	addClock = addClock.Add(24 * time.Hour)
	th := &thread.Thread{ID: id, State: thread.Open, Body: body}
	if err := s.Create(p, th, addClock); err != nil {
		t.Fatal(err)
	}
	return th
}

func project(t *testing.T, s *Store, id string) *Project {
	t.Helper()
	p, err := s.LookupProject(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func checkProjects(t *testing.T, s *Store, want ...string) {
	t.Helper()
	ps, err := s.Projects()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.DisplayName())
	}
	if !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
		t.Errorf("projects = %q, want %q", got, want)
	}
}

func checkTitles(t *testing.T, s *Store, projectID string, want ...string) {
	t.Helper()
	ths, err := s.Threads(project(t, s, projectID))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, th := range ths {
		got = append(got, th.Title())
	}
	if !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
		t.Errorf("%s: titles = %q, want %q", projectID, got, want)
	}
}

func checkErrorContains(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: error %v, want one containing %q", what, err, want)
	}
}

func TestProjects(t *testing.T) {
	s := newStore(t)
	checkProjects(t, s)

	addThread(t, s, "github.com/rjbs/zed", "z")
	addThread(t, s, "github.com/rjbs/alpha", "a")
	checkProjects(t, s, "github.com/rjbs/alpha", "github.com/rjbs/zed")

	if _, err := s.LookupProject("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LookupProject(nope) = %v, want ErrNotFound", err)
	}
}

func TestThreads(t *testing.T) {
	s := newStore(t)
	const pid = "github.com/rjbs/foo"

	first := addThread(t, s, pid, "First\n")
	addThread(t, s, pid, "Second\n\nWith body.\n")
	checkTitles(t, s, pid, "First", "Second")

	p := project(t, s, pid)
	got, err := s.Get(p, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title() != "First" || got.State != thread.Open || !got.Created.Equal(first.Created) {
		t.Errorf("Get returned %+v", got)
	}

	if err := got.SetState(thread.Done, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(p, got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(p, first.ID)
	if again.State != thread.Done || again.Closed == nil || !again.Closed.Equal(now) {
		t.Errorf("state change did not persist: %+v", again)
	}

	if _, err := s.Get(p, "2026-01-01-zzzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}
}

func TestThreadsSortByCreation(t *testing.T) {
	s := newStore(t)
	p, _ := s.Project("p")
	later := &thread.Thread{ID: "2026-09-04-aaaa", State: thread.Open, Body: "later\n", Created: now.Add(time.Hour)}
	earlier := &thread.Thread{ID: "2026-09-04-zzzz", State: thread.Open, Body: "earlier\n", Created: now}
	for _, th := range []*thread.Thread{later, earlier} {
		if err := s.Create(p, th, now); err != nil {
			t.Fatal(err)
		}
	}
	checkTitles(t, s, "p", "earlier", "later")
}

// checkRoundTrip saves th and reads it back, expecting it unchanged.
func checkRoundTrip(t *testing.T, name string, th *thread.Thread) {
	t.Helper()
	s := newStore(t)
	p, _ := s.Project("p")
	if err := s.Create(p, th, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(p, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	same := got.ID == th.ID && got.State == th.State && got.Created.Equal(th.Created) &&
		got.Session == th.Session && got.Origin == th.Origin && got.Body == th.Body &&
		(got.Closed == nil) == (th.Closed == nil) && (got.Closed == nil || got.Closed.Equal(*th.Closed))
	if !same {
		t.Errorf("%s: round trip\n got: %+v\nwant: %+v", name, got, th)
	}
}

func TestRoundTrip(t *testing.T) {
	closed := now.Add(time.Hour)
	checkRoundTrip(t, "title only",
		&thread.Thread{ID: "2026-09-04-aaaaaa", State: thread.Open, Created: now, Body: "Just a title\n"})
	checkRoundTrip(t, "everything",
		&thread.Thread{ID: "2026-09-04-bbbbbb", State: thread.Done, Created: now, Closed: &closed,
			Session: "abc-123", Origin: thread.OriginAgent,
			Body: "Title: with colon\n\nBody paragraph.\n\n**2026-09-04 13:40 (agent):**\nDone: yes.\n"})
	checkRoundTrip(t, "abandoned by a human",
		&thread.Thread{ID: "2026-09-04-cccccc", State: thread.Abandoned, Created: now, Closed: &closed,
			Origin: thread.OriginHuman, Body: "Nope\n\nnot doing it\n"})
}

// checkTask creates th and compares the Task stored for it with want,
// given as JSON.
func checkTask(t *testing.T, th *thread.Thread, want string) {
	t.Helper()
	s := newStore(t)
	p, _ := s.Project("p")
	if err := s.Create(p, th, now); err != nil {
		t.Fatal(err)
	}
	r, err := taskRow(s.db, p, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := mustObject(t, want)
	w["taskListIds"] = r.local["taskListIds"]
	if !patch.Equal(r.local, w) {
		t.Errorf("task = %v\nwant %v", r.local, w)
	}
}

func TestTaskShape(t *testing.T) {
	closed := now.Add(time.Hour)
	checkTask(t,
		&thread.Thread{ID: "2026-09-04-aaaaaa", State: thread.Done, Created: now, Closed: &closed,
			Session: "s-1", Origin: thread.OriginAgent, Body: "The title\n\nThe body.\n"},
		`{"@type":"Task","uid":"2026-09-04-aaaaaa","title":"The title","description":"The body.\n",
		  "created":"2026-09-04T16:40:00Z","progress":"completed","progressUpdated":"2026-09-04T17:40:00Z",
		  "lt.semiotic.systems:session":"s-1","keywords":{"lt.semiotic.systems:origin:agent":true}}`)
	checkTask(t,
		&thread.Thread{ID: "2026-09-04-bbbbbb", State: thread.Open, Created: now, Body: "Bare\n"},
		`{"@type":"Task","uid":"2026-09-04-bbbbbb","title":"Bare","description":"",
		  "created":"2026-09-04T16:40:00Z","progress":"needs-action"}`)
}

// checkForeignTask reads a Task some other client made and compares the
// thread lt sees.
func checkForeignTask(t *testing.T, task string, state thread.State, body string, created time.Time) {
	t.Helper()
	th := threadFromTask(mustObject(t, task))
	if th.State != state || th.Body != body || !th.Created.Equal(created) || th.Session != "" || th.Origin != "" {
		t.Errorf("thread from %s = %+v", task, th)
	}
}

func TestForeignTasks(t *testing.T) {
	updated := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	checkForeignTask(t, `{"uid":"u1","title":"T","updated":"2026-10-01T12:00:00Z"}`, thread.Open, "T\n", updated)
	checkForeignTask(t, `{"uid":"u2","title":"T","progress":"in-process","created":"2026-10-01T12:00:00Z"}`, thread.Open, "T\n", updated)
	checkForeignTask(t, `{"uid":"u3","title":"T","progress":"failed","created":"2026-10-01T12:00:00Z"}`, thread.Abandoned, "T\n", updated)
}

func TestNonPlainDescriptionRefused(t *testing.T) {
	th := &thread.Thread{ID: "x", State: thread.Open, Body: "T\n\nmore\n"}
	task := patch.Object{"uid": "x", "title": "T", "description": "<p>hi</p>", "descriptionContentType": "text/html"}
	_, err := applyThread(task, th)
	checkErrorContains(t, "appending to HTML", err, "text/html")
}

func TestRehome(t *testing.T) {
	s := newStore(t)
	addThread(t, s, "/old/path", "one")
	addThread(t, s, "/old/path", "two")

	moved, err := s.Rehome("/old/path", "github.com/rjbs/new")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 {
		t.Errorf("moved %v", moved)
	}
	checkTitles(t, s, "github.com/rjbs/new", "one", "two")
	checkProjects(t, s, "github.com/rjbs/new")

	if _, err := s.Rehome("/old/path", "github.com/rjbs/new"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rehoming a gone project: %v, want ErrNotFound", err)
	}

	// A collision refuses to move anything.
	c := addThread(t, s, "/other/path", "three")
	addThreadWithID(t, s, "github.com/rjbs/new", c.ID, "dup\n")
	addThread(t, s, "/other/path", "four")
	_, err = s.Rehome("/other/path", "github.com/rjbs/new")
	checkErrorContains(t, "collision", err, "already exists")
	checkTitles(t, s, "/other/path", "three", "four")

	// An empty source project moves nothing.
	if _, err := s.Project("/empty/path"); err != nil {
		t.Fatal(err)
	}
	moved, err = s.Rehome("/empty/path", "github.com/rjbs/new")
	if err != nil || moved != nil {
		t.Errorf("empty source: moved %v, err %v", moved, err)
	}
}

func TestMove(t *testing.T) {
	s := newStore(t)
	a := addThread(t, s, "github.com/rjbs/one", "one")
	addThread(t, s, "github.com/rjbs/one", "stays")
	b := addThread(t, s, "github.com/rjbs/two", "two")
	one := project(t, s, "github.com/rjbs/one")
	two := project(t, s, "github.com/rjbs/two")

	if err := s.Move(one, two, a.ID); err != nil {
		t.Fatal(err)
	}
	checkTitles(t, s, "github.com/rjbs/one", "stays")
	checkTitles(t, s, "github.com/rjbs/two", "one", "two")

	if err := s.Move(one, two, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("moving a gone thread: %v, want ErrNotFound", err)
	}
	checkErrorContains(t, "same project", s.Move(two, two, b.ID), "already in")

	addThreadWithID(t, s, "github.com/rjbs/one", b.ID, "dup\n")
	checkErrorContains(t, "collision", s.Move(two, one, b.ID), "already exists")
	checkTitles(t, s, "github.com/rjbs/one", "stays", "dup")
	checkTitles(t, s, "github.com/rjbs/two", "one", "two")
}

func TestMoveProject(t *testing.T) {
	s := newStore(t)
	a := addThread(t, s, "p", "one")
	addThread(t, s, "p", "two")

	p, err := s.MoveProject("p", "work")
	if err != nil {
		t.Fatal(err)
	}
	if p.Collection != "work" {
		t.Errorf("moved project is in %q", p.Collection)
	}
	checkTitles(t, s, "p", "one", "two")
	if got, _ := s.Get(p, a.ID); got == nil || got.Title() != "one" {
		t.Errorf("thread %s did not keep its id across the move", a.ID)
	}
	// Nothing in local was ever synced, so nothing is left behind there.
	n := 0
	s.db.QueryRow(`SELECT count(*) FROM objects WHERE collection = 'local'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d objects left in local", n)
	}
}

func checkRoute(t *testing.T, c *Config, id, want string) {
	t.Helper()
	if got := c.Route(id); got != want {
		t.Errorf("Route(%q) = %q, want %q", id, got, want)
	}
}

func TestRouting(t *testing.T) {
	c := &Config{Routes: []Route{
		{"github.com/fastmail/*", "work"},
		{"gitbox.fastmail.com/*", "work"},
		{"github.com/rjbs/*", "personal"},
		{"*/secret?", "hidden"},
	}}
	checkRoute(t, c, "github.com/fastmail/cyrus", "work")
	checkRoute(t, c, "gitbox.fastmail.com/a/b/c", "work")
	checkRoute(t, c, "github.com/rjbs/Dist-Zilla", "personal")
	checkRoute(t, c, "github.com/rjbs", LocalCollection)
	checkRoute(t, c, "example.com/x/secret1", "hidden")
	checkRoute(t, c, "example.com/other", LocalCollection)
	checkRoute(t, &Config{}, "anything", LocalCollection)
}

func checkEndpoint(t *testing.T, c *Config, collection string, want Endpoint, ok bool) {
	t.Helper()
	got, gok := c.Endpoint(collection)
	if gok != ok || (ok && got != want) {
		t.Errorf("Endpoint(%q) = %+v, %v; want %+v, %v", collection, got, gok, want, ok)
	}
}

func TestEndpoint(t *testing.T) {
	c := &Config{
		Server: "http://tm/jmap/session",
		Token:  "store-token",
		Collections: map[string]CollectionConfig{
			"personal":  {Account: "rjbs"},
			"own":       {Account: "rjbs", Token: "own-token"},
			"work":      {Account: "work", Server: "http://other/jmap/session"},
			"elsewhere": {Account: "x", Server: "http://else/jmap/session", Token: "else-token"},
			"unsynced":  {},
		},
	}
	checkEndpoint(t, c, "personal", Endpoint{"http://tm/jmap/session", "store-token", "rjbs"}, true)
	checkEndpoint(t, c, "own", Endpoint{"http://tm/jmap/session", "own-token", "rjbs"}, true)
	checkEndpoint(t, c, "work", Endpoint{"http://other/jmap/session", "", "work"}, true)
	checkEndpoint(t, c, "elsewhere", Endpoint{"http://else/jmap/session", "else-token", "x"}, true)
	checkEndpoint(t, c, "unsynced", Endpoint{}, false)
	checkEndpoint(t, c, LocalCollection, Endpoint{}, false)
	checkEndpoint(t, &Config{Collections: map[string]CollectionConfig{"x": {Account: "a"}}}, "x", Endpoint{}, false)
}

func TestRoutedProjectLandsInCollection(t *testing.T) {
	s := newStoreWithConfig(t, "routes:\n  - { match: \"github.com/rjbs/*\", collection: personal }\n")
	addThread(t, s, "github.com/rjbs/x", "x")
	addThread(t, s, "elsewhere", "y")
	if p := project(t, s, "github.com/rjbs/x"); p.Collection != "personal" {
		t.Errorf("routed project in %q", p.Collection)
	}
	if p := project(t, s, "elsewhere"); p.Collection != LocalCollection {
		t.Errorf("unrouted project in %q", p.Collection)
	}
}

func mustObject(t *testing.T, s string) patch.Object {
	t.Helper()
	var o patch.Object
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatalf("bad test JSON %s: %v", s, err)
	}
	return o
}
