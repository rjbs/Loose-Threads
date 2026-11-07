// Package jmaptest is a small in-memory JMAP for Tasks server for tests:
// enough of Task and TaskList to exercise lt's sync engine, with change
// tracking, uid queries, patches, and a few injectable faults.  It is not
// a validating server; it stores what it is given.
package jmaptest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/rjbs/loosethreads/internal/patch"
)

// Server is a running test server.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	accounts map[string]*account

	// ForgetChanges makes every /changes call fail with
	// cannotCalculateChanges.
	ForgetChanges bool
}

// The limits the server advertises and enforces are small, so that tests
// with a handful of objects exercise a client's chunking.
const (
	MaxCallsInRequest = 4
	MaxObjectsInGet   = 3
)

type account struct {
	types map[string]*store
}

type store struct {
	state   int
	objects map[string]*object
	gone    map[string]int // destroyed id -> state at destruction
	nextID  int
}

type object struct {
	value    patch.Object
	modified int
	created  int
}

// New starts a server with the given account ids.
func New(accounts ...string) *Server {
	s := &Server{accounts: map[string]*account{}}
	for _, a := range accounts {
		s.accounts[a] = &account{types: map[string]*store{
			"Task":     {objects: map[string]*object{}, gone: map[string]int{}},
			"TaskList": {objects: map[string]*object{}, gone: map[string]int{}},
		}}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /session", s.session)
	mux.HandleFunc("POST /api", s.api)
	s.Server = httptest.NewServer(mux)
	return s
}

// SessionURL is the URL of the Session resource.
func (s *Server) SessionURL() string { return s.URL + "/session" }

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	accts := map[string]any{}
	for id := range s.accounts {
		accts[id] = map[string]any{"name": id, "isPersonal": true, "isReadOnly": false}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"apiUrl":   "/api",
		"accounts": accts,
		"capabilities": map[string]any{
			"urn:ietf:params:jmap:core": map[string]any{
				"maxCallsInRequest": MaxCallsInRequest,
				"maxObjectsInGet":   MaxObjectsInGet,
				"maxObjectsInSet":   1000,
			},
		},
	})
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MethodCalls [][3]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.MethodCalls) > MaxCallsInRequest {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:jmap:error:limit", "limit": "maxCallsInRequest"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][3]any
	for _, c := range req.MethodCalls {
		var name, id string
		var args map[string]any
		json.Unmarshal(c[0], &name)
		json.Unmarshal(c[1], &args)
		json.Unmarshal(c[2], &id)
		rname, rargs := s.call(name, args)
		out = append(out, [3]any{rname, rargs, id})
	}
	json.NewEncoder(w).Encode(map[string]any{"methodResponses": out})
}

func methodError(typ string) (string, map[string]any) {
	return "error", map[string]any{"type": typ}
}

func (s *Server) call(name string, args map[string]any) (string, map[string]any) {
	typ, method, _ := strings.Cut(name, "/")
	acctID, _ := args["accountId"].(string)
	acct := s.accounts[acctID]
	if acct == nil {
		return methodError("accountNotFound")
	}
	st := acct.types[typ]
	if st == nil {
		return methodError("unknownMethod")
	}
	switch method {
	case "get":
		if ids, ok := args["ids"].([]any); (ok && len(ids) > MaxObjectsInGet) || (args["ids"] == nil && typ == "Task" && len(st.objects) > MaxObjectsInGet) {
			return methodError("requestTooLarge")
		}
		return name, st.get(acctID, args)
	case "set":
		return name, st.set(acctID, typ, args)
	case "changes":
		if s.ForgetChanges {
			return methodError("cannotCalculateChanges")
		}
		if n, a := st.changes(acctID, args); n == "error" {
			return n, a
		} else {
			return name, a
		}
	case "query":
		return name, st.query(acctID, args)
	}
	return methodError("unknownMethod")
}

func (st *store) stateString() string { return strconv.Itoa(st.state) }

func (st *store) get(acct string, args map[string]any) map[string]any {
	list := []any{}
	var notFound []any
	if args["ids"] == nil {
		ids := make([]string, 0, len(st.objects))
		for id := range st.objects {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			list = append(list, patch.Clone(st.objects[id].value))
		}
	} else {
		for _, v := range args["ids"].([]any) {
			id := v.(string)
			if o, ok := st.objects[id]; ok {
				list = append(list, patch.Clone(o.value))
			} else {
				notFound = append(notFound, id)
			}
		}
	}
	return map[string]any{"accountId": acct, "list": list, "notFound": notFound, "state": st.stateString()}
}

