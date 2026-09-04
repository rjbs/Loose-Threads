// Package thread defines the on-disk format of a single Loose Threads
// item: a Markdown file with YAML frontmatter, whose first non-blank body
// line is the title.
package thread

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// State is the lifecycle state of a thread.
type State string

const (
	Open      State = "open"
	Done      State = "done"
	Abandoned State = "abandoned"
)

// Origin records who created a thread.
type Origin string

const (
	OriginAgent Origin = "agent"
	OriginHuman Origin = "human"
)

// Thread is one deferred item.  ID is the filename without extension and
// is not stored in the file itself.
type Thread struct {
	ID string `yaml:"-"`

	State      State      `yaml:"state"`
	Created    time.Time  `yaml:"created"`
	Session    string     `yaml:"session,omitempty"`
	Transcript string     `yaml:"transcript,omitempty"`
	Closed     *time.Time `yaml:"closed,omitempty"`
	Origin     Origin     `yaml:"origin,omitempty"`

	// Body is everything after the frontmatter, with the closing delimiter's
	// newline consumed.  Its first non-blank line is the title.
	Body string `yaml:"-"`
}

var (
	ErrNoFrontmatter = errors.New("thread: file does not begin with frontmatter")
	ErrUnterminated  = errors.New("thread: frontmatter is not terminated")
	ErrInvalidState  = errors.New("thread: invalid state")
)

const delimiter = "---"

// ValidState reports whether s is one of the known states.
func ValidState(s State) bool {
	switch s {
	case Open, Done, Abandoned:
		return true
	}
	return false
}

// Parse decodes a thread file.  The returned Thread has no ID; the caller
// knows the filename.
func Parse(data []byte) (*Thread, error) {
	front, body, err := split(data)
	if err != nil {
		return nil, err
	}

	var t Thread
	if err := yaml.Unmarshal(front, &t); err != nil {
		return nil, fmt.Errorf("thread: decoding frontmatter: %w", err)
	}
	if !ValidState(t.State) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidState, t.State)
	}
	t.Body = string(body)
	return &t, nil
}

// split separates the frontmatter (without delimiters) from the body.
// The delimiter is a line consisting of exactly "---".
func split(data []byte) (front, body []byte, err error) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines) == 0 || !isDelimiter(lines[0]) {
		return nil, nil, ErrNoFrontmatter
	}
	for i := 1; i < len(lines); i++ {
		if isDelimiter(lines[i]) {
			return bytes.Join(lines[1:i], nil), bytes.Join(lines[i+1:], nil), nil
		}
	}
	return nil, nil, ErrUnterminated
}

func isDelimiter(line []byte) bool {
	return string(bytes.TrimSuffix(line, []byte("\n"))) == delimiter
}

// Marshal encodes a thread as a file.
func (t *Thread) Marshal() ([]byte, error) {
	if !ValidState(t.State) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidState, t.State)
	}

	var buf bytes.Buffer
	buf.WriteString(delimiter + "\n")
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(t); err != nil {
		return nil, fmt.Errorf("thread: encoding frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	buf.WriteString(delimiter + "\n")
	buf.WriteString(t.Body)
	if !strings.HasSuffix(t.Body, "\n") {
		buf.WriteString("\n")
	}
	return buf.Bytes(), nil
}

// Title returns the first non-blank line of the body, trimmed.  A thread
// with an empty body has an empty title.
func (t *Thread) Title() string {
	for _, line := range strings.Split(t.Body, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// SetState changes the state, maintaining Closed: set when leaving Open,
// cleared when returning to it.
func (t *Thread) SetState(s State, now time.Time) error {
	if !ValidState(s) {
		return fmt.Errorf("%w: %q", ErrInvalidState, s)
	}
	switch {
	case s == Open:
		t.Closed = nil
	case t.State == Open:
		closed := now
		t.Closed = &closed
	}
	t.State = s
	return nil
}

// IsOpen reports whether the thread is still open.
func (t *Thread) IsOpen() bool { return t.State == Open }

// Base32 alphabet without the characters most easily confused in print.
const idAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// NewID returns a fresh thread id: the date followed by four random
// characters.  Ids sort by creation day; the random part need only be
// unique within one project directory.
func NewID(now time.Time) string {
	var sb strings.Builder
	sb.WriteString(now.Format("2006-01-02"))
	sb.WriteByte('-')
	for range 4 {
		sb.WriteByte(idAlphabet[rand.IntN(len(idAlphabet))])
	}
	return sb.String()
}
