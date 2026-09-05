package sync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rjbs/loosethreads/internal/store"
	"github.com/rjbs/loosethreads/internal/thread"
)

const proj = "github.com/work/thing"

var ctx = context.Background()

// world is two stores ("laptop" and "vm") sharing one bare repository as
// the remote of their "work" collection.
type world struct {
	t      *testing.T
	remote string
	laptop *store.Store
	vm     *store.Store
	clock  time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	w := &world{t: t, remote: remote, clock: time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)}
	w.laptop = w.newStore(filepath.Join(base, "laptop"))
	w.vm = w.newStore(filepath.Join(base, "vm"))
	return w
}

func (w *world) newStore(root string) *store.Store {
	w.t.Helper()
	s, err := store.Open(root)
	if err != nil {
		w.t.Fatal(err)
	}
	s.Config().Collections = map[string]store.CollectionConfig{"work": {Remote: w.remote}}
	s.Config().Routes = []store.Route{{Match: "github.com/work/*", Collection: "work"}}
	if err := s.SaveConfig(); err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *world) add(s *store.Store, title string) *thread.Thread {
	w.t.Helper()
	p, err := s.Project(proj)
	if err != nil {
		w.t.Fatal(err)
	}
	w.clock = w.clock.Add(time.Minute)
	th := &thread.Thread{State: thread.Open, Body: title + "\n"}
	if err := s.Create(p, th, w.clock); err != nil {
		w.t.Fatal(err)
	}
	return th
}

func (w *world) setState(s *store.Store, id string, state thread.State, note string) {
	w.t.Helper()
	p, err := s.LookupProject(proj)
	if err != nil {
		w.t.Fatal(err)
	}
	th, err := s.Get(p, id)
	if err != nil {
		w.t.Fatal(err)
	}
	th.Resolve(state, note, w.clock)
	if err := s.Save(p, th); err != nil {
		w.t.Fatal(err)
	}
}

// sync runs one sync and checks the result's summary against want.
func (w *world) sync(s *store.Store, name, want string) Result {
	w.t.Helper()
	r, err := Collection(ctx, s, "work")
	if err != nil && !errors.Is(err, ErrConflict) {
		w.t.Fatalf("%s: %v", name, err)
	}
	if got := r.String(); !strings.Contains(got, want) {
		w.t.Errorf("%s: result %q does not contain %q", name, got, want)
	}
	return r
}

// titles lists the titles and states in s's copy of the project.
func (w *world) titles(s *store.Store) string {
	w.t.Helper()
	p, err := s.LookupProject(proj)
	if err != nil {
		return "(no project)"
	}
	ths, errs, _ := s.Threads(p)
	var parts []string
	for _, th := range ths {
		parts = append(parts, th.Title()+":"+string(th.State))
	}
	for range errs {
		parts = append(parts, "(unparseable)")
	}
	return strings.Join(parts, " ")
}

func TestRoundTrip(t *testing.T) {
	w := newWorld(t)

	w.sync(w.laptop, "nothing yet", "up to date")

	one := w.add(w.laptop, "One")
	w.sync(w.laptop, "first push", "committed, pushed")
	w.sync(w.laptop, "idempotent", "up to date")

	w.sync(w.vm, "vm pulls", "merged")
	if got := w.titles(w.vm); got != "One:open" {
		t.Fatalf("vm has %q", got)
	}

	w.add(w.vm, "Two")
	w.setState(w.vm, one.ID, thread.Done, "did it on the vm")
	w.sync(w.vm, "vm pushes", "committed, pushed")

	w.sync(w.laptop, "laptop pulls", "merged")
	if got := w.titles(w.laptop); got != "One:done Two:open" {
		t.Errorf("laptop has %q", got)
	}
	w.sync(w.laptop, "laptop up to date", "up to date")
}

func TestConcurrentAddsDoNotConflict(t *testing.T) {
	w := newWorld(t)
	w.add(w.laptop, "Laptop thing")
	w.add(w.vm, "VM thing")
	w.sync(w.laptop, "laptop first", "pushed")
	w.sync(w.vm, "vm merges and pushes", "committed, merged, pushed")
	w.sync(w.laptop, "laptop merges", "merged")
	if got := w.titles(w.laptop); got != "Laptop thing:open VM thing:open" {
		t.Errorf("laptop has %q", got)
	}
}

