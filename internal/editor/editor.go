// Package editor builds the command that opens a thread file in the
// user's editor.
package editor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Command returns a command running $VISUAL or $EDITOR (default vi) on
// path, through the shell so that an editor setting with arguments or
// quoting works.  Vim-family editors are started with the cursor on the
// first body line, past the frontmatter.  The command's stdio is not
// set; callers attach the terminal.
func Command(path string) *exec.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	var extra []string
	base := filepath.Base(strings.Fields(editor)[0])
	switch base {
	case "vi", "vim", "nvim":
		// Go to line 1 first so the search finds the closing delimiter
		// regardless of where the editor would otherwise start.
		extra = []string{"-c", "1", "-c", "/^---$/+1"}
	}

	args := append([]string{"-c", editor + ` "$@"`, base}, extra...)
	args = append(args, path)
	return exec.Command("sh", args...)
}
