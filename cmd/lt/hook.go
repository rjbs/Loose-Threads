package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rjbs/loosethreads/internal/project"
	"github.com/rjbs/loosethreads/internal/store"
	"github.com/rjbs/loosethreads/internal/thread"
)

// The prose given to the model by the hooks lives in hooktext/ so it can
// be edited as plain text.  Each is trimmed of surrounding whitespace by
// hookText, so trailing newlines in the files don't matter.
var (
	//go:embed hooktext/session-start.md
	sessionStartText string

	//go:embed hooktext/compacted.md
	compactedText string

	//go:embed hooktext/pre-compact.md
	preCompactText string
)

// hookText returns an embedded text with surrounding whitespace removed.
func hookText(s string) string {
	return strings.TrimSpace(s)
}

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
		b.WriteString(hookText(compactedText) + "\n\n")
	}
	b.WriteString(hookText(sessionStartText) + "\n\n")

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
	defer s.Close()
	syncAllInBackground(s)
	writeSyncProblems(&b, s)
	p, err := s.LookupProject(id.ID)
	if err != nil {
		b.WriteString("No threads have been recorded for this project yet.\n")
		return emitContext("SessionStart", b.String())
	}
	ths, err := s.Threads(p)
	if err != nil {
		return err
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

// hookPreCompact reminds the model to record deferred work before the
// conversation is summarized, which is when such items are most often
// lost.
func hookPreCompact(in hookInput) error {
	return emitContext("PreCompact", hookText(preCompactText))
}
