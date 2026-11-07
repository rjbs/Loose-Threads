package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rjbs/loosethreads/internal/jmaptest"
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

// checkHookLacks runs "lt hook NAME" with the given stdin JSON and asserts
// that the additionalContext text does not contain unwanted.
func (w *world) checkHookLacks(name, hook, stdin, unwanted string) {
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
	if strings.Contains(got.HookSpecificOutput.AdditionalContext, unwanted) {
		w.t.Errorf("%s: context contains %q:\n%s", name, unwanted, got.HookSpecificOutput.AdditionalContext)
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

func TestHookSyncProblems(t *testing.T) {
	w := newWorld(t)
	srv := jmaptest.New("personal")
	defer srv.Close()
	srv.Token = "required"
	os.MkdirAll(w.store, 0o755)
	writeConfig := func(token string) {
		os.WriteFile(filepath.Join(w.store, "config.yaml"), []byte(fmt.Sprintf(
			"server: %s\ntoken: %q\ncollections:\n  personal: { account: personal }\nroutes:\n  - { match: \"*\", collection: personal }\n",
			srv.SessionURL(), token)), 0o600)
	}
	input := `{"session_id":"s1","cwd":"` + w.cwd + `","hook_event_name":"SessionStart","source":"startup"}`

	writeConfig("")
	w.check("add", 0, idPat, "add", "Thing")
	w.check("sync fails", 1, `personal: .*401`, "sync")
	if out, _, code := w.run("", nil, "sync", "-status"); code != 1 || !strings.Contains(out, "the last sync, at") {
		t.Errorf("sync -status after a failure: exit %d\n%s", code, out)
	}
	w.checkHook("problems reported", "session-start", input, "SessionStart",
		`(?s)Sync is not working.*personal: the last sync, at .* failed: .*401`)

	writeConfig("required")
	w.check("sync works", 0, `personal: sent`, "sync")
	w.check("status is clean", 0, `^personal: last sync .*, 0 waiting, 0 refused\n$`, "sync", "-status")
	w.checkHookLacks("no problems", "session-start", input, "Sync is not working")
}
