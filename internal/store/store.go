// Package store is Loose Threads' local cache of JMAP Tasks: one SQLite
// database holding every thread as a Task and every project as a
// TaskList, grouped into collections, each of which may be synced with
// an account on a JMAP for Tasks server.  Every command reads and writes
// the cache and never waits on the network; sync is separate.  See the
// JMAP storage section of DESIGN.md.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rjbs/loosethreads/internal/patch"
	"github.com/rjbs/loosethreads/internal/thread"
)

// EnvRoot names the environment variable that overrides the store root.
const EnvRoot = "LOOSETHREADS_HOME"

const cacheFile = "cache.sqlite"

var ErrNotFound = errors.New("store: not found")

// Store is a handle on one store root: its config.yaml and its cache.
type Store struct {
	Root   string
	db     *sql.DB
	config *Config
}

// Project is a project: a TaskList carrying the project id, within one
// collection.
type Project struct {
	ID         string
	Name       string
	Color      string // a CSS color, or empty
	Collection string
}

// DisplayName returns the project's name, or its id when it has none.
func (p Project) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// DefaultRoot returns $LOOSETHREADS_HOME, or the XDG data directory
// default of ~/.local/share/loosethreads.
func DefaultRoot() (string, error) {
	if r := os.Getenv(EnvRoot); r != "" {
		return r, nil
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "loosethreads"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "loosethreads"), nil
}

