package store

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	return &Store{Root: filepath.Join(t.TempDir(), "store")}
}

// addThread creates a thread in project id with the given body and returns
// it, so tests read as a sequence of declarations.
func addThread(t *testing.T, s *Store, projectID, body string) *thread.Thread {
	t.Helper()
	p, err := s.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	th := &thread.Thread{State: thread.Open, Body: body}
	if err := s.Create(p, th, now); err != nil {
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

	// Ids from the same instant differ only in the random suffix, so
	// sort order between them is arbitrary; check that both are present
	// rather than their order.
	p, _ := s.LookupProject(pid)
	ths, _, _ := s.Threads(p)
	if len(ths) != 2 || ths[0].ID == ths[1].ID {
		t.Fatalf("expected two distinct threads, got %+v", ths)
	}

	got, err := s.Get(p, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title() != "First" || got.State != thread.Open || !got.Created.Equal(now) {
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
