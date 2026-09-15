// Package sqlite persists snapshots using SQLite. Requires CGO and a C compiler.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"

	"github.com/longhopefor/goscope/session"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct{ db *sql.DB }

// Open accepts a filesystem path, not an arbitrary SQLite DSN. Caller owns Close.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("database path required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: abs}
	q := uri.Query()
	q.Set("_busy_timeout", "5000")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "FULL")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var tx *sql.Tx
	fail := func(err error) (*Store, error) {
		if tx != nil {
			tx.Rollback()
		}
		db.Close()
		return nil, err
	}
	tx, err = db.Begin()
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS goscope_session_schema (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL)`); err != nil {
		return fail(err)
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO goscope_session_schema(id,version) VALUES(1,1)`); err != nil {
		return fail(err)
	}
	var version int
	if err = tx.QueryRow(`SELECT version FROM goscope_session_schema WHERE id=1`).Scan(&version); err != nil {
		return fail(err)
	}
	if version != 1 {
		return fail(fmt.Errorf("unsupported database schema %d", version))
	}
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS goscope_sessions(namespace TEXT NOT NULL,id TEXT NOT NULL,version INTEGER NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(namespace,id))`); err != nil {
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Load(ctx context.Context, key session.Key) (*session.Snapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context required")
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	var raw []byte
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT version,payload FROM goscope_sessions WHERE namespace=? AND id=?`, key.Namespace, key.ID).Scan(&revision, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out, err := session.Decode(raw)
	if err != nil {
		return nil, err
	}
	if out.Key != key || out.Version != revision {
		return nil, fmt.Errorf("session row and payload disagree")
	}
	return out, nil
}
func (s *Store) Save(ctx context.Context, snapshot *session.Snapshot) (*session.Snapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context required")
	}
	if snapshot != nil && snapshot.Version == math.MaxInt64 {
		return nil, fmt.Errorf("session version exhausted")
	}
	candidate, err := session.Clone(snapshot)
	if err != nil {
		return nil, err
	}
	expected := candidate.Version
	candidate.Version++
	raw, err := json.Marshal(candidate)
	if err != nil {
		return nil, err
	}
	var result sql.Result
	if expected == 0 {
		result, err = s.db.ExecContext(ctx, `INSERT INTO goscope_sessions(namespace,id,version,payload) VALUES(?,?,?,?) ON CONFLICT(namespace,id) DO NOTHING`, candidate.Key.Namespace, candidate.Key.ID, candidate.Version, raw)
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE goscope_sessions SET version=?,payload=? WHERE namespace=? AND id=? AND version=?`, candidate.Version, raw, candidate.Key.Namespace, candidate.Key.ID, expected)
	}
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, session.ErrConflict
	}
	return candidate, nil
}
