package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func checkNormalize(t *testing.T, raw, want string) {
	t.Helper()
	if got := NormalizeRemote(raw); got != want {
		t.Errorf("NormalizeRemote(%q) = %q, want %q", raw, got, want)
	}
}

func TestIsPath(t *testing.T) {
	for id, want := range map[string]bool{
		"github.com/rjbs/foo":       false,
		"/Users/rjbs/code/foo":      true,
		"/srv/git/foo":              true, // a local-path remote is a path too
		"gitbox.example.com/g/repo": false,
	} {
		if got := IsPath(id); got != want {
			t.Errorf("IsPath(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestNormalizeRemote(t *testing.T) {
	checkNormalize(t, "git@github.com:rjbs/foo.git", "github.com/rjbs/foo")
	checkNormalize(t, "https://github.com/rjbs/foo.git", "github.com/rjbs/foo")
	checkNormalize(t, "https://github.com/rjbs/foo", "github.com/rjbs/foo")
	checkNormalize(t, "https://user@GitHub.com:443/rjbs/foo/", "github.com/rjbs/foo")
	checkNormalize(t, "ssh://git@gitbox.example.com:2222/repos/Foo.git", "gitbox.example.com/repos/Foo")
	checkNormalize(t, "git://host/path", "host/path")
	checkNormalize(t, "host:path/to/repo.git", "host/path/to/repo")
	checkNormalize(t, "file:///srv/git/foo.git", "/srv/git/foo")
	checkNormalize(t, "/srv/git/foo.git", "/srv/git/foo")
	checkNormalize(t, "../relative/repo", "../relative/repo")
	checkNormalize(t, "  git@github.com:rjbs/foo.git\n", "github.com/rjbs/foo")
}

// makeRepo creates a git repository with the given remotes (name -> url)
// and returns its path.
func makeRepo(t *testing.T, remotes map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	for name, url := range remotes {
		run(t, dir, "remote", "add", name, url)
	}
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func checkIdentify(t *testing.T, name, dir string, want Identity) {
	t.Helper()
	got, err := Identify(dir)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if got != want {
		t.Errorf("%s: got %+v, want %+v", name, got, want)
	}
}

func TestIdentify(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	all := makeRepo(t, map[string]string{
		"origin": "git@example.com:o/repo.git",
		"gitbox": "git@gitbox.example.com:g/repo.git",
		"github": "git@github.com:rjbs/repo.git",
	})
	checkIdentify(t, "github wins", all, Identity{"github.com/rjbs/repo", "remote:github"})

	sub := filepath.Join(all, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	checkIdentify(t, "subdirectory finds repo", sub, Identity{"github.com/rjbs/repo", "remote:github"})

	gb := makeRepo(t, map[string]string{
		"origin": "git@example.com:o/repo.git",
		"gitbox": "git@gitbox.example.com:g/repo.git",
	})
	checkIdentify(t, "gitbox beats origin", gb, Identity{"gitbox.example.com/g/repo", "remote:gitbox"})

	origin := makeRepo(t, map[string]string{"origin": "https://example.com/o/repo"})
	checkIdentify(t, "origin", origin, Identity{"example.com/o/repo", "remote:origin"})

	other := makeRepo(t, map[string]string{"upstream": "https://example.com/u/repo"})
	checkIdentify(t, "unlisted remote falls back to root", other, Identity{realpath(t, other), "gitroot"})

	bare := makeRepo(t, nil)
	checkIdentify(t, "no remotes", bare, Identity{realpath(t, bare), "gitroot"})

	plain := t.TempDir()
	checkIdentify(t, "not a repo", plain, Identity{realpath(t, plain), "path"})
}

// realpath resolves symlinks the way git reports the toplevel, since
// t.TempDir() on macOS lives under a symlinked /var.
func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
