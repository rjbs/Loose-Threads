package thread

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	created = time.Date(2026, 9, 4, 12, 40, 0, 0, time.FixedZone("EDT", -4*3600))
	closed  = time.Date(2026, 9, 5, 9, 0, 0, 0, time.FixedZone("EDT", -4*3600))
)

// checkRoundTrip marshals th, checks the bytes against want, then parses
// them back and checks the result equals th (ignoring ID).
func checkRoundTrip(t *testing.T, name string, th Thread, want string) {
	t.Helper()
	got, err := th.Marshal()
	if err != nil {
		t.Fatalf("%s: marshal: %v", name, err)
	}
	if string(got) != want {
		t.Errorf("%s: marshal\n got: %q\nwant: %q", name, got, want)
	}
	back, err := Parse(got)
	if err != nil {
		t.Fatalf("%s: parse: %v", name, err)
	}
	th.ID = ""
	if !strings.HasSuffix(th.Body, "\n") {
		th.Body += "\n"
	}
	if !equal(*back, th) {
		t.Errorf("%s: round trip\n got: %+v\nwant: %+v", name, *back, th)
	}
}

func equal(a, b Thread) bool {
	if a.State != b.State || !a.Created.Equal(b.Created) || a.Session != b.Session ||
		a.Transcript != b.Transcript || a.Origin != b.Origin || a.Body != b.Body {
		return false
	}
	switch {
	case a.Closed == nil && b.Closed == nil:
		return true
	case a.Closed == nil || b.Closed == nil:
		return false
	}
	return a.Closed.Equal(*b.Closed)
}

func checkParseError(t *testing.T, name, input string, want error) {
	t.Helper()
	_, err := Parse([]byte(input))
	if !errors.Is(err, want) {
		t.Errorf("%s: got error %v, want %v", name, err, want)
	}
}

func checkParseBody(t *testing.T, name, input, wantBody, wantTitle string) {
	t.Helper()
	th, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if th.Body != wantBody {
		t.Errorf("%s: body %q, want %q", name, th.Body, wantBody)
	}
	if th.Title() != wantTitle {
		t.Errorf("%s: title %q, want %q", name, th.Title(), wantTitle)
	}
}

func checkSetState(t *testing.T, name string, from State, hadClosed bool, to State, wantClosed bool) {
	t.Helper()
	th := Thread{State: from}
	if hadClosed {
		c := closed
		th.Closed = &c
	}
	if err := th.SetState(to, closed); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if th.State != to {
		t.Errorf("%s: state %q, want %q", name, th.State, to)
	}
	if (th.Closed != nil) != wantClosed {
		t.Errorf("%s: closed set = %v, want %v", name, th.Closed != nil, wantClosed)
	}
}

func TestRoundTrip(t *testing.T) {
	checkRoundTrip(t, "minimal",
		Thread{State: Open, Created: created, Body: "Fix the thing\n"},
		"---\nstate: open\ncreated: 2026-09-04T12:40:00-04:00\n---\nFix the thing\n")

	checkRoundTrip(t, "all fields",
		Thread{
			State: Done, Created: created, Session: "abc-123",
			Transcript: "/t/abc.jsonl", Closed: &closed, Origin: OriginAgent,
			Body: "Title: with colon [and] #hash\n\nBody paragraph.\n",
		},
		"---\nstate: done\ncreated: 2026-09-04T12:40:00-04:00\nsession: abc-123\n"+
			"transcript: /t/abc.jsonl\nclosed: 2026-09-05T09:00:00-04:00\norigin: agent\n---\n"+
			"Title: with colon [and] #hash\n\nBody paragraph.\n")

	checkRoundTrip(t, "body gets trailing newline",
		Thread{State: Open, Created: created, Body: "No newline"},
		"---\nstate: open\ncreated: 2026-09-04T12:40:00-04:00\n---\nNo newline\n")
}

func TestParseErrors(t *testing.T) {
	checkParseError(t, "empty", "", ErrNoFrontmatter)
	checkParseError(t, "no delimiter", "state: open\n", ErrNoFrontmatter)
	checkParseError(t, "unterminated", "---\nstate: open\n", ErrUnterminated)
	checkParseError(t, "dashes in value not delimiter", "---\nstate: ---\nfoo: bar\n", ErrUnterminated)
	checkParseError(t, "bad state", "---\nstate: pending\n---\nx\n", ErrInvalidState)
	checkParseError(t, "missing state", "---\ncreated: 2026-09-04T12:40:00-04:00\n---\nx\n", ErrInvalidState)
}

