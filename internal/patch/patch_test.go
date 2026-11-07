package patch

import (
	"encoding/json"
	"reflect"
	"testing"
)

func obj(t *testing.T, s string) Object {
	t.Helper()
	var o Object
	if err := json.Unmarshal([]byte(s), &o); err != nil {
		t.Fatalf("bad test JSON %s: %v", s, err)
	}
	return o
}

func checkDiff(t *testing.T, from, to, want string) {
	t.Helper()
	got := Diff(obj(t, from), obj(t, to))
	if !reflect.DeepEqual(got, obj(t, want)) {
		g, _ := json.Marshal(got)
		t.Errorf("Diff(%s, %s) = %s, want %s", from, to, g, want)
	}
	if applied := Apply(obj(t, from), got); !Equal(applied, obj(t, to)) {
		a, _ := json.Marshal(applied)
		t.Errorf("Apply(%s, Diff) = %s, want %s", from, a, to)
	}
}

func TestDiff(t *testing.T) {
	checkDiff(t, `{"a":1}`, `{"a":1}`, `{}`)
	checkDiff(t, `{"a":1}`, `{"a":2}`, `{"a":2}`)
	checkDiff(t, `{"a":1}`, `{}`, `{"a":null}`)
	checkDiff(t, `{"a":null}`, `{}`, `{}`)
	checkDiff(t, `{}`, `{"a":null}`, `{}`)
	checkDiff(t, `{"k":{"x":true}}`, `{"k":{"x":true,"y":true}}`, `{"k/y":true}`)
	checkDiff(t, `{"k":{"x":true}}`, `{"k":{}}`, `{"k/x":null}`)
	checkDiff(t, `{"k":null}`, `{"k":{"y":true}}`, `{"k":{"y":true}}`)
	checkDiff(t, `{"l":[1,2]}`, `{"l":[1,3]}`, `{"l":[1,3]}`)
	checkDiff(t, `{"a/b~c":1}`, `{"a/b~c":2}`, `{"a~1b~0c":2}`)
}

func checkRebase(t *testing.T, base, local, fetched, want string, wantConflicts ...string) {
	t.Helper()
	got, conflicts := Rebase(obj(t, base), obj(t, local), obj(t, fetched),
		map[string]Resolver{"description": AppendResolver})
	if !Equal(got, obj(t, want)) {
		g, _ := json.Marshal(got)
		t.Errorf("Rebase = %s, want %s", g, want)
	}
	if len(conflicts) != len(wantConflicts) || (len(conflicts) > 0 && !reflect.DeepEqual(conflicts, wantConflicts)) {
		t.Errorf("conflicts = %v, want %v", conflicts, wantConflicts)
	}
}

func TestRebase(t *testing.T) {
	// Independent properties merge.
	checkRebase(t,
		`{"title":"t","progress":"needs-action"}`,
		`{"title":"t","progress":"completed"}`,
		`{"title":"T2","progress":"needs-action"}`,
		`{"title":"T2","progress":"completed"}`)

	// The local side wins a property both changed.
	checkRebase(t,
		`{"title":"t"}`,
		`{"title":"local"}`,
		`{"title":"server"}`,
		`{"title":"local"}`)

	// Foreign properties the server gained survive.
	checkRebase(t,
		`{"title":"t"}`,
		`{"title":"u"}`,
		`{"title":"t","priority":3}`,
		`{"title":"u","priority":3}`)

	// Keywords added on each side both survive.
	checkRebase(t,
		`{"keywords":{"a":true}}`,
		`{"keywords":{"a":true,"b":true}}`,
		`{"keywords":{"a":true,"c":true}}`,
		`{"keywords":{"a":true,"b":true,"c":true}}`)
}

func TestRebaseDescription(t *testing.T) {
	// Appends on both sides: the server's note, then ours.
	checkRebase(t,
		`{"description":"body\n"}`,
		`{"description":"body\n\n**2026-11-06 14:07 (agent):**\nBar\n"}`,
		`{"description":"body\n\n**2026-11-06 14:05 (agent):**\nFoo\n"}`,
		`{"description":"body\n\n**2026-11-06 14:05 (agent):**\nFoo\n\n**2026-11-06 14:07 (agent):**\nBar\n"}`)

	// An append survives a server-side rewrite of the body.
	checkRebase(t,
		`{"description":"body\n"}`,
		`{"description":"body\n\nnote\n"}`,
		`{"description":"new body\n"}`,
		`{"description":"new body\n\nnote\n"}`)

	// Appends to an empty description keep a blank line between notes.
	checkRebase(t,
		`{"description":""}`,
		`{"description":"**B**\nbar\n"}`,
		`{"description":"**A**\nfoo\n"}`,
		`{"description":"**A**\nfoo\n\n**B**\nbar\n"}`)

	// The server did not change it: plain local win.
	checkRebase(t,
		`{"description":"body\n"}`,
		`{"description":"rewritten\n"}`,
		`{"description":"body\n","title":"x"}`,
		`{"description":"rewritten\n","title":"x"}`)

	// Both really edited: local wins, and it is a conflict.
	checkRebase(t,
		`{"description":"body\n"}`,
		`{"description":"rewritten\n"}`,
		`{"description":"body\n\nnote\n"}`,
		`{"description":"rewritten\n"}`,
		"description")
}
