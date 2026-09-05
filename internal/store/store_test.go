package store

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/loosethreads/internal/thread"
)

var now = time.Date(2026, 9, 4, 12, 40, 0, 0, time.FixedZone("EDT", -4*3600))

func checkDirName(t *testing.T, id, wantSlug string) {
	t.Helper()
	got := DirName(id)
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(wantSlug) + `-?[0-9a-f]{6}$`)
	if !re.MatchString(got) {
		t.Errorf("DirName(%q) = %q, want slug %q plus hash", id, got, wantSlug)
	}
	if got != DirName(id) {
		t.Errorf("DirName(%q) is not deterministic", id)
	}
}

func TestDirName(t *testing.T) {
	checkDirName(t, "github.com/rjbs/Dist-Zilla", "github.com-rjbs-dist-zilla")
	checkDirName(t, "/Users/rjbs/code/LooseThreads", "users-rjbs-code-loosethreads")
	checkDirName(t, "host/a  b--c", "host-a-b-c")
	checkDirName(t, "///", "")

	if DirName("rjbs/foo-bar") == DirName("rjbs/foo/bar") {
		t.Error("ids with the same slug must get different directories")
	}
	if DirName("Host/Foo") == DirName("host/foo") {
		t.Error("ids differing in case must get different directories")
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// addThread creates a thread in project id with the given body and returns
// it, so tests read as a sequence of declarations.  Each call is stamped a
// day after the previous one so that ids sort in call order.
var addClock = now

func addThread(t *testing.T, s *Store, projectID, body string) *thread.Thread {
	t.Helper()
	p, err := s.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	addClock = addClock.Add(24 * time.Hour)
	th := &thread.Thread{State: thread.Open, Body: body}
	if err := s.Create(p, th, addClock); err != nil {
		t.Fatal(err)
	}
	return th
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
	if len(got) != len(want) {
		t.Fatalf("projects = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("projects = %q, want %q", got, want)
			return
		}
	}
}

func checkTitles(t *testing.T, s *Store, projectID string, wantErrs int, want ...string) {
	t.Helper()
	p, err := s.LookupProject(projectID)
	if err != nil {
		t.Fatal(err)
	}
	ths, errs, err := s.Threads(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != wantErrs {
		t.Errorf("%s: %d parse errors, want %d: %v", projectID, len(errs), wantErrs, errs)
	}
	var got []string
	for _, th := range ths {
		got = append(got, th.Title())
	}
	if len(got) != len(want) {
		t.Fatalf("%s: titles = %q, want %q", projectID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: titles = %q, want %q", projectID, got, want)
			return
		}
	}
}

func TestProjects(t *testing.T) {
	s := newStore(t)
	checkProjects(t, s) // no root yet

	addThread(t, s, "github.com/rjbs/zed", "z")
	addThread(t, s, "github.com/rjbs/alpha", "a")
	checkProjects(t, s, "github.com/rjbs/alpha", "github.com/rjbs/zed")

	p, _ := s.LookupProject("github.com/rjbs/zed")
	p.Name = "Aardvark"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	checkProjects(t, s, "Aardvark", "github.com/rjbs/alpha")

	// A stray directory with no project.yaml is not a project.
	if err := os.MkdirAll(filepath.Join(s.Root, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkProjects(t, s, "Aardvark", "github.com/rjbs/alpha")

	if _, err := s.LookupProject("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LookupProject(nope) = %v, want ErrNotFound", err)
	}
}

func TestThreads(t *testing.T) {
	s := newStore(t)
	const pid = "github.com/rjbs/foo"

	first := addThread(t, s, pid, "First\n")
	addThread(t, s, pid, "Second\n\nWith body.\n")
	checkTitles(t, s, pid, 0, "First", "Second")

	p, _ := s.LookupProject(pid)

	got, err := s.Get(p, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title() != "First" || got.State != thread.Open || got.Created.IsZero() {
		t.Errorf("Get returned %+v", got)
	}

	if err := got.SetState(thread.Done, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(p, got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(p, first.ID)
	if again.State != thread.Done || again.Closed == nil {
		t.Errorf("state change did not persist: %+v", again)
	}

	if _, err := s.Get(p, "2026-01-01-zzzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}

	// A malformed file is reported, not fatal.
	if err := os.WriteFile(filepath.Join(p.Dir, "2026-09-04-bad!.md"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	ths, errs, err := s.Threads(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ths) != 2 || len(errs) != 1 {
		t.Errorf("got %d threads and %d errors, want 2 and 1", len(ths), len(errs))
	}
}

func TestThreadsSortByCreation(t *testing.T) {
	s := newStore(t)
	p, _ := s.Project("p")
	later := &thread.Thread{State: thread.Open, Body: "later\n"}
	earlier := &thread.Thread{State: thread.Open, Body: "earlier\n"}
	// Same day, so the ids share a date prefix; force the id order to
	// contradict the creation order.
	later.ID, later.Created = "2026-09-04-aaaa", now.Add(time.Hour)
	earlier.ID, earlier.Created = "2026-09-04-zzzz", now
	for _, th := range []*thread.Thread{later, earlier} {
		if err := s.Save(p, th); err != nil {
			t.Fatal(err)
		}
	}
	checkTitles(t, s, "p", 0, "earlier", "later")
}

func TestMismatched(t *testing.T) {
	s := newStore(t)
	addThread(t, s, "github.com/rjbs/right", "x")
	p, _ := s.LookupProject("github.com/rjbs/right")
	if p.Mismatched() {
		t.Error("freshly created project reports mismatch")
	}

	// Simulate a hand relocation that carried the wrong project.yaml along.
	if err := os.WriteFile(filepath.Join(p.Dir, "project.yaml"), []byte("id: /some/old/path\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := s.LookupProject("github.com/rjbs/right")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Mismatched() {
		t.Errorf("project.yaml with id %q in dir %s should be mismatched", p.ID, filepath.Base(p.Dir))
	}
	ps, _ := s.Projects()
	if len(ps) != 1 || !ps[0].Mismatched() {
		t.Errorf("Projects should report the mismatched project: %+v", ps)
	}
}

func TestRehome(t *testing.T) {
	s := newStore(t)
	a := addThread(t, s, "/old/path", "one")
	b := addThread(t, s, "/old/path", "two")
	old, _ := s.LookupProject("/old/path")

	moved, err := s.Rehome("/old/path", "github.com/rjbs/new")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 {
		t.Errorf("moved %v", moved)
	}
	checkTitles(t, s, "github.com/rjbs/new", 0, "one", "two")
	if _, err := os.Stat(old.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old directory still present: %v", err)
	}
	checkProjects(t, s, "github.com/rjbs/new")

	if _, err := s.Rehome("/old/path", "github.com/rjbs/new"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rehoming a gone project: %v, want ErrNotFound", err)
	}

	// A collision refuses to move anything.
	c := addThread(t, s, "/other/path", "three")
	newP, _ := s.LookupProject("github.com/rjbs/new")
	dup := &thread.Thread{ID: c.ID, State: thread.Open, Body: "dup\n", Created: now}
	if err := s.Save(newP, dup); err != nil {
		t.Fatal(err)
	}
	addThread(t, s, "/other/path", "four")
	if _, err := s.Rehome("/other/path", "github.com/rjbs/new"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("collision: %v", err)
	}
	checkTitles(t, s, "/other/path", 0, "three", "four")
	_ = a
	_ = b

	// An empty source project moves nothing and is left alone.
	if _, err := s.Project("/empty/path"); err != nil {
		t.Fatal(err)
	}
	moved, err = s.Rehome("/empty/path", "github.com/rjbs/new")
	if err != nil || moved != nil {
		t.Errorf("empty source: moved %v, err %v", moved, err)
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
	checkRoute(t, c, "github.com/rjbs", LocalCollection) // no trailing segment
	checkRoute(t, c, "example.com/x/secret1", "hidden")
	checkRoute(t, c, "example.com/other", LocalCollection)
	checkRoute(t, &Config{}, "anything", LocalCollection)
}

func TestCollections(t *testing.T) {
	s := newStore(t)
	s.Config().Routes = []Route{{"github.com/work/*", "work"}}
	s.Config().Collections = map[string]CollectionConfig{"work": {Remote: "git@example.com:threads.git"}}
	if err := s.SaveConfig(); err != nil {
		t.Fatal(err)
	}

	work := addThread(t, s, "github.com/work/thing", "w")
	home := addThread(t, s, "github.com/home/thing", "h")
	_ = work
	_ = home

	wp, _ := s.LookupProject("github.com/work/thing")
	hp, _ := s.LookupProject("github.com/home/thing")
	if wp.Collection != "work" || filepath.Base(filepath.Dir(wp.Dir)) != "work" {
		t.Errorf("work project in %q at %s", wp.Collection, wp.Dir)
	}
	if hp.Collection != LocalCollection {
		t.Errorf("unrouted project in %q", hp.Collection)
	}
	checkProjects(t, s, "github.com/home/thing", "github.com/work/thing")

	names, _ := s.Collections()
	if strings.Join(names, ",") != "local,work" {
		t.Errorf("collections %v", names)
	}
	if s.Config().Remote("work") == "" || s.Config().Remote(LocalCollection) != "" {
		t.Error("remotes wrong")
	}

	// Reopening reads the config back.
	again, err := Open(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Config().Routes) != 1 {
		t.Errorf("config not persisted: %+v", again.Config())
	}

	moved, err := s.MoveProject("github.com/home/thing", "work")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Collection != "work" {
		t.Errorf("moved to %q", moved.Collection)
	}
	checkTitles(t, s, "github.com/home/thing", 0, "h")
	if _, err := s.MoveProject("github.com/home/thing", "work"); err != nil {
		t.Errorf("moving to where it already is should be a no-op: %v", err)
	}
}

func TestMigrateFlatLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	old := filepath.Join(root, DirName("github.com/rjbs/old"))
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(old, "project.yaml"), []byte("id: github.com/rjbs/old\n"), 0o644)
	os.WriteFile(filepath.Join(old, "2026-01-01-abcdef.md"), []byte("---\nstate: open\n---\nOld\n"), 0o644)

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.LookupProject("github.com/rjbs/old")
	if err != nil {
		t.Fatal(err)
	}
	if p.Collection != LocalCollection {
		t.Errorf("migrated into %q", p.Collection)
	}
	checkTitles(t, s, "github.com/rjbs/old", 0, "Old")
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Error("old directory still present")
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	s := newStore(t)
	addThread(t, s, "p", "x")
	p, _ := s.LookupProject("p")
	entries, _ := os.ReadDir(p.Dir)
	for _, e := range entries {
		if e.Name()[0] == '.' {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
