package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rjbs/loosethreads/internal/store"
	"github.com/rjbs/loosethreads/internal/thread"
)

var now = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// fixture describes one thread to seed: project id, title, and state.
type fixture struct {
	project string
	title   string
	state   thread.State
}

// newModel seeds a store with the fixtures, starting on the project with
// id start, and sizes the model so the detail pane is visible.
func newModel(t *testing.T, start string, fixtures ...fixture) (*Model, *store.Store) {
	t.Helper()
	s := &store.Store{Root: filepath.Join(t.TempDir(), "store")}
	tick := now
	for _, f := range fixtures {
		p, err := s.Project(f.project)
		if err != nil {
			t.Fatal(err)
		}
		th := &thread.Thread{State: thread.Open, Body: f.title + "\n\nbody of " + f.title + "\n"}
		if err := s.Create(p, th, tick); err != nil {
			t.Fatal(err)
		}
		if f.state != thread.Open {
			th.SetState(f.state, tick)
			if err := s.Save(p, th); err != nil {
				t.Fatal(err)
			}
		}
		tick = tick.Add(24 * time.Hour) // distinct ids that sort in fixture order
	}
	m, err := New(s, start)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return now }
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m, s
}

// press sends each key: single runes as rune keys, longer names as the
// matching special key.
func press(m *Model, keys ...string) tea.Cmd {
	var last tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "ctrl+r":
			msg = tea.KeyMsg{Type: tea.KeyCtrlR}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, last = m.Update(msg)
	}
	return last
}

func typeText(m *Model, s string) {
	for _, r := range s {
		press(m, string(r))
	}
}

func checkView(t *testing.T, name string, m *Model, wantAll []string, wantNone []string) {
	t.Helper()
	v := m.View()
	for _, w := range wantAll {
		if !strings.Contains(v, w) {
			t.Errorf("%s: view lacks %q:\n%s", name, w, v)
		}
	}
	for _, w := range wantNone {
		if strings.Contains(v, w) {
			t.Errorf("%s: view should not contain %q:\n%s", name, w, v)
		}
	}
}

func checkState(t *testing.T, name string, s *store.Store, project, title string, want thread.State) {
	t.Helper()
	p, err := s.LookupProject(project)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	ths, _, _ := s.Threads(p)
	for _, th := range ths {
		if th.Title() == title {
			if th.State != want {
				t.Errorf("%s: %q is %s, want %s", name, title, th.State, want)
			}
			return
		}
	}
	t.Errorf("%s: no thread titled %q in %s", name, title, project)
}

const (
	projA = "github.com/rjbs/alpha"
	projB = "github.com/rjbs/beta"
)

func TestListAndDetail(t *testing.T) {
	m, _ := newModel(t, projA,
		fixture{projA, "Alpha one", thread.Open},
		fixture{projA, "Alpha two", thread.Open},
		fixture{projA, "Alpha closed", thread.Done},
		fixture{projB, "Beta one", thread.Open},
	)
	checkView(t, "initial", m,
		[]string{projA, "Alpha one", "Alpha two", "body of Alpha one", "state:   open"},
		[]string{"Alpha closed", "Beta one"})

	press(m, "j")
	checkView(t, "moved down", m, []string{"body of Alpha two"}, []string{"body of Alpha one"})

	press(m, "c")
	checkView(t, "show closed", m, []string{"Alpha closed", "(showing closed)"}, nil)
	press(m, "c")
	checkView(t, "hide closed", m, nil, []string{"Alpha closed"})
}

func TestStateToggles(t *testing.T) {
	m, s := newModel(t, projA,
		fixture{projA, "One", thread.Open},
		fixture{projA, "Two", thread.Open},
	)
	press(m, "d")
	checkState(t, "done", s, projA, "One", thread.Done)
	checkView(t, "done hidden, status shown", m, []string{": done", "Two"}, []string{"body of One"})

	press(m, "c", "k", "d") // show closed, move to One, toggle back
	checkState(t, "reopened", s, projA, "One", thread.Open)

	press(m, "x")
	checkState(t, "abandoned", s, projA, "One", thread.Abandoned)
	press(m, "x")
	checkState(t, "unabandoned", s, projA, "One", thread.Open)
}

