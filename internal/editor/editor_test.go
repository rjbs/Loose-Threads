package editor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// checkCursorLine runs Command on a file with the given editor setting
// and checks which line the cursor lands on.  The editor is a real vim
// in Ex mode that records the cursor line once startup, including the
// -c positioning commands, has finished.
func checkCursorLine(t *testing.T, name, content, want string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "thread.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "line")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "vim -es -c \"autocmd VimEnter * call writefile([line('.')], '"+out+"') | q!\"")
	if err := Command(path).Run(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("%s: cursor on line %s, want %s", name, strings.TrimSpace(string(got)), want)
	}
}

func TestVimCursorOnTitle(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim not available")
	}
	checkCursorLine(t, "three frontmatter lines",
		"---\nstate: open\ncreated: 2026-09-04T12:40:00-04:00\norigin: human\n---\nTitle here\n\nbody\n", "6")
	checkCursorLine(t, "one frontmatter line",
		"---\nstate: open\n---\nTitle here\n", "4")
}
