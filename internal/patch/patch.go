// Package patch computes and applies JMAP PatchObjects between JSON
// objects, and rebases a pending local change onto a newly fetched server
// version.  It is the pure core of the sync model described in DESIGN.md,
// after Jinx's: no I/O, no knowledge of Tasks beyond the description
// resolver.
package patch

import (
	"reflect"
	"sort"
	"strings"
)

// Object is a JSON object as decoded by encoding/json into any.
type Object = map[string]any

// Patch is a JMAP PatchObject: JSON Pointer paths (without the leading
// slash) mapped to new values, with nil meaning "remove".
type Patch = map[string]any

// Diff returns the patch that turns from into to.  It recurses into
// properties that are objects on both sides, so that a change to one
// entry of a map such as keywords does not overwrite entries another
// client added, and never into arrays.  A property that is absent and one
// that is null are treated as the same.
func Diff(from, to Object) Patch {
	p := Patch{}
	diffInto(p, "", from, to)
	return p
}

func diffInto(p Patch, prefix string, from, to Object) {
	for k, tv := range to {
		path := prefix + escape(k)
		fv, ok := from[k]
		if tv == nil {
			if ok && fv != nil {
				p[path] = nil
			}
			continue
		}
		fm, fIsObj := fv.(Object)
		tm, tIsObj := tv.(Object)
		if fIsObj && tIsObj {
			diffInto(p, path+"/", fm, tm)
			continue
		}
		if !ok || !reflect.DeepEqual(fv, tv) {
			p[path] = tv
		}
	}
	for k, fv := range from {
		if _, ok := to[k]; !ok && fv != nil {
			p[prefix+escape(k)] = nil
		}
	}
}

// Apply returns a copy of obj with p applied.  Intermediate objects
// missing along a path are created, which a server would refuse but which
// is the useful behavior when replaying a local change.
func Apply(obj Object, p Patch) Object {
	out := Clone(obj)
	paths := make([]string, 0, len(p))
	for path := range p {
		paths = append(paths, path)
	}
	// Shorter paths first, so that a patch replacing a whole object and
	// then one of its entries applies in a sensible order.
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) < len(paths[j]) })
	for _, path := range paths {
		setPath(out, splitPath(path), p[path])
	}
	return out
}

func setPath(obj Object, keys []string, v any) {
	for len(keys) > 1 {
		next, ok := obj[keys[0]].(Object)
		if !ok {
			if v == nil {
				return
			}
			next = Object{}
			obj[keys[0]] = next
		}
		obj, keys = next, keys[1:]
	}
	if v == nil {
		delete(obj, keys[0])
		return
	}
	obj[keys[0]] = cloneValue(v)
}

// Equal reports whether two objects are the same, treating absent and
// null properties alike.
func Equal(a, b Object) bool { return len(Diff(a, b)) == 0 }

// Clone returns a deep copy of obj.
func Clone(obj Object) Object {
	if obj == nil {
		return nil
	}
	return cloneValue(obj).(Object)
}

func cloneValue(v any) any {
	switch v := v.(type) {
	case Object:
		out := make(Object, len(v))
		for k, e := range v {
			out[k] = cloneValue(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneValue(e)
		}
		return out
	}
	return v
}

func escape(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

func splitPath(path string) []string {
	keys := strings.Split(path, "/")
	for i, k := range keys {
		keys[i] = strings.ReplaceAll(strings.ReplaceAll(k, "~1", "/"), "~0", "~")
	}
	return keys
}

// Resolver merges one top-level string property when the local change and
// the server both changed it since base.  It returns the merged value and
// whether the change was a conflict it could not merge.
type Resolver func(base, server, local string) (merged string, conflict bool)

// Rebase replays the local change (from base to local) onto fetched, the
// object as the server now has it.  A property changed locally wins
// unless a resolver is registered for it, in which case the resolver
// decides; every other property takes the server's value.  conflicts
// names the properties whose resolvers reported a conflict.
func Rebase(base, local, fetched Object, resolvers map[string]Resolver) (rebased Object, conflicts []string) {
	p := Diff(base, local)
	for prop, resolve := range resolvers {
		lv, ok := p[prop]
		if !ok {
			continue
		}
		bs, _ := base[prop].(string)
		ss, _ := fetched[prop].(string)
		if bs == ss {
			continue
		}
		ls, _ := lv.(string)
		merged, conflict := resolve(bs, ss, ls)
		if conflict {
			conflicts = append(conflicts, prop)
		}
		p[prop] = merged
	}
	return Apply(fetched, p), conflicts
}

// AppendResolver merges a text property to which notes are appended.  If
// the local value is the base plus a tail, the tail is appended to the
// server's value, so appends on two machines both survive.  Otherwise the
// local side really edited the text, and its value wins as a conflict.
//
// The prefix is taken against the base, never against the server's value:
// two notes written the same day begin with the same characters, so a
// common prefix of server and local values runs into the notes themselves
// and would cut the local one in half.  -- claude, 2026-11-06
func AppendResolver(base, server, local string) (string, bool) {
	if server == base {
		return local, false
	}
	if tail, ok := strings.CutPrefix(local, base); ok {
		// Appending to an empty description writes the paragraph with no
		// blank line before it, so one is needed when the server's value is
		// no longer empty.
		if base == "" && !strings.HasPrefix(tail, "\n") {
			server = strings.TrimRight(server, "\n") + "\n\n"
		}
		return server + tail, false
	}
	return local, true
}
