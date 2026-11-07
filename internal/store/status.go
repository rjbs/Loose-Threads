package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Syncs run in the background, so nobody sees their errors as they
// happen.  Each one records how it went, and SyncStatus reports it, for
// the session-start hook, the browser, and "lt sync -status" to show.

// StaleAfter is how long changes may wait to be sent, with no failed
// sync to explain why, before SyncStatus.Problems mentions them.  Every
// write starts a sync, so waiting this long means syncs are not running.
const StaleAfter = time.Hour

// timeLayout is how sync times are stored.
const timeLayout = time.RFC3339

// SyncStatus is how syncing one collection has gone.
type SyncStatus struct {
	Collection string
	Attempted  time.Time // the last sync's start; zero if there has been none
	Succeeded  time.Time // the last successful sync's start; zero if none
	Error      string    // why the last sync failed, or empty
	Pending    int       // changes not yet on the server, not counting Rejected
	Rejected   []string  // changes the server refused, each with the reason
}

// recordSync notes a sync of collection that started at start, failed
// with err unless err is nil.
func (s *Store) recordSync(collection string, start time.Time, err error) error {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	at := start.Format(timeLayout)
	return s.tx(func(tx *sql.Tx) error {
		_, e := tx.Exec(`INSERT INTO sync_status (collection, attempted, succeeded, error)
			VALUES (?, ?, CASE WHEN ? = '' THEN ? END, ?)
			ON CONFLICT (collection) DO UPDATE SET
				attempted = excluded.attempted,
				succeeded = coalesce(excluded.succeeded, succeeded),
				error = excluded.error`,
			collection, at, msg, at, msg)
		return e
	})
}

// SyncStatuses reports on every collection that syncs.
func (s *Store) SyncStatuses() ([]SyncStatus, error) {
	var out []SyncStatus
	for _, name := range s.config.Synced() {
		st, err := s.SyncStatus(name)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// SyncStatus reports on one collection.
func (s *Store) SyncStatus(collection string) (SyncStatus, error) {
	st := SyncStatus{Collection: collection}
	var attempted, succeeded sql.NullString
	err := s.db.QueryRow(`SELECT attempted, succeeded, error FROM sync_status WHERE collection = ?`, collection).
		Scan(&attempted, &succeeded, &st.Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	st.Attempted, _ = time.Parse(timeLayout, attempted.String)
	st.Succeeded, _ = time.Parse(timeLayout, succeeded.String)

	rows, err := queryRows(s.db, `collection = ? AND pending > 0`, collection)
	if err != nil {
		return st, err
	}
	for _, r := range rows {
		if r.err != "" {
			st.Rejected = append(st.Rejected, fmt.Sprintf("%s %s: %s", r.typ, describe(r), r.err))
		} else {
			st.Pending++
		}
	}
	return st, nil
}

// Problems describes whatever is wrong, as of now, one line each: the
// last sync failed, the server refused some changes, or changes have
// waited long enough that syncs must not be running.  It is empty when
// all is well.
func (st SyncStatus) Problems(now time.Time) []string {
	var out []string
	switch {
	case st.Error != "":
		out = append(out, fmt.Sprintf("%s: the last sync, at %s, failed: %s.  %s; %s.",
			st.Collection, when(st.Attempted), st.Error, st.lastSuccess(), waiting(st.Pending)))
	case st.Pending > 0 && st.Attempted.IsZero():
		out = append(out, fmt.Sprintf("%s: never synced; %s.", st.Collection, waiting(st.Pending)))
	case st.Pending > 0 && now.Sub(st.Attempted) > StaleAfter:
		out = append(out, fmt.Sprintf("%s: no sync since %s; %s.", st.Collection, when(st.Attempted), waiting(st.Pending)))
	}
	const show = 3
	for i, r := range st.Rejected {
		if i == show {
			out = append(out, fmt.Sprintf("%s: and %d more refused by the server", st.Collection, len(st.Rejected)-show))
			break
		}
		out = append(out, fmt.Sprintf("%s: refused by the server: %s", st.Collection, r))
	}
	return out
}

func (st SyncStatus) lastSuccess() string {
	if st.Succeeded.IsZero() {
		return "It has never synced successfully"
	}
	return "The last success was at " + when(st.Succeeded)
}

func waiting(n int) string {
	switch n {
	case 0:
		return "nothing is waiting to be sent"
	case 1:
		return "1 change is waiting to be sent"
	}
	return fmt.Sprintf("%d changes are waiting to be sent", n)
}

func when(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
