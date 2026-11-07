package editor

import (
	"fmt"
	"os"

	"github.com/rjbs/loosethreads/internal/thread"
)

// Session is one edit of a thread: its Markdown form written to a
// temporary file for the editor, and what that file held beforehand.
type Session struct {
	Path   string
	id     string
	before []byte
}

// Start writes t's Markdown form to a temporary file for editing.  The
// caller runs Command on the Session's Path, then calls Finish.
func Start(t *thread.Thread) (*Session, error) {
	data, err := t.Marshal()
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp("", "lt-"+t.ID+"-*.md")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		os.Remove(f.Name())
		return nil, err
	}
	return &Session{Path: f.Name(), id: t.ID, before: data}, nil
}

// Finish reads the edited file back and removes it.  It returns the
// edited thread, or nil if the file was not changed.
func (s *Session) Finish() (*thread.Thread, error) {
	defer os.Remove(s.Path)
	after, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	if string(after) == string(s.before) {
		return nil, nil
	}
	t, err := thread.Parse(after)
	if err != nil {
		return nil, fmt.Errorf("not saved: %w", err)
	}
	t.ID = s.id
	return t, nil
}
