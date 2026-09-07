// Package mcpserver exposes the Loose Threads store to Claude Code as MCP
// tools.  The server does not know which session is calling; the agent
// passes session and project ids explicitly, having learned them from the
// SessionStart hook.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rjbs/loosethreads/internal/store"
	ltsync "github.com/rjbs/loosethreads/internal/sync"
	"github.com/rjbs/loosethreads/internal/thread"
)

// Version is reported to MCP clients.
const Version = "0.1.0"

type server struct {
	store          *store.Store
	defaultProject string
	now            func() time.Time
}

// New returns an MCP server over s.  defaultProject is used when a tool
// call omits the project; it is normally the identity of the directory
// the server was started in.
func New(s *store.Store, defaultProject string) *mcp.Server {
	srv := &server{store: s, defaultProject: defaultProject, now: time.Now}

	m := mcp.NewServer(&mcp.Implementation{
		Name:    "loosethreads",
		Title:   "Loose Threads",
		Version: Version,
	}, &mcp.ServerOptions{
		Instructions: "Loose Threads is a per-project backlog of small deferred items, shared " +
			"between the agent and the human across sessions.  When work is deferred " +
			"(\"we should also fix X, but not now\"), call add_thread immediately with a " +
			"short title, a body explaining why it came up, and your session id.  When " +
			"asked what was deferred, call list_threads rather than recalling from memory.",
	})

	mcp.AddTool(m, &mcp.Tool{
		Name: "add_thread",
		Description: "Record a deferred item.  The title is one line; the body is free " +
			"Markdown explaining what was being done and why the item was deferred.  " +
			"Pass your session id so the item can be traced back.",
	}, srv.addThread)

	mcp.AddTool(m, &mcp.Tool{
		Name: "list_threads",
		Description: "List threads.  scope is \"project\" (default: the current project), " +
			"\"session\" (only threads recorded by the given session), or \"all\" " +
			"(every project).  Closed threads are omitted unless include_closed is true.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, srv.listThreads)

	mcp.AddTool(m, &mcp.Tool{
		Name:        "get_thread",
		Description: "Fetch one thread by id, or by a unique suffix of its id.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, srv.getThread)

	mcp.AddTool(m, &mcp.Tool{
		Name: "append_thread",
		Description: "Add to an existing thread: new context, a partial result, a change of " +
			"plan.  The text is appended as a paragraph headed by a timestamp and " +
			"\"(agent)\", so the thread keeps its history.  This is the way to revise " +
			"a thread; there is no tool for editing one in place.",
	}, srv.appendThread)

	mcp.AddTool(m, &mcp.Tool{
		Name: "set_thread_state",
		Description: "Change a thread's state to open, done, or abandoned.  Use done when " +
			"the work was completed and abandoned when it will not be.  Pass a note " +
			"saying why, especially for threads that were questions to decide; it is " +
			"appended to the thread so a later session can see the outcome.",
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
	}, srv.setThreadState)

	return m
}

func (s *server) project(id string, create bool) (*store.Project, error) {
	if id == "" {
		id = s.defaultProject
	}
	if id == "" {
		return nil, fmt.Errorf("no project given and none could be derived from the working directory")
	}
	if create {
		return s.store.Project(id)
	}
	return s.store.LookupProject(id)
}

type addInput struct {
	Title      string `json:"title" jsonschema:"one-line summary of the deferred item"`
	Body       string `json:"body,omitempty" jsonschema:"Markdown explaining the context: what was being done, why this was deferred, what to do"`
	Project    string `json:"project,omitempty" jsonschema:"project id; defaults to the project of the server's working directory"`
	Session    string `json:"session,omitempty" jsonschema:"the calling session's id, for provenance"`
	Transcript string `json:"transcript,omitempty" jsonschema:"path to the calling session's transcript, if known"`
}

func (s *server) addThread(ctx context.Context, req *mcp.CallToolRequest, in addInput) (*mcp.CallToolResult, store.View, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, store.View{}, fmt.Errorf("title must not be blank")
	}
	p, err := s.project(in.Project, true)
	if err != nil {
		return nil, store.View{}, err
	}

	body := title + "\n"
	if text := strings.TrimSpace(in.Body); text != "" {
		body += "\n" + text + "\n"
	}
	t := &thread.Thread{
		State:      thread.Open,
		Created:    s.now().Truncate(time.Second),
		Session:    in.Session,
		Transcript: in.Transcript,
		Origin:     thread.OriginAgent,
		Body:       body,
	}
	if err := s.store.Create(p, t, t.Created); err != nil {
		return nil, store.View{}, err
	}
	s.pushLater(p)
	return nil, s.store.View(p, t), nil
}

// pushLater syncs p's collection in the background if it has a remote.
func (s *server) pushLater(p *store.Project) {
	if s.store.Config().Remote(p.Collection) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		ltsync.Collection(ctx, s.store, p.Collection)
	}()
}

type listInput struct {
	Scope         string `json:"scope,omitempty" jsonschema:"project (default), session, or all"`
	Project       string `json:"project,omitempty" jsonschema:"project id; defaults to the project of the server's working directory"`
	Session       string `json:"session,omitempty" jsonschema:"session id; required for scope session"`
	IncludeClosed bool   `json:"include_closed,omitempty" jsonschema:"also return done and abandoned threads"`
}

type listOutput struct {
	Threads []store.View `json:"threads"`
}

func (s *server) listThreads(ctx context.Context, req *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, listOutput, error) {
	out := listOutput{Threads: []store.View{}}
	scope := in.Scope
	if scope == "" {
		scope = "project"
	}

	var projects []*store.Project
	switch scope {
	case "all":
		ps, err := s.store.Projects()
		if err != nil {
			return nil, out, err
		}
		projects = ps
	case "project", "session":
		if scope == "session" && in.Session == "" {
			return nil, out, fmt.Errorf("scope session requires a session id")
		}
		p, err := s.project(in.Project, false)
		if err != nil {
			if strings.Contains(err.Error(), store.ErrNotFound.Error()) {
				return nil, out, nil // a project with no threads has nothing to list
			}
			return nil, out, err
		}
		projects = []*store.Project{p}
	default:
		return nil, out, fmt.Errorf("unknown scope %q", in.Scope)
	}

	for _, p := range projects {
		ths, _, err := s.store.Threads(p)
		if err != nil {
			return nil, out, err
		}
		for _, t := range ths {
			if !in.IncludeClosed && !t.IsOpen() {
				continue
			}
			if scope == "session" && t.Session != in.Session {
				continue
			}
			out.Threads = append(out.Threads, s.store.View(p, t))
		}
	}
	return nil, out, nil
}

type getInput struct {
	ID      string `json:"id" jsonschema:"thread id, or a unique suffix of one"`
	Project string `json:"project,omitempty" jsonschema:"project id; defaults to the project of the server's working directory"`
}

func (s *server) getThread(ctx context.Context, req *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, store.View, error) {
	p, err := s.project(in.Project, false)
	if err != nil {
		return nil, store.View{}, err
	}
	t, err := s.store.Resolve(p, in.ID)
	if err != nil {
		return nil, store.View{}, err
	}
	return nil, s.store.View(p, t), nil
}

type appendInput struct {
	ID      string `json:"id" jsonschema:"thread id, or a unique suffix of one"`
	Text    string `json:"text" jsonschema:"Markdown to append; a timestamp line is added above it"`
	Project string `json:"project,omitempty" jsonschema:"project id; defaults to the project of the server's working directory"`
}

func (s *server) appendThread(ctx context.Context, req *mcp.CallToolRequest, in appendInput) (*mcp.CallToolResult, store.View, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, store.View{}, fmt.Errorf("text must not be blank")
	}
	p, err := s.project(in.Project, false)
	if err != nil {
		return nil, store.View{}, err
	}
	t, err := s.store.Resolve(p, in.ID)
	if err != nil {
		return nil, store.View{}, err
	}
	t.Append(in.Text, thread.OriginAgent, s.now().Truncate(time.Second))
	if err := s.store.Save(p, t); err != nil {
		return nil, store.View{}, err
	}
	s.pushLater(p)
	return nil, s.store.View(p, t), nil
}

type setStateInput struct {
	ID      string `json:"id" jsonschema:"thread id, or a unique suffix of one"`
	State   string `json:"state" jsonschema:"open, done, or abandoned"`
	Note    string `json:"note,omitempty" jsonschema:"one line saying why: what was decided, where it was fixed, why it was dropped.  Appended to the thread"`
	Project string `json:"project,omitempty" jsonschema:"project id; defaults to the project of the server's working directory"`
}

func (s *server) setThreadState(ctx context.Context, req *mcp.CallToolRequest, in setStateInput) (*mcp.CallToolResult, store.View, error) {
	if !thread.ValidState(thread.State(in.State)) {
		return nil, store.View{}, fmt.Errorf("state must be open, done, or abandoned, not %q", in.State)
	}
	p, err := s.project(in.Project, false)
	if err != nil {
		return nil, store.View{}, err
	}
	t, err := s.store.Resolve(p, in.ID)
	if err != nil {
		return nil, store.View{}, err
	}
	if err := t.Resolve(thread.State(in.State), in.Note, s.now().Truncate(time.Second)); err != nil {
		return nil, store.View{}, err
	}
	if err := s.store.Save(p, t); err != nil {
		return nil, store.View{}, err
	}
	s.pushLater(p)
	return nil, s.store.View(p, t), nil
}
