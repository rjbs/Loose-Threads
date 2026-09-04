package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// checkHook runs "lt hook NAME" with the given stdin JSON and asserts the
// event name and a regexp over the additionalContext text.
func (w *world) checkHook(name, hook, stdin, wantEvent, wantMatch string) {
	w.t.Helper()
	out, errOut, code := w.run(stdin, nil, "hook", hook)
	if code != 0 {
		w.t.Errorf("%s: exit %d\nstderr: %s", name, code, errOut)
		return
	}
	var got hookOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		w.t.Errorf("%s: output is not hook JSON: %v\n%s", name, err, out)
		return
	}
	if got.HookSpecificOutput.HookEventName != wantEvent {
		w.t.Errorf("%s: event %q, want %q", name, got.HookSpecificOutput.HookEventName, wantEvent)
	}
	if !regexp.MustCompile(wantMatch).MatchString(got.HookSpecificOutput.AdditionalContext) {
		w.t.Errorf("%s: context does not match %q:\n%s", name, wantMatch, got.HookSpecificOutput.AdditionalContext)
	}
}

func TestHookSessionStart(t *testing.T) {
	w := newWorld(t)
	input := func(session string) string {
		return `{"session_id":"` + session + `","transcript_path":"/t.jsonl","cwd":"` + w.cwd + `","hook_event_name":"SessionStart","source":"startup"}`
	}
	compact := `{"session_id":"s1","cwd":"` + w.cwd + `","hook_event_name":"SessionStart","source":"compact"}`

	w.checkHook("no store yet", "session-start", input("s1"), "SessionStart",
		`(?s)Session id: s1\nProject id: github\.com/rjbs/testrepo\n.*No threads have been recorded`)

	mine := strings.TrimSpace(w.checkIn("", []string{"CLAUDE_CODE_SESSION_ID=s1"}, "add mine", 0, idPat, "add", "Mine"))
	other := strings.TrimSpace(w.checkIn("", []string{"CLAUDE_CODE_SESSION_ID=s2"}, "add other", 0, idPat, "add", "Theirs"))
	closed := strings.TrimSpace(w.check("add closed", 0, idPat, "add", "Closed"))
	w.check("close it", 0, `done`, "done", closed)

	// The two threads share a creation date, so their order is arbitrary;
	// check each line separately.
	w.checkHook("lists open threads", "session-start", input("s1"), "SessionStart",
		`(?s)Open threads for this project \(2\):\n\n.*\n\n\(\* marks`)
	w.checkHook("marks this session's thread", "session-start", input("s1"), "SessionStart",
		`(?m)^\* `+regexp.QuoteMeta(mine)+`  Mine$`)
	w.checkHook("leaves other session's thread unmarked", "session-start", input("s1"), "SessionStart",
		`(?m)^  `+regexp.QuoteMeta(other)+`  Theirs$`)

	w.checkHook("after compaction", "session-start", compact, "SessionStart",
		`(?s)^# Loose Threads\n\nContext was just compacted\..*Open threads for this project \(2\)`)
	w.checkHook("startup does not mention compaction", "session-start", input("s1"), "SessionStart",
		`^# Loose Threads\n\nLoose Threads is`)
	w.checkHook("pre-compact", "pre-compact", input("s1"), "PreCompact", `about to be compacted`)
	w.checkHook("empty stdin", "session-start", "", "SessionStart", `Loose Threads`)
	w.checkHook("no session id", "session-start", `{"cwd":"`+w.cwd+`"}`, "SessionStart",
		`(?s)^[^*]*Open threads for this project \(2\):\n\n  `+idPat+`  (Mine|Theirs)\n  `+idPat+`  (Mine|Theirs)\n$`)

	plain := t.TempDir()
	w.checkHook("path identity warns", "session-start", `{"cwd":"`+plain+`"}`, "SessionStart",
		`(?s)Project id: /.*\nWarning: this project is identified by its checkout path`)
	w.checkHook("remote identity does not warn", "session-start", input("s1"), "SessionStart",
		`Project id: github\.com/rjbs/testrepo\n\n`)

	_, errOut, code := w.run("not json", nil, "hook", "session-start")
	if code != 1 || !strings.Contains(errOut, "decoding hook input") {
		t.Errorf("bad json: exit %d, stderr %q", code, errOut)
	}
	_, errOut, code = w.run("", nil, "hook", "bogus")
	if code != 1 || !strings.Contains(errOut, "unknown hook") {
		t.Errorf("unknown hook: exit %d, stderr %q", code, errOut)
	}
}