// Open returns a Store for root, or for DefaultRoot when root is empty,
// creating the root and its cache if needed.
func Open(root string) (*Store, error) {
	if root == "" {
		var err error
		if root, err = DefaultRoot(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	s := &Store{Root: root}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	db, err := openDB(filepath.Join(root, cacheFile))
	if err != nil {
		return nil, err
	}
	s.db = db
	return s, nil
}

// Close closes the cache.
func (s *Store) Close() error { return s.db.Close() }

// projectLists returns the live TaskLists for a project id, in every
// collection, oldest first.  Normally there is one.  Two machines that
// start the same project offline each create a list for it; sync merges
// them, and until then they read as one project.
func projectLists(q queryer, id string) ([]*row, error) {
	return queryRows(q, `type = ? AND project = ? AND local IS NOT NULL ORDER BY key`, typeTaskList, id)
}

func projectOf(lists []*row) *Project {
	l := lists[0]
	p := &Project{Collection: l.collection}
	p.ID, _ = l.local[propProject].(string)
	if name, _ := l.local["name"].(string); name != p.ID {
		p.Name = name
	}
	p.Color, _ = l.local["color"].(string)
	return p
}

// LookupProject returns the project with the given id, or ErrNotFound.
func (s *Store) LookupProject(id string) (*Project, error) {
	lists, err := projectLists(s.db, id)
	if err != nil {
		return nil, err
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("%w: project %q", ErrNotFound, id)
	}
	return projectOf(lists), nil
}

// Project returns the project with the given id, creating its TaskList
// in the routed collection if it exists nowhere.
func (s *Store) Project(id string) (*Project, error) {
	if p, err := s.LookupProject(id); err == nil || !errors.Is(err, ErrNotFound) {
		return p, err
	}
	collection := s.config.Route(id)
	err := s.tx(func(tx *sql.Tx) error {
		_, err := newList(tx, collection, id, "", "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.LookupProject(id)
}

// ProjectIn returns the project with the given id, creating it in the
// named collection, with the given display name, if it exists nowhere.
// It is for importing; ordinary creation goes by the routing rules.
func (s *Store) ProjectIn(id, name, collection string) (*Project, error) {
	if p, err := s.LookupProject(id); err == nil || !errors.Is(err, ErrNotFound) {
		return p, err
	}
	err := s.tx(func(tx *sql.Tx) error {
		_, err := newList(tx, collection, id, name, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.LookupProject(id)
}

// newList creates a project's TaskList.  A name that is empty or just the
// id, or an empty color, is replaced by the default.
func newList(tx *sql.Tx, collection, id, name, color string) (*row, error) {
	if name == "" || name == id {
		name = DefaultName(id)
	}
	if color == "" {
		color = DefaultColor(id)
	}
	r := &row{collection: collection, typ: typeTaskList}
	return r, setLocal(tx, r, patch.Object{"name": name, "color": color, propProject: id})
}

// Projects lists every project, sorted by display name.
func (s *Store) Projects() ([]*Project, error) {
	lists, err := queryRows(s.db, `type = ? AND project IS NOT NULL AND local IS NOT NULL ORDER BY key`, typeTaskList)
	if err != nil {
		return nil, err
	}
	byID := map[string][]*row{}
	var ids []string
	for _, l := range lists {
		id := l.local[propProject].(string)
		if byID[id] == nil {
			ids = append(ids, id)
		}
		byID[id] = append(byID[id], l)
	}
	var ps []*Project
	for _, id := range ids {
		ps = append(ps, projectOf(byID[id]))
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].DisplayName() < ps[j].DisplayName() })
	return ps, nil
}

// taskRows returns the live Tasks in a project, in any of its lists.
func taskRows(q queryer, projectID string) ([]*row, error) {
	return queryRows(q, `type = ? AND local IS NOT NULL AND list_key IN
		(SELECT key FROM objects WHERE type = ? AND project = ? AND local IS NOT NULL)`,
		typeTask, typeTaskList, projectID)
}

// Threads lists every thread in p, oldest first by creation time, with id
// as the tiebreaker.
func (s *Store) Threads(p *Project) ([]*thread.Thread, error) {
	rows, err := taskRows(s.db, p.ID)
	if err != nil {
		return nil, err
	}
	threads := make([]*thread.Thread, 0, len(rows))
	for _, r := range rows {
		threads = append(threads, threadFromTask(r.local))
	}
	sort.Slice(threads, func(i, j int) bool {
		a, b := threads[i], threads[j]
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.ID < b.ID
	})
	return threads, nil
}

func taskRow(q queryer, p *Project, id string) (*row, error) {
	r, err := queryRow(q, `type = ? AND uid = ? AND local IS NOT NULL AND list_key IN
		(SELECT key FROM objects WHERE type = ? AND project = ? AND local IS NOT NULL)`,
		typeTask, id, typeTaskList, p.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: thread %q", ErrNotFound, id)
	}
	return r, err
}

// Get reads one thread by its exact id.
func (s *Store) Get(p *Project, id string) (*thread.Thread, error) {
	r, err := taskRow(s.db, p, id)
	if err != nil {
		return nil, err
	}
	return threadFromTask(r.local), nil
}

// Resolve finds a thread in p by exact id, or by a unique suffix of its
// id, so that the random characters suffice when they are distinct.
func (s *Store) Resolve(p *Project, ref string) (*thread.Thread, error) {
	if t, err := s.Get(p, ref); err == nil {
		return t, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	ths, err := s.Threads(p)
	if err != nil {
		return nil, err
	}
	var matches []*thread.Thread
	for _, t := range ths {
		if strings.HasSuffix(t.ID, ref) {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: no thread matches %q", ErrNotFound, ref)
	case 1:
		return matches[0], nil
	}
	var ids []string
	for _, t := range matches {
		ids = append(ids, t.ID)
	}
	return nil, fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(ids, ", "))
}

// primaryList returns the list new threads in a project go into: the
// oldest live one.
func primaryList(q queryer, p *Project) (*row, error) {
	lists, err := projectLists(q, p.ID)
	if err != nil {
		return nil, err
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("%w: project %q", ErrNotFound, p.ID)
	}
	return lists[0], nil
}

// Create assigns t a fresh id, unless it already has one, and records it
// in p.  It retries on the unlikely id collision.
func (s *Store) Create(p *Project, t *thread.Thread, now time.Time) error {
	if t.Created.IsZero() {
		t.Created = now
	}
	return s.tx(func(tx *sql.Tx) error {
		list, err := primaryList(tx, p)
		if err != nil {
			return err
		}
		if t.ID == "" {
			for range 10 {
				id := thread.NewID(now)
				if _, err := queryRow(tx, `uid = ? AND collection = ?`, id, list.collection); errors.Is(err, ErrNotFound) {
					t.ID = id
					break
				}
			}
			if t.ID == "" {
				return errors.New("store: could not find an unused thread id")
			}
		}
		task, err := applyThread(nil, t)
		if err != nil {
			return err
		}
		fileUnder(task, list)
		r := &row{collection: list.collection, typ: typeTask, listKey: list.key}
		return setLocal(tx, r, task)
	})
}

// Save records changes to t, which must already exist in p.
func (s *Store) Save(p *Project, t *thread.Thread) error {
	if t.ID == "" {
		return errors.New("store: cannot save a thread without an id")
	}
	return s.tx(func(tx *sql.Tx) error {
		r, err := taskRow(tx, p, t.ID)
		if err != nil {
			return err
		}
		task, err := applyThread(r.local, t)
		if err != nil {
			return err
		}
		return setLocal(tx, r, task)
	})
}

// Move relocates the thread with id from one project to another.
func (s *Store) Move(from, to *Project, id string) error {
	if from.ID == to.ID {
		return fmt.Errorf("store: thread %s is already in %s", id, to.ID)
	}
	return s.tx(func(tx *sql.Tx) error {
		r, err := taskRow(tx, from, id)
		if err != nil {
			return err
		}
		list, err := primaryList(tx, to)
		if err != nil {
			return err
		}
		if _, err := taskRow(tx, to, id); err == nil {
			return fmt.Errorf("store: thread %s already exists in %s", id, to.ID)
		}
		return moveTask(tx, r, list)
	})
}

// moveTask puts the Task in r into list.  Within one collection that is a
// change of taskListIds.  Between collections, which are different JMAP
// accounts, the Task is created anew in the list's collection and
// destroyed in its old one; its uid carries its identity across.
func moveTask(tx *sql.Tx, r *row, list *row) error {
	task := patch.Clone(r.local)
	fileUnder(task, list)
	if r.collection == list.collection {
		r.listKey = list.key
		return setLocal(tx, r, task)
	}
	delete(task, "id")
	moved := &row{collection: list.collection, typ: typeTask, listKey: list.key}
	if err := setLocal(tx, moved, task); err != nil {
		return err
	}
	return setLocal(tx, r, nil)
}

// MoveProject relocates a project into another collection.
func (s *Store) MoveProject(id, collection string) (*Project, error) {
	err := s.tx(func(tx *sql.Tx) error {
		lists, err := projectLists(tx, id)
		if err != nil {
			return err
		}
		if len(lists) == 0 {
			return fmt.Errorf("%w: project %q", ErrNotFound, id)
		}
		if lists[0].collection == collection {
			return nil
		}
		name, _ := lists[0].local["name"].(string)
		color, _ := lists[0].local["color"].(string)
		dest, err := newList(tx, collection, id, name, color)
		if err != nil {
			return err
		}
		return absorb(tx, lists, dest)
	})
	if err != nil {
		return nil, err
	}
	return s.LookupProject(id)
}

// absorb moves every Task in lists into dest and destroys the lists.
func absorb(tx *sql.Tx, lists []*row, dest *row) error {
	for _, l := range lists {
		if l.key == dest.key {
			continue
		}
		tasks, err := queryRows(tx, `type = ? AND list_key = ? AND local IS NOT NULL`, typeTask, l.key)
		if err != nil {
			return err
		}
		for _, t := range tasks {
			if err := moveTask(tx, t, dest); err != nil {
				return err
			}
		}
		if err := setLocal(tx, l, nil); err != nil {
			return err
		}
	}
	return nil
}

// Rehome moves every thread from the project fromID into the project
// toID, creating the destination if needed, and removes the source
// project.  It refuses to move anything if any thread id already exists
// in the destination.  It returns the ids moved.
func (s *Store) Rehome(fromID, toID string) ([]string, error) {
	if fromID == toID {
		return nil, fmt.Errorf("store: source and destination are the same project")
	}
	from, err := s.LookupProject(fromID)
	if err != nil {
		return nil, err
	}
	ths, err := s.Threads(from)
	if err != nil {
		return nil, err
	}
	if len(ths) == 0 {
		return nil, nil
	}
	to, err := s.Project(toID)
	if err != nil {
		return nil, err
	}
	var ids []string
	err = s.tx(func(tx *sql.Tx) error {
		for _, t := range ths {
			if _, err := taskRow(tx, to, t.ID); err == nil {
				return fmt.Errorf("store: thread %s already exists in %s; nothing moved", t.ID, toID)
			}
			ids = append(ids, t.ID)
		}
		lists, err := projectLists(tx, fromID)
		if err != nil {
			return err
		}
		dest, err := primaryList(tx, to)
		if err != nil {
			return err
		}
		return absorb(tx, lists, dest)
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// View is the JSON shape of a thread as seen by the CLI's --json output
// and the MCP server, so the two agree.
type View struct {
	ID      string     `json:"id"`
	Project string     `json:"project"`
	State   string     `json:"state"`
	Title   string     `json:"title"`
	Body    string     `json:"body"`
	Created time.Time  `json:"created"`
	Closed  *time.Time `json:"closed,omitempty"`
	Session string     `json:"session,omitempty"`
	Origin  string     `json:"origin,omitempty"`
}

// View builds the View of t within p.
func (s *Store) View(p *Project, t *thread.Thread) View {
	return View{
		ID:      t.ID,
		Project: p.ID,
		State:   string(t.State),
		Title:   t.Title(),
		Body:    t.Body,
		Created: t.Created,
		Closed:  t.Closed,
		Session: t.Session,
		Origin:  string(t.Origin),
	}
}