func TestConflictLeavesMarkersThenCommitsResolution(t *testing.T) {
	w := newWorld(t)
	th := w.add(w.laptop, "Contested")
	w.sync(w.laptop, "seed", "pushed")
	w.sync(w.vm, "vm gets it", "merged")

	w.setState(w.laptop, th.ID, thread.Done, "laptop says done")
	w.setState(w.vm, th.ID, thread.Abandoned, "vm says abandoned")
	w.sync(w.laptop, "laptop pushes first", "pushed")

	r, err := Collection(ctx, w.vm, "work")
	if !errors.Is(err, ErrConflict) || len(r.Conflicts) != 1 {
		t.Fatalf("expected a conflict, got %v / %+v", err, r)
	}
	if !strings.Contains(r.String(), "CONFLICT") {
		t.Errorf("summary %q", r.String())
	}
	p, _ := w.vm.LookupProject(proj)
	path := w.vm.ThreadPath(p, th.ID)
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "<<<<<<<") {
		t.Fatalf("no conflict markers in %s:\n%s", path, data)
	}
	if got := w.titles(w.vm); got != "(unparseable)" {
		t.Errorf("conflicted thread should read as unparseable, got %q", got)
	}

	// Running again without resolving reports the same conflict.
	w.sync(w.vm, "still conflicted", "CONFLICT")

	// The person resolves it by writing a good file; the next sync commits.
	resolved := &thread.Thread{ID: th.ID, State: thread.Done, Created: w.clock, Body: "Contested\n\nDone 2026-09-05: laptop wins\n"}
	if err := w.vm.Save(p, resolved); err != nil {
		t.Fatal(err)
	}
	w.sync(w.vm, "resolution", "committed")
	w.sync(w.laptop, "laptop sees resolution", "merged")
	if got := w.titles(w.laptop); got != "Contested:done" {
		t.Errorf("laptop has %q", got)
	}
}

func TestAddClonesAndAttaches(t *testing.T) {
	w := newWorld(t)
	w.add(w.laptop, "Seed")
	w.sync(w.laptop, "seed", "pushed")

	fresh, err := store.Open(filepath.Join(t.TempDir(), "fresh"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Add(ctx, fresh, "work", w.remote)
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped != "" || len(r.Conflicts) != 0 {
		t.Errorf("add result %+v", r)
	}
	if fresh.Config().Remote("work") != w.remote {
		t.Error("config not written")
	}
	if got := w.titles(fresh); got != "Seed:open" {
		t.Errorf("fresh store has %q", got)
	}

	// Attaching a remote to an existing, non-git collection works too.
	other, _ := store.Open(filepath.Join(t.TempDir(), "other"))
	other.Config().Routes = []store.Route{{Match: "github.com/work/*", Collection: "work"}}
	other.SaveConfig()
	w.add(other, "Local first")
	if _, err := Add(ctx, other, "work", w.remote); err != nil {
		t.Fatal(err)
	}
	if got := w.titles(other); got != "Seed:open Local first:open" && got != "Local first:open Seed:open" {
		t.Errorf("attached store has %q", got)
	}

	if _, err := Add(ctx, other, "../evil", w.remote); err == nil {
		t.Error("bad collection name accepted")
	}
}

func TestAllSkipsCollectionsWithoutRemotes(t *testing.T) {
	w := newWorld(t)
	w.add(w.laptop, "x")
	p, _ := w.laptop.Project("github.com/home/local-only")
	_ = p
	results, err := All(ctx, w.laptop)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Collection != "work" {
		t.Errorf("results %+v", results)
	}
}

func TestUnreachableRemote(t *testing.T) {
	s, _ := store.Open(filepath.Join(t.TempDir(), "s"))
	s.Config().Collections = map[string]store.CollectionConfig{"work": {Remote: filepath.Join(t.TempDir(), "nowhere.git")}}
	s.SaveConfig()
	if err := os.MkdirAll(s.CollectionDir("work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Collection(ctx, s, "work"); err == nil {
		t.Error("expected an error for an unreachable remote")
	}
}
