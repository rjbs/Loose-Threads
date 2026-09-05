// Package sync moves collections between machines with git.  Each
// collection with a remote is a clone; syncing commits everything, fetches,
// merges, and pushes.  Conflicts are left as git's markers for a person to
// resolve; the next sync commits the resolution.
package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rjbs/loosethreads/internal/store"
)

// Result describes what one collection's sync did.
type Result struct {
	Collection string
	Skipped    string   // why nothing was done, if so ("no remote", "locked")
	Committed  bool     // local changes were committed
	Merged     bool     // remote changes were merged in
	Pushed     bool     // local commits were pushed
	Conflicts  []string // files left with conflict markers
}

// String summarizes a Result for a person.
func (r Result) String() string {
	switch {
	case r.Skipped != "":
		return fmt.Sprintf("%s: skipped (%s)", r.Collection, r.Skipped)
	case len(r.Conflicts) > 0:
		return fmt.Sprintf("%s: CONFLICT in %s; resolve, then run lt sync again", r.Collection, strings.Join(r.Conflicts, ", "))
	}
	var did []string
	if r.Committed {
		did = append(did, "committed")
	}
	if r.Merged {
		did = append(did, "merged")
	}
	if r.Pushed {
		did = append(did, "pushed")
	}
	if len(did) == 0 {
		return r.Collection + ": up to date"
	}
	return r.Collection + ": " + strings.Join(did, ", ")
}

// ErrConflict is returned alongside a Result whose Conflicts is non-empty.
var ErrConflict = errors.New("sync: merge conflict")

// All syncs every collection that has a remote.  It returns one Result
// per such collection and the first error, but keeps going past errors
// so one unreachable remote does not stop the others.
func All(ctx context.Context, s *store.Store) ([]Result, error) {
	names, err := s.Collections()
	if err != nil {
		return nil, err
	}
	var results []Result
	var first error
	for _, name := range names {
		if s.Config().Remote(name) == "" {
			continue
		}
		r, err := Collection(ctx, s, name)
		results = append(results, r)
		if err != nil && first == nil {
			first = fmt.Errorf("%s: %w", name, err)
		}
	}
	return results, first
}

// Collection syncs one collection.  A collection without a remote is
// skipped; one being synced by another process is skipped too.
func Collection(ctx context.Context, s *store.Store, name string) (Result, error) {
	r := Result{Collection: name}
	remote := s.Config().Remote(name)
	if remote == "" {
		r.Skipped = "no remote"
		return r, nil
	}
	dir := s.CollectionDir(name)

	cloned, err := ensureRepo(ctx, dir, remote)
	if err != nil {
		return r, err
	}
	g := git{ctx: ctx, dir: dir}
	g.identity = identityFlags(g, s.Config().AuthorFor(name))
	r.Merged = cloned && g.hasCommits() // a fresh clone of a non-empty remote is the first merge

	unlock, err := lock(dir)
	if err != nil {
		r.Skipped = "locked"
		return r, nil
	}
	defer unlock()

	// A merge left over from an earlier conflict: if every file git still
	// calls unmerged has had its markers removed, the person has resolved
	// it, so stage and commit; otherwise report what is still unresolved.
	if g.exists(".git/MERGE_HEAD") {
		var unresolved []string
		for _, f := range g.conflicts() {
			if hasMarkers(filepath.Join(dir, f)) {
				unresolved = append(unresolved, f)
			}
		}
		if len(unresolved) > 0 {
			r.Conflicts = unresolved
			return r, ErrConflict
		}
		if _, err := g.run("add", "-A"); err != nil {
			return r, err
		}
		if _, err := g.run("commit", "-q", "--no-edit"); err != nil {
			return r, err
		}
		r.Committed = true
	}

	if _, err := g.run("add", "-A"); err != nil {
		return r, err
	}
	if _, err := g.run("diff", "--cached", "--quiet"); err != nil {
		if _, err := g.run("commit", "-q", "-m", "lt sync from "+hostname()); err != nil {
			return r, err
		}
		r.Committed = true
	}

	if _, err := g.run("fetch", "-q", "origin"); err != nil {
		return r, fmt.Errorf("fetch: %w", err)
	}

	branch := g.remoteBranch()
	local := g.localBranch()
	switch {
	case branch == "":
		// Empty remote: nothing to merge; push whatever we have.
		branch = local
	case !g.hasCommits():
		if _, err := g.run("checkout", "-q", "-B", local, "origin/"+branch); err != nil {
			return r, err
		}
		r.Merged = true
	default:
		before := g.head()
		out, err := g.run("merge", "-q", "--no-edit", "--allow-unrelated-histories", "origin/"+branch)
		if err != nil {
			if conflicts := g.conflicts(); len(conflicts) > 0 {
				r.Conflicts = conflicts
				return r, ErrConflict
			}
			return r, fmt.Errorf("merge: %w\n%s", err, out)
		}
		r.Merged = r.Merged || g.head() != before
	}

	if !g.hasCommits() {
		return r, nil
	}
	// rev-list fails when the remote branch does not exist yet (empty
	// remote); treating that as "ahead" is right, since anything local
	// needs pushing.
	ahead, err := g.run("rev-list", "--count", "origin/"+branch+"..HEAD")
	if err != nil || strings.TrimSpace(ahead) != "0" {
		if _, err := g.run("push", "-q", "-u", "origin", "HEAD:"+branch); err != nil {
			return r, fmt.Errorf("push: %w", err)
		}
		r.Pushed = true
	}
	return r, nil
}