func TestParseBody(t *testing.T) {
	checkParseBody(t, "simple", "---\nstate: open\n---\nTitle\n\nBody\n", "Title\n\nBody\n", "Title")
	checkParseBody(t, "leading blank lines", "---\nstate: open\n---\n\n\n  Title  \nBody\n", "\n\n  Title  \nBody\n", "Title")
	checkParseBody(t, "empty body", "---\nstate: open\n---\n", "", "")
	checkParseBody(t, "no newline after delimiter", "---\nstate: open\n---", "", "")
	checkParseBody(t, "body has delimiter lines", "---\nstate: open\n---\nTitle\n---\nrule above\n", "Title\n---\nrule above\n", "Title")
}

func TestSetState(t *testing.T) {
	checkSetState(t, "open to done sets closed", Open, false, Done, true)
	checkSetState(t, "open to abandoned sets closed", Open, false, Abandoned, true)
	checkSetState(t, "done to open clears closed", Done, true, Open, false)
	checkSetState(t, "done to abandoned keeps closed", Done, true, Abandoned, true)
	checkSetState(t, "open to open stays unclosed", Open, false, Open, false)

	var th Thread
	if err := th.SetState("bogus", closed); !errors.Is(err, ErrInvalidState) {
		t.Errorf("bogus state: got %v, want ErrInvalidState", err)
	}
}

func checkResolve(t *testing.T, name, body string, to State, note, wantBody string) {
	t.Helper()
	th := Thread{State: Open, Body: body}
	if err := th.Resolve(to, note, closed); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if th.State != to {
		t.Errorf("%s: state %q, want %q", name, th.State, to)
	}
	if th.Body != wantBody {
		t.Errorf("%s: body\n got: %q\nwant: %q", name, th.Body, wantBody)
	}
}

func TestResolve(t *testing.T) {
	checkResolve(t, "done with note", "Title\n\nBody.\n", Done, "shipped it",
		"Title\n\nBody.\n\nDone 2026-09-05: shipped it\n")
	checkResolve(t, "abandoned with note", "Title\n", Abandoned, "  not worth it  ",
		"Title\n\nAbandoned 2026-09-05: not worth it\n")
	checkResolve(t, "reopen with note", "Title\n", Open, "it came back",
		"Title\n\nReopened 2026-09-05: it came back\n")
	checkResolve(t, "empty note leaves body alone", "Title\n\nBody.\n", Done, "   ",
		"Title\n\nBody.\n")
	checkResolve(t, "body without trailing newline", "Title", Done, "ok",
		"Title\n\nDone 2026-09-05: ok\n")
	checkResolve(t, "empty body", "", Done, "ok",
		"Done 2026-09-05: ok\n")

	var th Thread
	if err := th.Resolve("bogus", "x", closed); !errors.Is(err, ErrInvalidState) {
		t.Errorf("bogus state: got %v, want ErrInvalidState", err)
	}
}

func checkAppend(t *testing.T, name, body, text string, by Origin, wantBody string) {
	t.Helper()
	th := Thread{State: Open, Body: body}
	th.Append(text, by, closed)
	if th.Body != wantBody {
		t.Errorf("%s: body\n got: %q\nwant: %q", name, th.Body, wantBody)
	}
}

func TestAppend(t *testing.T) {
	checkAppend(t, "agent adds a paragraph", "Title\n\nBody.\n", "Turns out it also affects sync.", OriginAgent,
		"Title\n\nBody.\n\n**2026-09-05 09:00 (agent):**\nTurns out it also affects sync.\n")
	checkAppend(t, "human, multi-line, trimmed", "Title\n", "  one\ntwo  \n", OriginHuman,
		"Title\n\n**2026-09-05 09:00 (human):**\none\ntwo\n")
	checkAppend(t, "blank text is ignored", "Title\n", "  \n", OriginAgent, "Title\n")
	checkAppend(t, "body without trailing newline", "Title", "x", OriginAgent,
		"Title\n\n**2026-09-05 09:00 (agent):**\nx\n")
}

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^2026-09-04-[abcdefghjkmnpqrstuvwxyz23456789]{6}$`)
	seen := map[string]bool{}
	for range 50 {
		id := NewID(created)
		if !re.MatchString(id) {
			t.Fatalf("id %q does not match %v", id, re)
		}
		seen[id] = true
	}
	if len(seen) < 40 {
		t.Errorf("only %d distinct ids in 50 draws", len(seen))
	}
}
