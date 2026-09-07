package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var ltBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lt-test")
	if err != nil {
		panic(err)
	}
	ltBin = filepath.Join(dir, "lt")
	build := exec.Command("go", "build", "-o", ltBin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		panic("building lt: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// world is one test's isolated store and working directory.
type world struct {
	t     *testing.T
	store string
	cwd   string
	env   []string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	base := t.TempDir()
	w := &world{t: t, store: filepath.Join(base, "store"), cwd: filepath.Join(base, "repo")}
	if err := os.MkdirAll(w.cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "git@github.com:rjbs/testrepo.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", w.cwd}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	w.env = []string{"LOOSETHREADS_HOME=" + w.store, "PATH=" + os.Getenv("PATH"), "HOME=" + base, "LOOSETHREADS_NO_PUSH=1"}
	return w
}

// run executes lt with args and returns stdout, stderr, and the exit code.
// stdin is the given string, or /dev/null when empty.
func (w *world) run(stdin string, extraEnv []string, args ...string) (string, string, int) {
	w.t.Helper()
	cmd := exec.Command(ltBin, args...)
	cmd.Dir = w.cwd
	cmd.Env = append(append([]string{}, w.env...), extraEnv...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		w.t.Fatalf("lt %v: %v", args, err)
	}
	return out.String(), errb.String(), code
}

// check runs lt and asserts on exit code and a regexp over stdout (or
// stderr when the code is nonzero).  It returns stdout for chaining.
func (w *world) check(name string, wantCode int, wantMatch string, args ...string) string {
	w.t.Helper()
	return w.checkIn("", nil, name, wantCode, wantMatch, args...)
}

func (w *world) checkIn(stdin string, env []string, name string, wantCode int, wantMatch string, args ...string) string {
	w.t.Helper()
	out, errOut, code := w.run(stdin, env, args...)
	if code != wantCode {
		w.t.Errorf("%s: exit %d, want %d\nstdout: %s\nstderr: %s", name, code, wantCode, out, errOut)
		return out
	}
	subject := out
	if code != 0 {
		subject = errOut
	}
	if !regexp.MustCompile(wantMatch).MatchString(subject) {
		w.t.Errorf("%s: output does not match %q:\n%s", name, wantMatch, subject)
	}
	return out
}

const idPat = `2\d\d\d-\d\d-\d\d-[a-z2-9]{6}`

func TestCLI(t *testing.T) {
	w := newWorld(t)

	w.check("project-id", 0, `^github\.com/rjbs/testrepo\n$`, "project-id")
	w.check("no args", 2, `usage:`)
	w.check("unknown command", 2, `unknown command`, "frobnicate")
	w.check("list before any threads", 0, `^$`, "list")

	out, errOut, code := w.run("", nil, "add", "First: with colon", "-body", "Some context.")
	if code != 0 || !strings.Contains(errOut, "new project github.com/rjbs/testrepo in collection local") {
		t.Errorf("first add should report the collection: exit %d, stderr %q", code, errOut)
	}
	a := strings.TrimSpace(out)
	b := strings.TrimSpace(w.checkIn("stdin body", []string{"CLAUDE_CODE_SESSION_ID=sess-1"},
		"add with stdin", 0, `^`+idPat+`\n$`, "add", "Second"))
	w.check("add without title", 1, `TITLE`, "add")
	w.check("add blank title", 1, `blank`, "add", "  ")

	w.check("list", 0, `(?s)First: with colon.*Second|Second.*First: with colon`, "list")
	w.check("list marks session", 0, `(?m)^\* `+regexp.QuoteMeta(b)+`  Second$`, "list", "-session", "sess-1")
	w.check("list session scope", 0, `^\* `+regexp.QuoteMeta(b)+`  Second\n$`, "list", "-scope", "session", "-session", "sess-1")
	w.check("list session scope needs id", 1, `requires a session id`, "list", "-scope", "session")
	w.check("list bad scope", 1, `unknown scope`, "list", "-scope", "galaxy")
	w.check("list rejects args", 1, `takes no arguments`, "list", "x")

	w.check("show full id", 0, `(?s)^---\nstate: open\n.*origin: human\n---\nFirst: with colon\n\nSome context\.\n$`, "show", a)
	w.check("show by suffix", 0, `First: with colon`, "show", a[len(a)-4:])
	w.check("show json", 0, `"session": "sess-1"`, "show", "-json", b)
	w.check("show missing", 1, `no thread matches`, "show", "zzzz")

	w.check("done", 0, `^`+regexp.QuoteMeta(a)+`: done  First: with colon\n$`, "done", a)
	w.check("list hides done", 0, `^  `+regexp.QuoteMeta(b)+`  Second\n$`, "list")
	w.check("list all-states", 0, `\[done\]  First`, "list", "-all-states")
	w.check("reopen", 0, `: open  First`, "reopen", a)
	w.check("done with note", 0, `: done  First`, "done", a, "-note", "shipped in abc123")
	w.check("note appended", 0, `(?s)Some context\.\n\nDone \d{4}-\d\d-\d\d: shipped in abc123\n$`, "show", a)
	w.check("reopen with note", 0, `: open`, "reopen", a, "-note", "regressed")
	w.check("second note appended", 0, `(?s)shipped in abc123\n\nReopened \d{4}-\d\d-\d\d: regressed\n$`, "show", a)
	w.check("abandon two", 0, `(?s): abandoned  First.*: abandoned  Second`, "abandon", a, b)
	w.check("list json empty array", 0, `^\[\]\n$`, "list", "-json")

	w.check("add elsewhere", 0, idPat, "add", "-project", "example.com/other", "Elsewhere")
	w.check("list all", 0, `(?s)^example\.com/other\n  `+idPat+`  Elsewhere\n$`, "list", "-scope", "all")
	w.check("list all with states", 0, `(?s)example\.com/other.*\n\ngithub\.com/rjbs/testrepo\n.*\[abandoned\]`, "list", "-scope", "all", "-all-states")

	w.check("append", 0, `^`+regexp.QuoteMeta(b)+`: appended  Second\n$`, "append", b, "More came up.")
	w.check("append shows dated paragraph", 0, `(?s)Second\n\nstdin body\n\n\*\*\d{4}-\d\d-\d\d \d\d:\d\d \(human\):\*\*\nMore came up\.\n$`, "show", b)
	w.checkIn("from stdin\n", []string{"CLAUDECODE=1"}, "append from stdin as agent", 0, `appended`, "append", b)
	w.check("append stdin recorded", 0, `(?s)More came up\.\n\n\*\*[^\n]* \(agent\):\*\*\nfrom stdin\n$`, "show", b)
	w.check("append nothing", 1, `nothing to append`, "append", b, "  ")
	w.check("append missing thread", 1, `no thread matches`, "append", "zzzz", "x")

	w.checkIn("", []string{"EDITOR=true"}, "edit without a terminal", 1, `needs a terminal.*lt append[^\n]*\n  .*`+regexp.QuoteMeta(a)+`\.md`, "edit", a)

	w.check("double dash title", 0, idPat, "add", "--", "-leading-dash")
	w.check("show dash title", 0, `-leading-dash`, "list")
}

func TestOriginDefaults(t *testing.T) {
	w := newWorld(t)
	id := strings.TrimSpace(w.check("human add", 0, idPat, "add", "Human thing"))
	w.check("origin human", 0, `origin: human`, "show", id)

	id = strings.TrimSpace(w.checkIn("", []string{"CLAUDECODE=1"}, "agent add", 0, idPat, "add", "Agent thing"))
	w.check("origin agent", 0, `origin: agent`, "show", id)
}

func TestMoveProject(t *testing.T) {
	w := newWorld(t)
	w.check("add", 0, idPat, "add", "Thing")
	w.check("move", 0, `^github\.com/rjbs/testrepo: local -> work\n$`, "move-project", "work")
	w.check("still listed", 0, `Thing`, "list")
	w.check("move again is a no-op", 0, `already in work`, "move-project", "work")
	w.check("missing project", 1, `not found`, "move-project", "-project", "nope/nope", "work")
}

func TestRehome(t *testing.T) {
	w := newWorld(t)
	// newWorld gives the repo an origin remote; take it away to start out
	// path-identified.
	if out, err := exec.Command("git", "-C", w.cwd, "remote", "remove", "origin").CombinedOutput(); err != nil {
		t.Fatalf("git remote remove: %v\n%s", err, out)
	}
	w.check("rehome without remote", 1, `no remote-based identity`, "rehome")

	_, errOut, _ := w.run("", nil, "add", "Early one")
	if !strings.Contains(errOut, "lt rehome") {
		t.Errorf("add under a path identity should mention lt rehome: %q", errOut)
	}
	w.check("add another", 0, idPat, "add", "Early two")
	w.check("path identity", 0, `^/`, "project-id")

	if out, err := exec.Command("git", "-C", w.cwd, "remote", "add", "origin", "git@github.com:rjbs/testrepo.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	w.check("threads now invisible", 0, `^$`, "list")
	w.check("rehome", 0, `(?s)`+idPat+`: /.* -> github\.com/rjbs/testrepo\n`+idPat+`: /.* -> github\.com/rjbs/testrepo\n$`, "rehome")
	w.check("threads visible again", 0, `(?s)Early one.*Early two|Early two.*Early one`, "list")
	w.check("rehome again", 0, `nothing to do`, "rehome")
	w.check("only one project remains", 0, `(?s)^github\.com/rjbs/testrepo\n.*Early`, "list", "-scope", "all")
}

func TestSyncCommand(t *testing.T) {
	w := newWorld(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	w.check("no remotes yet", 0, `no collections have remotes`, "sync")
	w.check("sync add", 0, `^work: `, "sync", "add", "work", remote)
	w.check("bad usage", 1, `usage:`, "sync", "add", "work")

	// Route this project into work, then add a thread and sync.
	os.WriteFile(filepath.Join(w.store, "config.yaml"),
		[]byte("collections:\n  work:\n    remote: "+remote+"\nroutes:\n  - match: \"github.com/rjbs/*\"\n    collection: work\n"), 0o644)
	_, errOut, _ := w.run("", nil, "add", "Synced thing")
	if !strings.Contains(errOut, "in collection work") {
		t.Fatalf("expected routing into work: %q", errOut)
	}
	w.check("sync pushes", 0, `^work: committed, pushed\n$`, "sync")
	w.check("sync again", 0, `^work: up to date\n$`, "sync")
	w.check("one collection", 0, `up to date`, "sync", "-collection", "work")
	w.check("quiet", 0, `^$`, "sync", "-quiet")

	out, err := exec.Command("git", "-C", remote, "log", "--oneline").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "lt sync from") {
		t.Errorf("remote log: %v\n%s", err, out)
	}
}
