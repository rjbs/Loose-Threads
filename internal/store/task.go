package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/rjbs/loosethreads/internal/patch"
	"github.com/rjbs/loosethreads/internal/thread"
)

// Vendor properties and keywords are named under the owning domain, as
// RFC 8984 asks.
const (
	vendorPrefix  = "lt.semiotic.systems:"
	propProject   = vendorPrefix + "project"
	propSession   = vendorPrefix + "session"
	originKeyword = vendorPrefix + "origin:"
)

const utcFormat = "2006-01-02T15:04:05Z"

func utc(t time.Time) string { return t.UTC().Format(utcFormat) }

func parseUTC(v any) (time.Time, bool) {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.Local(), true
}

// stateOf maps a JSCalendar progress value to a thread state.  Clients
// other than lt may use all five values; lt writes only three.
func stateOf(progress any) thread.State {
	switch progress {
	case "completed":
		return thread.Done
	case "cancelled", "failed":
		return thread.Abandoned
	}
	return thread.Open
}

var progressOf = map[thread.State]string{
	thread.Open:      "needs-action",
	thread.Done:      "completed",
	thread.Abandoned: "cancelled",
}

// splitBody separates a thread body into the Task's title and
// description: the first non-blank line, and everything after it with
// leading blank lines removed.
func splitBody(body string) (title, description string) {
	rest := body
	for rest != "" {
		line, after, _ := strings.Cut(rest, "\n")
		rest = after
		if strings.TrimSpace(line) != "" {
			title = strings.TrimSpace(line)
			break
		}
	}
	return title, strings.TrimLeft(rest, "\n")
}

func joinBody(title, description string) string {
	if description == "" {
		return title + "\n"
	}
	return title + "\n\n" + description
}

// threadFromTask builds the thread a Task represents.  A Task created by
// another client may lack everything lt adds; such properties read as
// unknown.
func threadFromTask(task patch.Object) *thread.Thread {
	title, _ := task["title"].(string)
	desc, _ := task["description"].(string)
	t := &thread.Thread{
		State: stateOf(task["progress"]),
		Body:  joinBody(title, desc),
	}
	t.ID, _ = task["uid"].(string)
	t.Session, _ = task[propSession].(string)
	if c, ok := parseUTC(task["created"]); ok {
		t.Created = c
	} else if u, ok := parseUTC(task["updated"]); ok {
		t.Created = u
	}
	if t.State != thread.Open {
		if c, ok := parseUTC(task["progressUpdated"]); ok {
			t.Closed = &c
		}
	}
	if kw, ok := task["keywords"].(patch.Object); ok {
		for k := range kw {
			if o, ok := strings.CutPrefix(k, originKeyword); ok {
				t.Origin = thread.Origin(o)
			}
		}
	}
	return t
}

// applyThread returns a copy of task with the properties lt owns set from
// t, leaving every other property, including ones lt has never heard of,
// as it was.
func applyThread(task patch.Object, t *thread.Thread) (patch.Object, error) {
	out := patch.Clone(task)
	if out == nil {
		out = patch.Object{"@type": "Task"}
	}
	title, desc := splitBody(t.Body)

	if old, _ := out["description"].(string); old != desc {
		if ct, ok := out["descriptionContentType"].(string); ok && ct != "" && ct != "text/plain" {
			return nil, fmt.Errorf("thread %s has a %s description, which lt will not rewrite", t.ID, ct)
		}
	}
	out["uid"] = t.ID
	out["title"] = title
	out["description"] = desc
	if !t.Created.IsZero() {
		out["created"] = utc(t.Created)
	}

	if stateOf(out["progress"]) != t.State || out["progress"] == nil {
		out["progress"] = progressOf[t.State]
	}
	if t.Closed != nil {
		out["progressUpdated"] = utc(*t.Closed)
	} else if t.State == thread.Open {
		delete(out, "progressUpdated")
	}

	if t.Session != "" {
		out[propSession] = t.Session
	} else {
		delete(out, propSession)
	}

	kw, _ := out["keywords"].(patch.Object)
	for k := range kw {
		if strings.HasPrefix(k, originKeyword) {
			delete(kw, k)
		}
	}
	if t.Origin != "" {
		if kw == nil {
			kw = patch.Object{}
		}
		kw[originKeyword+string(t.Origin)] = true
	}
	if len(kw) > 0 {
		out["keywords"] = kw
	} else {
		delete(out, "keywords")
	}
	return out, nil
}
