package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rjbs/loosethreads/internal/store"
)

const defaultProject = "github.com/rjbs/default"

// harness is an in-memory client connected to a server over a fresh store.
type harness struct {
	t       *testing.T
	store   *store.Store
	session *mcp.ClientSession
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(s, defaultProject)

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &harness{t: t, store: s, session: cs}
}

// call invokes a tool and returns the structured output decoded into out.
// It fails the test if the call errors and wantErr is empty, or if the
// error text does not contain wantErr.
func (h *harness) call(name string, args map[string]any, wantErr string, out any) {
	h.t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s(%v): protocol error: %v", name, args, err)
	}
	if res.IsError {
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		if wantErr == "" {
			h.t.Fatalf("%s(%v): unexpected tool error: %s", name, args, text)
		}
		if !strings.Contains(text, wantErr) {
			h.t.Errorf("%s(%v): error %q does not contain %q", name, args, text, wantErr)
		}
		return
	}
	if wantErr != "" {
		h.t.Fatalf("%s(%v): expected error containing %q, got success", name, args, wantErr)
	}
	if out == nil {
		return
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		h.t.Fatalf("%s: decoding structured output: %v\n%s", name, err, data)
	}
}

func (h *harness) add(title, body, project, session string) store.View {
	h.t.Helper()
	var v store.View
	h.call("add_thread", map[string]any{"title": title, "body": body, "project": project, "session": session}, "", &v)
	return v
}

func (h *harness) list(args map[string]any) []string {
	h.t.Helper()
	var out struct {
		Threads []store.View `json:"threads"`
	}
	h.call("list_threads", args, "", &out)
	var titles []string
	for _, v := range out.Threads {
		titles = append(titles, v.Title)
	}
	return titles
}

func checkTitles(t *testing.T, name string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s: titles %q, want %q", name, got, want)
	}
}

func TestTools(t *testing.T) {
	h := newHarness(t)

	checkTitles(t, "empty project", h.list(nil))

	a := h.add("First: colon", "Because reasons.", "", "sess-1")
	if a.Project != defaultProject || a.State != "open" || a.Origin != "agent" || a.Session != "sess-1" {
		t.Errorf("add returned %+v", a)
	}
	if a.Body != "First: colon\n\nBecause reasons.\n" {
		t.Errorf("body %q", a.Body)
	}
	time.Sleep(time.Millisecond)
	b := h.add("Second", "", "", "sess-2")
	h.add("Other project", "", "example.com/other", "")

	got := h.list(nil)
	if len(got) != 2 {
		t.Fatalf("project scope: %q", got)
	}
	checkTitles(t, "session scope", h.list(map[string]any{"scope": "session", "session": "sess-1"}), "First: colon")
	all := h.list(map[string]any{"scope": "all"})
	if len(all) != 3 {
		t.Errorf("all scope: %q", all)
	}
	checkTitles(t, "explicit project", h.list(map[string]any{"project": "example.com/other"}), "Other project")
	checkTitles(t, "unknown project is empty", h.list(map[string]any{"project": "nope/nope"}))

	var v store.View
	h.call("get_thread", map[string]any{"id": a.ID[len(a.ID)-4:]}, "", &v)
	if v.ID != a.ID {
		t.Errorf("get by suffix returned %q", v.ID)
	}
	h.call("get_thread", map[string]any{"id": "zzzz"}, "no thread matches", nil)

	h.call("set_thread_state", map[string]any{"id": a.ID, "state": "done"}, "", &v)
	if v.State != "done" || v.Closed == nil {
		t.Errorf("set state returned %+v", v)
	}
	checkTitles(t, "closed hidden", h.list(nil), "Second")
	if got := h.list(map[string]any{"include_closed": true}); len(got) != 2 {
		t.Errorf("include_closed: %q", got)
	}
	h.call("set_thread_state", map[string]any{"id": b.ID, "state": "pending"}, "state must be", nil)
	h.call("set_thread_state", map[string]any{"id": b.ID, "state": "abandoned", "note": "superseded"}, "", &v)
	if !strings.HasSuffix(v.Body, "(agent):**\nAbandoned: superseded\n") {
		t.Errorf("note not appended: %q", v.Body)
	}

	h.call("append_thread", map[string]any{"id": a.ID, "text": "also affects sync"}, "", &v)
	if !regexp.MustCompile(`\n\n\*\*\d{4}-\d\d-\d\d \d\d:\d\d \(agent\):\*\*\nalso affects sync\n$`).MatchString(v.Body) {
		t.Errorf("append not recorded: %q", v.Body)
	}
	h.call("append_thread", map[string]any{"id": a.ID, "text": "  "}, "blank", nil)
	h.call("append_thread", map[string]any{"id": "zzzz", "text": "x"}, "no thread matches", nil)

	h.call("add_thread", map[string]any{"title": "   "}, "blank", nil)
	h.call("add_thread", map[string]any{}, "missing properties", nil) // schema validation, before the handler
	h.call("list_threads", map[string]any{"scope": "session"}, "requires a session", nil)
	h.call("list_threads", map[string]any{"scope": "galaxy"}, "unknown scope", nil)
}

func TestNoDefaultProject(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(s, "")
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	h := &harness{t: t, store: s, session: cs}
	h.call("add_thread", map[string]any{"title": "x"}, "no project given", nil)
	h.add("x", "", "explicit/project", "")
}
