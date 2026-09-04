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
	w.env = []string{"LOOSETHREADS_HOME=" + w.store, "PATH=" + os.Getenv("PATH"), "HOME=" + base}
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

const idPat = `2\d\d\d-\d\d-\d\d-[a-z2-9]{4}`

func TestCLI(t *testing.T) {
	w := newWorld(t)

	w.check("project-id", 0, `^github\.com/rjbs/testrepo\n$`, "project-id")
	w.check("no args", 2, `usage:`)
	w.check("unknown command", 2, `unknown command`, "frobnicate")
	w.check("list before any threads", 0, `^$`, "list")

	a := strings.TrimSpace(w.check("add with body flag", 0, `^`+idPat+`\n$`,
		"add", "First: with colon", "-body", "Some context."))
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

func TestEditPositionsCursor(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim not available")
	}
	w := newWorld(t)
	id := strings.TrimSpace(w.check("add", 0, idPat, "add", "Title here", "-body", "body"))

	out := filepath.Join(t.TempDir(), "line")
	// A real vim in Ex mode that records the cursor line once startup,
	// including lt's own -c positioning commands, has finished.
	editor := "vim -es -c \"autocmd VimEnter * call writefile([line('.')], '" + out + "') | q!\""
	w.checkIn("", []string{"EDITOR=" + editor}, "edit", 0, ``, "edit", id)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// Four frontmatter lines (state, created, origin, delimiter) plus the
	// opening delimiter puts the title on line 6.
	if strings.TrimSpace(string(got)) != "6" {
		t.Errorf("cursor on line %s, want 6", got)
	}
}
