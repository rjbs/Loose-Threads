package store

import (
	"database/sql"
	"regexp"
	"testing"

	"github.com/rjbs/loosethreads/internal/patch"
)

func checkDefaultName(t *testing.T, id, want string) {
	t.Helper()
	if got := DefaultName(id); got != want {
		t.Errorf("DefaultName(%q) = %q, want %q", id, got, want)
	}
}

func TestDefaultName(t *testing.T) {
	checkDefaultName(t, "github.com/rjbs/Loose-Threads", "rjbs/Loose-Threads")
	checkDefaultName(t, "gitlab.fm/fastmail/hm", "fastmail/hm")
	checkDefaultName(t, "gitlab.fm/group/sub/repo", "group/sub/repo")
	checkDefaultName(t, "/Users/rjbs/code/slop/Jep", "Jep")
	checkDefaultName(t, "plain", "plain")
	checkDefaultName(t, "no-host/thing", "no-host/thing")
}

func TestDefaultColor(t *testing.T) {
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	seen := map[string]bool{}
	for _, id := range []string{"github.com/rjbs/a", "github.com/rjbs/b", "github.com/rjbs/c", "github.com/rjbs/d", "github.com/rjbs/e"} {
		c := DefaultColor(id)
		if !hex.MatchString(c) {
			t.Errorf("DefaultColor(%q) = %q, not hex RGB", id, c)
		}
		if DefaultColor(id) != c {
			t.Errorf("DefaultColor(%q) is not stable", id)
		}
		seen[c] = true
	}
	if len(seen) < 2 {
		t.Errorf("five ids all got one color: %v", seen)
	}
}

func checkProject(t *testing.T, what string, s *Store, id, name, color string) {
	t.Helper()
	p, err := s.LookupProject(id)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if p.DisplayName() != name || p.Color != color {
		t.Errorf("%s: %s is named %q in %q, want %q in %q", what, id, p.DisplayName(), p.Color, name, color)
	}
}

func TestNewProjectNameAndColor(t *testing.T) {
	s := newStore(t)
	addThread(t, s, "github.com/rjbs/thing", "x")
	checkProject(t, "new", s, "github.com/rjbs/thing", "rjbs/thing", DefaultColor("github.com/rjbs/thing"))

	if err := s.RenameProject("github.com/rjbs/thing", "The Thing"); err != nil {
		t.Fatal(err)
	}
	checkProject(t, "renamed", s, "github.com/rjbs/thing", "The Thing", DefaultColor("github.com/rjbs/thing"))

	if _, err := s.MoveProject("github.com/rjbs/thing", "work"); err != nil {
		t.Fatal(err)
	}
	checkProject(t, "moved", s, "github.com/rjbs/thing", "The Thing", DefaultColor("github.com/rjbs/thing"))
}

func TestSyncRename(t *testing.T) {
	srv := newServer(t)
	a, b := machine(t, srv), machine(t, srv)
	th := addThread(t, a, "github.com/rjbs/p", "x\n")
	syncAll(t, a, b)

	if err := a.RenameProject("github.com/rjbs/p", "Mine"); err != nil {
		t.Fatal(err)
	}
	syncAll(t, a, b)
	checkProject(t, "renamed here", b, "github.com/rjbs/p", "Mine", DefaultColor("github.com/rjbs/p"))

	// Renamed and recolored in another client: lt shows it, and later
	// writes here do not put the old values back.
	list := srv.Objects("personal", "TaskList")[0]
	srv.Update("personal", "TaskList", list["id"].(string), patch.Patch{"name": "Theirs", "color": "teal"})
	syncAll(t, a, b)
	appendTo(t, a, "github.com/rjbs/p", th.ID, "a note")
	syncAll(t, a, b)
	checkProject(t, "renamed elsewhere", a, "github.com/rjbs/p", "Theirs", "teal")
	if got := srv.Objects("personal", "TaskList")[0]; got["name"] != "Theirs" || got["color"] != "teal" {
		t.Errorf("server list changed back: %v", got)
	}
}

// oldList makes a project's list as lt made them before it picked names
// and colors: with the given name, and with color only if not empty.
func oldList(t *testing.T, s *Store, id, name, color string) {
	t.Helper()
	v := patch.Object{"name": name, propProject: id}
	if color != "" {
		v["color"] = color
	}
	err := s.tx(func(tx *sql.Tx) error {
		return setLocal(tx, &row{collection: "personal", typ: typeTaskList}, v)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkDefaults(t *testing.T, what string, s *Store, dryRun bool, want ...DefaultsChange) {
	t.Helper()
	got, err := s.ApplyProjectDefaults(dryRun)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Errorf("%s: changes %+v, want %+v", what, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: change %+v, want %+v", what, got[i], want[i])
		}
	}
}

func TestApplyProjectDefaults(t *testing.T) {
	s := newStore(t)
	oldList(t, s, "github.com/rjbs/plain", "github.com/rjbs/plain", "")
	oldList(t, s, "github.com/rjbs/named", "Named", "")
	oldList(t, s, "github.com/rjbs/chosen", "Chosen", "red")

	plain := DefaultsChange{"github.com/rjbs/plain", "rjbs/plain", DefaultColor("github.com/rjbs/plain")}
	named := DefaultsChange{"github.com/rjbs/named", "", DefaultColor("github.com/rjbs/named")}
	checkDefaults(t, "dry run", s, true, named, plain)
	checkDefaults(t, "dry run changed nothing", s, true, named, plain)
	checkDefaults(t, "applied", s, false, named, plain)
	checkDefaults(t, "again", s, false)

	checkProject(t, "plain", s, "github.com/rjbs/plain", "rjbs/plain", DefaultColor("github.com/rjbs/plain"))
	checkProject(t, "named", s, "github.com/rjbs/named", "Named", DefaultColor("github.com/rjbs/named"))
	checkProject(t, "chosen", s, "github.com/rjbs/chosen", "Chosen", "red")
}
