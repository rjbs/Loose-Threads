package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
	ltsync "github.com/rjbs/loosethreads/internal/sync"
	"github.com/rjbs/loosethreads/internal/thread"
)

func init() {
	register(&command{"hook", "act as a Claude Code hook (session-start, pre-compact)", runHook})
}

// hookInput is the JSON Claude Code writes to a hook's stdin.  Only the
// fields we use are listed.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"` // SessionStart: startup, resume, clear, compact
}

// hookOutput is the JSON a hook writes to stdout to feed text back into
// the model's context.
type hookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func runHook(args []string) error {
	fs := newFlagSet("hook", "session-start|pre-compact")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one hook name is required")
	}

	var in hookInput
	if data, err := io.ReadAll(os.Stdin); err != nil {
		return err
	} else if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &in); err != nil {
			return fmt.Errorf("decoding hook input: %w", err)
		}
	}

	switch pos[0] {
	case "session-start":
		return hookSessionStart(in)
	case "pre-compact":
		return hookPreCompact(in)
	}
	return fmt.Errorf("unknown hook %q", pos[0])
}

func emitContext(event, text string) error {
	var out hookOutput
	out.HookSpecificOutput.HookEventName = event
	out.HookSpecificOutput.AdditionalContext = text
	return json.NewEncoder(os.Stdout).Encode(out)
}

// hookSessionStart tells the model its session and project identity and
// lists the project's open threads.  Errors in identifying the project
// are reported into context rather than failing the hook, since a hook
// failure would only produce a confusing message at session start.
func hookSessionStart(in hookInput) error {
	dir := in.CWD
	if dir == "" {
		dir = "."
	}

	var b strings.Builder
	b.WriteString("# Loose Threads\n\n")
	if in.Source == "compact" {
		b.WriteString("Context was just compacted.  If deferred work was discussed before the\n")
		b.WriteString("compaction and does not appear in the list below, record it now.\n\n")
	}
	b.WriteString("Loose Threads is a per-project backlog of small deferred items, shared between\n")
	b.WriteString("you and the human across sessions.  When work is deferred (\"we should also\n")
	b.WriteString("fix X, but not now\"), record it immediately:\n\n")
	b.WriteString("    lt add \"short title\" -body \"why it came up and what to do\"\n\n")
	b.WriteString("Session id and origin are picked up from the environment.  When asked what\n")
	b.WriteString("was deferred, run `lt list` (this project) or `lt list -scope session` (this\n")
	b.WriteString("session) rather than recalling from memory.  Close items with `lt done ID` or\n")
	b.WriteString("`lt abandon ID`, adding `-note \"why\"` when the reason is not obvious from a\n")
	b.WriteString("commit, especially for threads that were questions to decide.\n\n")

	if in.SessionID != "" {
		fmt.Fprintf(&b, "Session id: %s\n", in.SessionID)
	}

	id, err := project.Identify(dir)
	if err != nil {
		fmt.Fprintf(&b, "Project: could not be identified (%v)\n", err)
		return emitContext("SessionStart", b.String())
	}
	fmt.Fprintf(&b, "Project id: %s\n", id.ID)
	if project.IsPath(id.ID) {
		fmt.Fprintf(&b, "Warning: %s.  Mention this to the user.\n", project.PathWarning)
	}
	b.WriteString("\n")

	s, err := store.Open("")
	if err != nil {
		return err
	}
	syncAtStart(s, &b)
	p, err := s.LookupProject(id.ID)
	if err != nil {
		b.WriteString("No threads have been recorded for this project yet.\n")
		return emitContext("SessionStart", b.String())
	}
	ths, errs, err := s.Threads(p)
	if err != nil {
		return err
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "warning: %v\n", e)
	}

	var open []*thread.Thread
	for _, t := range ths {
		if t.IsOpen() {
			open = append(open, t)
		}
	}
	if len(open) == 0 {
		b.WriteString("There are no open threads for this project.\n")
		return emitContext("SessionStart", b.String())
	}

	fmt.Fprintf(&b, "Open threads for this project (%d):\n\n", len(open))
	for _, t := range open {
		mark := "  "
		if in.SessionID != "" && t.Session == in.SessionID {
			mark = "* "
		}
		fmt.Fprintf(&b, "%s%s  %s\n", mark, t.ID, t.Title())
	}
	if in.SessionID != "" {
		b.WriteString("\n(* marks threads recorded by this session.)\n")
	}
	return emitContext("SessionStart", b.String())
}

// syncAtStart pulls every synced collection so the open list reflects
// other machines, bounded so a slow network cannot stall session start.
// Problems are noted in the context rather than failing the hook.
func syncAtStart(s *store.Store, b *strings.Builder) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	results, err := ltsync.All(ctx, s)
	for _, r := range results {
		if len(r.Conflicts) > 0 {
			fmt.Fprintf(b, "Sync: %s\n", r)
		}
	}
	if err != nil && !errors.Is(err, ltsync.ErrConflict) {
		fmt.Fprintf(b, "Sync failed (working from the local copy): %v\n", err)
	}
}

// hookPreCompact reminds the model to record deferred work before the
// conversation is summarized, which is when such items are most often
// lost.
func hookPreCompact(in hookInput) error {
	text := "Context is about to be compacted.  Before it is, record any deferred work\n" +
		"that has not yet been written down, with `lt add \"title\" -body \"context\"`.\n" +
		"Check `lt list -scope session` to see what this session has already recorded."
	return emitContext("PreCompact", text)
}