func (st *store) set(acct, typ string, args map[string]any) map[string]any {
	old := st.stateString()
	created, notCreated := map[string]any{}, map[string]any{}
	updated, notUpdated := map[string]any{}, map[string]any{}
	destroyed, notDestroyed := []any{}, map[string]any{}

	creates, _ := args["create"].(map[string]any)
	cids := make([]string, 0, len(creates))
	for cid := range creates {
		cids = append(cids, cid)
	}
	sort.Strings(cids)
	for _, cid := range cids {
		v := patch.Clone(creates[cid].(map[string]any))
		if typ == "Task" {
			if lists, _ := v["taskListIds"].(map[string]any); len(lists) == 0 {
				notCreated[cid] = map[string]any{"type": "invalidProperties", "properties": []string{"taskListIds"}}
				continue
			}
		}
		st.state++
		st.nextID++
		id := fmt.Sprintf("%s-%d", typ, st.nextID)
		v["id"] = id
		st.objects[id] = &object{value: v, modified: st.state, created: st.state}
		created[cid] = map[string]any{"id": id}
	}

	updates, _ := args["update"].(map[string]any)
	for id, p := range updates {
		o, ok := st.objects[id]
		if !ok {
			notUpdated[id] = map[string]any{"type": "notFound"}
			continue
		}
		st.state++
		o.value = patch.Apply(o.value, p.(map[string]any))
		o.modified = st.state
		updated[id] = nil
	}

	ids, _ := args["destroy"].([]any)
	for _, v := range ids {
		id := v.(string)
		if _, ok := st.objects[id]; !ok {
			notDestroyed[id] = map[string]any{"type": "notFound"}
			continue
		}
		st.state++
		delete(st.objects, id)
		st.gone[id] = st.state
		destroyed = append(destroyed, id)
	}

	return map[string]any{
		"accountId": acct, "oldState": old, "newState": st.stateString(),
		"created": created, "notCreated": notCreated,
		"updated": updated, "notUpdated": notUpdated,
		"destroyed": destroyed, "notDestroyed": notDestroyed,
	}
}

func (st *store) changes(acct string, args map[string]any) (string, map[string]any) {
	since, err := strconv.Atoi(fmt.Sprint(args["sinceState"]))
	if err != nil || since > st.state {
		return methodError("cannotCalculateChanges")
	}
	created, updated, destroyed := []any{}, []any{}, []any{}
	for id, o := range st.objects {
		switch {
		case o.created > since:
			created = append(created, id)
		case o.modified > since:
			updated = append(updated, id)
		}
	}
	for id, at := range st.gone {
		if at > since {
			destroyed = append(destroyed, id)
		}
	}
	return "changes", map[string]any{
		"accountId": acct, "oldState": strconv.Itoa(since), "newState": st.stateString(),
		"hasMoreChanges": false, "created": created, "updated": updated, "destroyed": destroyed,
	}
}

func (st *store) query(acct string, args map[string]any) map[string]any {
	filter, _ := args["filter"].(map[string]any)
	ids := []any{}
	var all []string
	for id := range st.objects {
		all = append(all, id)
	}
	sort.Strings(all)
	for _, id := range all {
		if uid, ok := filter["uid"]; ok && st.objects[id].value["uid"] != uid {
			continue
		}
		ids = append(ids, id)
	}
	pos, _ := args["position"].(float64)
	ids = ids[min(int(pos), len(ids)):]
	if limit, ok := args["limit"].(float64); ok && int(limit) < len(ids) {
		ids = ids[:int(limit)]
	}
	return map[string]any{"accountId": acct, "ids": ids, "queryState": st.stateString()}
}

// Objects returns copies of every object of a type in an account, sorted
// by id, for assertions.
func (s *Server) Objects(acct, typ string) []patch.Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.accounts[acct].types[typ]
	out := st.get(acct, map[string]any{"ids": nil})["list"].([]any)
	objs := make([]patch.Object, len(out))
	for i, o := range out {
		objs[i] = o.(patch.Object)
	}
	return objs
}

// Update applies a patch to an object directly, as another client would.
func (s *Server) Update(acct, typ, id string, p patch.Patch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.accounts[acct].types[typ]
	st.set(acct, typ, map[string]any{"update": map[string]any{id: map[string]any(p)}})
}