// Add records a collection with a remote in the config and clones it into
// place, or attaches the remote to a collection directory that already
// exists.  It then syncs.
func Add(ctx context.Context, s *store.Store, name, remote string) (Result, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return Result{}, fmt.Errorf("sync: %q is not a usable collection name", name)
	}
	cfg := s.Config()
	if cfg.Collections == nil {
		cfg.Collections = map[string]store.CollectionConfig{}
	}
	cfg.Collections[name] = store.CollectionConfig{Remote: remote}
	if err := s.SaveConfig(); err != nil {
		return Result{}, err
	}
	return Collection(ctx, s, name)
}

// ensureRepo makes dir a git repository whose origin is remote: cloning
// if the directory does not exist (reported as cloned), initializing if
// it exists without .git, and correcting origin's url if it has changed.
func ensureRepo(ctx context.Context, dir, remote string) (cloned bool, err error) {
	g := git{ctx: ctx, dir: dir}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return false, err
		}
		out, err := exec.CommandContext(ctx, "git", "clone", "-q", remote, dir).CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("clone: %w\n%s", err, out)
		}
		return true, nil
	}
	if !g.exists(".git") {
		if _, err := g.run("init", "-q"); err != nil {
			return false, err
		}
		_, err := g.run("remote", "add", "origin", remote)
		return false, err
	}
	if cur, err := g.run("remote", "get-url", "origin"); err != nil {
		_, err := g.run("remote", "add", "origin", remote)
		return false, err
	} else if strings.TrimSpace(cur) != remote {
		_, err := g.run("remote", "set-url", "origin", remote)
		return false, err
	}
	return false, nil
}

// hasMarkers reports whether a file still contains git conflict markers.
func hasMarkers(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") {
			return true
		}
	}
	return false
}

// lock takes an exclusive, non-blocking lock on the repository so two
// syncs (say, the browser's and one triggered by lt add) do not race.
func lock(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".git", "lt-sync.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown host"
	}
	return h
}

// git runs git commands in one repository.
type git struct {
	ctx      context.Context
	dir      string
	identity []string // -c flags fixing the commit identity, if any
}

// identityFlags chooses who sync commits are by.  A configured Author
// wins.  Otherwise git's own configuration is left alone if it has an
// identity, and a fixed one is supplied only when it has none, so a
// fresh VM without git config still works.
func identityFlags(g git, a *store.Author) []string {
	if a == nil {
		if _, err := g.run("config", "user.email"); err == nil {
			if _, err := g.run("config", "user.name"); err == nil {
				return nil
			}
		}
		a = &store.Author{Name: "Loose Threads", Email: "lt@" + hostname()}
	}
	return []string{"-c", "user.name=" + a.Name, "-c", "user.email=" + a.Email}
}

func (g git) run(args ...string) (string, error) {
	full := append([]string{"-C", g.dir, "-c", "commit.gpgsign=false"}, g.identity...)
	full = append(full, args...)
	cmd := exec.CommandContext(g.ctx, "git", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (g git) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(g.dir, rel))
	return err == nil
}

func (g git) hasCommits() bool {
	_, err := g.run("rev-parse", "-q", "--verify", "HEAD")
	return err == nil
}

func (g git) head() string {
	out, _ := g.run("rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

// localBranch is the branch HEAD is on, even if unborn.
func (g git) localBranch() string {
	out, err := g.run("symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || strings.TrimSpace(out) == "" {
		return "main"
	}
	return strings.TrimSpace(out)
}

// remoteBranch picks the remote branch to merge from: origin's HEAD if it
// advertises one, else main or master, else the only branch there is.
// "" means the remote has no branches yet.
func (g git) remoteBranch() string {
	if out, err := g.run("symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	}
	out, err := g.run("for-each-ref", "--format=%(refname:short)", "refs/remotes/origin/")
	if err != nil {
		return ""
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if b := strings.TrimPrefix(line, "origin/"); b != "" && b != "HEAD" {
			branches = append(branches, b)
		}
	}
	for _, want := range []string{"main", "master"} {
		for _, b := range branches {
			if b == want {
				return b
			}
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return ""
}

// conflicts lists files with unresolved merge conflicts.
func (g git) conflicts() []string {
	out, err := g.run("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}
