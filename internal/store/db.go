package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/rjbs/loosethreads/internal/patch"
)

// The cache holds one row per JMAP object, following the model in
// DESIGN.md: the last server version (null if the server has never seen
// it), the local value (null if destroyed locally), and a pending
// sequence number, nonzero while the local value differs from the server
// version or the object is yet to be created or destroyed.  uid, project,
// and list_key are copied out of the JSON for lookups.
const schema = `
CREATE TABLE IF NOT EXISTS objects (
	key        INTEGER PRIMARY KEY,
	collection TEXT NOT NULL,
	type       TEXT NOT NULL,
	jmap_id    TEXT,
	server     TEXT,
	local      TEXT,
	pending    INTEGER NOT NULL DEFAULT 0,
	uid        TEXT,
	project    TEXT,
	list_key   INTEGER,
	error      TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS objects_jmap_id ON objects (collection, type, jmap_id) WHERE jmap_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS objects_uid ON objects (collection, uid) WHERE uid IS NOT NULL;
CREATE INDEX IF NOT EXISTS objects_project ON objects (project) WHERE project IS NOT NULL;
CREATE INDEX IF NOT EXISTS objects_list ON objects (list_key);
CREATE INDEX IF NOT EXISTS objects_pending ON objects (collection, pending) WHERE pending > 0;

CREATE TABLE IF NOT EXISTS states (
	collection TEXT NOT NULL,
	type       TEXT NOT NULL,
	state      TEXT NOT NULL,
	PRIMARY KEY (collection, type)
);

CREATE TABLE IF NOT EXISTS counters (
	name  TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
INSERT OR IGNORE INTO counters VALUES ('seq', 0), ('generation', 0);
`

const (
	typeTask     = "Task"
	typeTaskList = "TaskList"
)

func openDB(path string) (*sql.DB, error) {
	// _txlock=immediate takes the write lock at BEGIN, so two lt processes
	// never both read, then both try to upgrade and one fails with
	// SQLITE_BUSY in the middle of a transaction.  -- claude, 2026-11-06
	dsn := "file:" + path +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: initializing %s: %w", path, err)
	}
	return db, nil
}

// row is one object in the cache.
type row struct {
	key        int64
	collection string
	typ        string
	jmapID     string
	server     patch.Object
	local      patch.Object
	pending    int64
	listKey    int64
	err        string
}

const rowColumns = `key, collection, type, jmap_id, server, local, pending, list_key, error`

type scanner interface{ Scan(...any) error }

func scanRow(sc scanner) (*row, error) {
	var (
		r                   row
		jmapID, server, loc sql.NullString
		listKey             sql.NullInt64
		errText             sql.NullString
	)
	if err := sc.Scan(&r.key, &r.collection, &r.typ, &jmapID, &server, &loc, &r.pending, &listKey, &errText); err != nil {
		return nil, err
	}
	r.jmapID, r.listKey, r.err = jmapID.String, listKey.Int64, errText.String
	var err error
	if r.server, err = decodeObject(server); err != nil {
		return nil, err
	}
	if r.local, err = decodeObject(loc); err != nil {
		return nil, err
	}
	return &r, nil
}

func decodeObject(s sql.NullString) (patch.Object, error) {
	if !s.Valid {
		return nil, nil
	}
	var o patch.Object
	if err := json.Unmarshal([]byte(s.String), &o); err != nil {
		return nil, fmt.Errorf("store: corrupt object in cache: %w", err)
	}
	return o, nil
}

func encodeObject(o patch.Object) (any, error) {
	if o == nil {
		return nil, nil
	}
	b, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func queryRows(q queryer, where string, args ...any) ([]*row, error) {
	rs, err := q.Query(`SELECT `+rowColumns+` FROM objects WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []*row
	for rs.Next() {
		r, err := scanRow(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func queryRow(q queryer, where string, args ...any) (*row, error) {
	r, err := scanRow(q.QueryRow(`SELECT `+rowColumns+` FROM objects WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// tx runs f in one write transaction and bumps the generation counter,
// which is how the browser notices that something changed.
func (s *Store) tx(f func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`UPDATE counters SET value = value + 1 WHERE name = 'generation'`); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func nextSeq(tx *sql.Tx) (int64, error) {
	var n int64
	err := tx.QueryRow(`UPDATE counters SET value = value + 1 WHERE name = 'seq' RETURNING value`).Scan(&n)
	return n, err
}

// Generation returns a number that changes whenever anything in the cache
// does.
func (s *Store) Generation() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT value FROM counters WHERE name = 'generation'`).Scan(&n)
	return n, err
}

// derived returns the lookup columns for an object: its uid (Tasks) or
// project id (TaskLists), taken from the local value, or from the server
// version when destroyed locally.
func derived(typ string, local, server patch.Object) (uid, project any) {
	o := local
	if o == nil {
		o = server
	}
	switch typ {
	case typeTask:
		if s, ok := o["uid"].(string); ok && s != "" {
			uid = s
		}
	case typeTaskList:
		if s, ok := o[propProject].(string); ok && s != "" {
			project = s
		}
	}
	return uid, project
}

// put writes r back, all columns.
func put(tx *sql.Tx, r *row) error {
	server, err := encodeObject(r.server)
	if err != nil {
		return err
	}
	local, err := encodeObject(r.local)
	if err != nil {
		return err
	}
	uid, project := derived(r.typ, r.local, r.server)
	var jmapID, listKey, errText any
	if r.jmapID != "" {
		jmapID = r.jmapID
	}
	if r.listKey != 0 {
		listKey = r.listKey
	}
	if r.err != "" {
		errText = r.err
	}
	if r.key == 0 {
		return tx.QueryRow(`INSERT INTO objects
			(collection, type, jmap_id, server, local, pending, uid, project, list_key, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING key`,
			r.collection, r.typ, jmapID, server, local, r.pending, uid, project, listKey, errText,
		).Scan(&r.key)
	}
	_, err = tx.Exec(`UPDATE objects SET collection = ?, type = ?, jmap_id = ?, server = ?,
		local = ?, pending = ?, uid = ?, project = ?, list_key = ?, error = ? WHERE key = ?`,
		r.collection, r.typ, jmapID, server, local, r.pending, uid, project, listKey, errText, r.key)
	return err
}

func remove(tx *sql.Tx, key int64) error {
	_, err := tx.Exec(`DELETE FROM objects WHERE key = ?`, key)
	return err
}

// setLocal records a local change to r: a new local value, or nil to
// destroy it.  An object the server never saw is simply dropped when
// destroyed.  A change that makes the local value equal to the server
// version clears the pending mark, since there is nothing left to send.
func setLocal(tx *sql.Tx, r *row, local patch.Object) error {
	if local == nil && r.server == nil {
		if r.key == 0 {
			return nil
		}
		return remove(tx, r.key)
	}
	if r.key != 0 && local != nil && r.local != nil && patch.Equal(r.local, local) {
		return nil
	}
	r.local = local
	if r.server != nil && local != nil && patch.Equal(r.server, local) {
		r.pending = 0
	} else {
		seq, err := nextSeq(tx)
		if err != nil {
			return err
		}
		r.pending = seq
	}
	return put(tx, r)
}

// listRef is how a Task's taskListId names the list with the given row:
// the list's JMAP id once it has one, and until then a creation id made
// from its local key, rewritten when the list is created on the server.
func listRef(list *row) string {
	if list.jmapID != "" {
		return list.jmapID
	}
	return creationRef(list.key)
}

func creationRef(key int64) string { return fmt.Sprintf("#k%d", key) }

func isCreationRef(s string) bool { return strings.HasPrefix(s, "#k") }