func TestCloseWithNote(t *testing.T) {
	m, s := newModel(t, projA,
		fixture{projA, "Decide thing", thread.Open},
		fixture{projA, "Other", thread.Open},
	)
	press(m, "D")
	checkView(t, "prompt", m, []string{"done, because:"}, nil)
	typeText(m, "chose option B")
	press(m, "enter")
	checkState(t, "done", s, projA, "Decide thing", thread.Done)
	checkView(t, "hidden after own close", m, []string{"Other"}, []string{"Decide thing"})

	p, _ := s.LookupProject(projA)
	ths, _, _ := s.Threads(p)
	if !strings.HasSuffix(ths[0].Body, "Done 2026-09-04: chose option B\n") {
		t.Errorf("note not appended: %q", ths[0].Body)
	}

	press(m, "X")
	checkView(t, "abandon prompt", m, []string{"abandon, because:"}, nil)
	press(m, "esc")
	checkState(t, "esc cancels", s, projA, "Other", thread.Open)

	press(m, "c", "k") // show closed, move up to the done one
	press(m, "D")
	checkView(t, "reopen prompt on a done thread", m, []string{"reopen, because:"}, nil)
	typeText(m, "came back")
	press(m, "enter")
	checkState(t, "reopened", s, projA, "Decide thing", thread.Open)
}

func TestAdd(t *testing.T) {
	m, s := newModel(t, projA, fixture{projA, "Existing", thread.Open})

	press(m, "a")
	checkView(t, "prompt", m, []string{"title:"}, nil)
	typeText(m, "Brand new: item")
	press(m, "enter")
	checkState(t, "created", s, projA, "Brand new: item", thread.Open)
	checkView(t, "selected after add", m, []string{"added ", "origin:  human"}, nil)
	if got := m.selected().Title(); got != "Brand new: item" {
		t.Errorf("selected %q after add", got)
	}
	if m.selected().Origin != thread.OriginHuman {
		t.Errorf("origin %q, want human", m.selected().Origin)
	}

	press(m, "a")
	typeText(m, "   ")
	press(m, "enter")
	if m.mode != modeAdd {
		t.Error("blank title should not leave add mode")
	}
	press(m, "esc")
	if m.mode != modeThreads {
		t.Error("esc should cancel add")
	}
}

func TestAddCreatesProjectDirectory(t *testing.T) {
	m, s := newModel(t, "github.com/rjbs/fresh", fixture{projA, "Elsewhere", thread.Open})
	checkView(t, "virtual project shown", m, []string{"github.com/rjbs/fresh"}, []string{"Elsewhere"})
	if _, err := s.LookupProject("github.com/rjbs/fresh"); err == nil {
		t.Fatal("project directory should not exist before add")
	}
	press(m, "a")
	typeText(m, "First")
	press(m, "enter")
	checkState(t, "created in fresh project", s, "github.com/rjbs/fresh", "First", thread.Open)
}

func TestProjectSwitching(t *testing.T) {
	m, _ := newModel(t, projA,
		fixture{projA, "Alpha one", thread.Open},
		fixture{projB, "Beta one", thread.Open},
		fixture{projB, "Beta two", thread.Done},
	)
	press(m, "]")
	checkView(t, "next project", m, []string{projB, "Beta one"}, []string{"Alpha one", "Beta two"})
	press(m, "]")
	checkView(t, "wraps", m, []string{projA}, nil)
	press(m, "[")
	checkView(t, "previous", m, []string{projB}, nil)

	press(m, "p")
	checkView(t, "picker", m, []string{"projects", "1    " + projA, "1    " + projB}, nil)
	press(m, "k", "enter")
	checkView(t, "picked", m, []string{projA, "Alpha one"}, nil)
}

func TestMismatchedProjectWarning(t *testing.T) {
	m, s := newModel(t, projA, fixture{projA, "One", thread.Open})
	checkView(t, "no warning normally", m, nil, []string{"warning:"})

	p, _ := s.LookupProject(projA)
	os.WriteFile(filepath.Join(p.Dir, "project.yaml"), []byte("id: /old/path\n"), 0o644)
	m2, err := New(s, projA)
	if err != nil {
		t.Fatal(err)
	}
	m2.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	checkView(t, "warning shown", m2, []string{warningGlyph + " project.yaml id does not match directory", "One"}, nil)
	if len(m2.projects) != 1 {
		t.Errorf("mismatched directory listed %d times, want 1", len(m2.projects))
	}
	press(m2, "p")
	checkView(t, "picker has one entry, no warning", m2, []string{"1    /old/path"}, []string{"warning:", "1    " + projA})
}

func TestPathIdentityWarning(t *testing.T) {
	m, _ := newModel(t, "/Users/someone/code/thing", fixture{"/Users/someone/code/thing", "One", thread.Open})
	checkView(t, "path warning", m, []string{warningGlyph + " project identified by path alone", "One"}, nil)
	if got, want := m.list.Height(), m.bodyHeight()-3; got != want {
		t.Errorf("list height %d with a one-line warning box, want %d", got, want)
	}
	press(m, "p")
	checkView(t, "not in picker", m, nil, []string{"path alone"})
	press(m, "esc")

	m2, _ := newModel(t, projA, fixture{projA, "One", thread.Open})
	checkView(t, "remote identity has no warning", m2, nil, []string{"path alone", warningGlyph})
	if got, want := m2.list.Height(), m2.bodyHeight(); got != want {
		t.Errorf("list height %d without warnings, want %d", got, want)
	}
}

func TestHelpAndQuit(t *testing.T) {
	m, _ := newModel(t, projA, fixture{projA, "One", thread.Open})
	press(m, "?")
	checkView(t, "help", m, []string{"Press any key to return"}, nil)
	press(m, "x")
	checkView(t, "back", m, []string{"One"}, []string{"Press any key"})

	cmd := press(m, "q")
	if cmd == nil {
		t.Fatal("q should produce a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should quit")
	}
}

func TestEmptyStore(t *testing.T) {
	m, _ := newModel(t, "")
	checkView(t, "empty", m, []string{"no projects"}, nil)
	press(m, "d", "x", "e", "]", "a")
	if m.err == nil {
		t.Error("add with no project should set an error")
	}
}

// External changes: another process writing to the store.

func externalAdd(t *testing.T, s *store.Store, project, title string, at time.Time) *thread.Thread {
	t.Helper()
	p, err := s.Project(project)
	if err != nil {
		t.Fatal(err)
	}
	th := &thread.Thread{State: thread.Open, Body: title + "\n"}
	if err := s.Create(p, th, at); err != nil {
		t.Fatal(err)
	}
	return th
}

func externalSetState(t *testing.T, s *store.Store, project string, th *thread.Thread, state thread.State) {
	t.Helper()
	p, err := s.LookupProject(project)
	if err != nil {
		t.Fatal(err)
	}
	th.SetState(state, now)
	if err := s.Save(p, th); err != nil {
		t.Fatal(err)
	}
}

func tickOnce(m *Model) { m.Update(tickMsg{}) }

func TestLiveUpdates(t *testing.T) {
	m, s := newModel(t, projA, fixture{projA, "Original", thread.Open})
	checkView(t, "count", m, []string{"1 open"}, nil)

	added := externalAdd(t, s, projA, "Added elsewhere", now.Add(48*time.Hour))
	checkView(t, "not yet polled", m, nil, []string{"Added elsewhere"})
	tickOnce(m)
	checkView(t, "appears after poll", m, []string{"Added elsewhere", "2 open"}, nil)

	externalSetState(t, s, projA, added, thread.Done)
	tickOnce(m)
	checkView(t, "externally closed lingers, struck", m, []string{"✓ Added elsewhere", "1 open"}, nil)

	tickOnce(m)
	checkView(t, "still lingers on a quiet poll", m, []string{"✓ Added elsewhere"}, nil)

	press(m, "ctrl+r")
	checkView(t, "hard refresh hides it", m, []string{"Original"}, []string{"Added elsewhere"})

	// Created and closed between polls: never shown.
	ghost := externalAdd(t, s, projA, "Ghost", now.Add(72*time.Hour))
	externalSetState(t, s, projA, ghost, thread.Abandoned)
	tickOnce(m)
	checkView(t, "never-seen closed thread hidden", m, nil, []string{"Ghost"})

	// Deleted on disk: vanishes.
	p, _ := s.LookupProject(projA)
	os.Remove(s.ThreadPath(p, added.ID))
	press(m, "c")
	checkView(t, "show closed before delete check", m, []string{"Ghost"}, []string{"Added elsewhere"})
}

func TestLocalCloseHidesImmediately(t *testing.T) {
	m, s := newModel(t, projA,
		fixture{projA, "One", thread.Open},
		fixture{projA, "Two", thread.Open},
	)
	press(m, "d")
	checkView(t, "own close hides", m, []string{"Two", "1 open"}, []string{"One"})

	// Someone else closes Two while we watch: it lingers.
	p, _ := s.LookupProject(projA)
	ths, _, _ := s.Threads(p)
	for _, th := range ths {
		if th.Title() == "Two" {
			externalSetState(t, s, projA, th, thread.Done)
		}
	}
	tickOnce(m)
	checkView(t, "other's close lingers", m, []string{"✓ Two", "0 open"}, nil)
}

func TestProjectSwitchResetsSticky(t *testing.T) {
	m, s := newModel(t, projA,
		fixture{projA, "Alpha one", thread.Open},
		fixture{projB, "Beta one", thread.Open},
	)
	p, _ := s.LookupProject(projA)
	ths, _, _ := s.Threads(p)
	externalSetState(t, s, projA, ths[0], thread.Done)
	tickOnce(m)
	checkView(t, "lingers", m, []string{"✓ Alpha one"}, nil)

	press(m, "]", "[")
	checkView(t, "gone after leaving and returning", m, []string{projA}, []string{"Alpha one"})
}
