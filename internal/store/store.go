// Package store persists versioned JSON records in pure-Go SQLite. Secrets are
// encrypted with authenticated record identity; public JSON never contains them.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/midi-HL/muse-bridge/internal/secure"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Store struct {
	db    *sql.DB
	vault *secure.Vault
}
type Record struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt string          `json:"updated_at"`
}

func Open(dir string, v *secure.Vault) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "muse-bridge.db")
	db, e := sql.Open("sqlite", p)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, vault: v}
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS records(kind TEXT NOT NULL,id TEXT NOT NULL,data BLOB NOT NULL,secret BLOB,updated_at TEXT NOT NULL,PRIMARY KEY(kind,id));
CREATE TABLE IF NOT EXISTS counters(scope TEXT NOT NULL,subject TEXT NOT NULL,period TEXT NOT NULL,count INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(scope,subject,period));
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,time TEXT NOT NULL,level TEXT NOT NULL,message TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	_ = os.Chmod(p, 0600)
	var marker string
	e = db.QueryRow("SELECT value FROM meta WHERE key='master_check'").Scan(&marker)
	if errors.Is(e, sql.ErrNoRows) {
		sealed, err := v.Seal("master_check", []byte("muse-bridge"))
		if err != nil {
			db.Close()
			return nil, err
		}
		_, e = db.Exec("INSERT INTO meta(key,value) VALUES('master_check',?)", sealed)
	} else if e == nil {
		_, e = v.Open("master_check", []byte(marker))
	}
	if e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Put(ctx context.Context, kind, id string, data any, secret any) error {
	b, e := json.Marshal(data)
	if e != nil {
		return e
	}
	var sealed []byte
	if secret != nil {
		raw, err := json.Marshal(secret)
		if err != nil {
			return err
		}
		sealed, err = s.vault.Seal(kind+":"+id, raw)
		if err != nil {
			return err
		}
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO records(kind,id,data,secret,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(kind,id) DO UPDATE SET data=excluded.data,secret=COALESCE(excluded.secret,records.secret),updated_at=excluded.updated_at`, kind, id, b, sealed, time.Now().UTC().Format(time.RFC3339Nano))
	return e
}
func (s *Store) Get(ctx context.Context, kind, id string, dst any) error {
	var b []byte
	if e := s.db.QueryRowContext(ctx, "SELECT data FROM records WHERE kind=? AND id=?", kind, id).Scan(&b); e != nil {
		return e
	}
	return json.Unmarshal(b, dst)
}
func (s *Store) Secret(ctx context.Context, kind, id string, dst any) error {
	var b []byte
	if e := s.db.QueryRowContext(ctx, "SELECT secret FROM records WHERE kind=? AND id=?", kind, id).Scan(&b); e != nil {
		return e
	}
	if len(b) == 0 {
		return sql.ErrNoRows
	}
	raw, e := s.vault.Open(kind+":"+id, b)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, dst)
}
func (s *Store) List(ctx context.Context, kind string) ([]Record, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT id,kind,data,updated_at FROM records WHERE kind=? ORDER BY updated_at DESC,id", kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var r Record
		if e = rows.Scan(&r.ID, &r.Kind, &r.Data, &r.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Delete(ctx context.Context, kind, id string) error {
	res, e := s.db.ExecContext(ctx, "DELETE FROM records WHERE kind=? AND id=?", kind, id)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) Increment(ctx context.Context, scope, subject, period string, limit int) (int, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, "INSERT INTO counters(scope,subject,period,count) VALUES(?,?,?,0) ON CONFLICT DO NOTHING", scope, subject, period)
	if e != nil {
		return 0, e
	}
	var n int
	e = tx.QueryRowContext(ctx, "SELECT count FROM counters WHERE scope=? AND subject=? AND period=?", scope, subject, period).Scan(&n)
	if e != nil {
		return 0, e
	}
	if limit > 0 && n >= limit {
		return n, ErrLimit
	}
	_, e = tx.ExecContext(ctx, "UPDATE counters SET count=count+1 WHERE scope=? AND subject=? AND period=?", scope, subject, period)
	if e != nil {
		return 0, e
	}
	return n + 1, tx.Commit()
}

var ErrLimit = errors.New("request limit reached")

func (s *Store) Count(ctx context.Context, scope, subject, period string) int {
	var n int
	_ = s.db.QueryRowContext(ctx, "SELECT count FROM counters WHERE scope=? AND subject=? AND period=?", scope, subject, period).Scan(&n)
	return n
}
func (s *Store) Log(ctx context.Context, level, message string) error {
	_, e := s.db.ExecContext(ctx, "INSERT INTO events(time,level,message) VALUES(?,?,?)", time.Now().UTC().Format(time.RFC3339), level, message)
	return e
}
func (s *Store) Logs(ctx context.Context) ([]map[string]any, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT id,time,level,message FROM events ORDER BY id DESC LIMIT 500")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int
		var t, l, m string
		if e = rows.Scan(&id, &t, &l, &m); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "time": t, "level": l, "message": m})
	}
	return out, rows.Err()
}
func (s *Store) Prune(ctx context.Context, days int) error {
	_, e := s.db.ExecContext(ctx, "DELETE FROM events WHERE time < ?", time.Now().Add(-time.Duration(days)*24*time.Hour).UTC().Format(time.RFC3339))
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, "DELETE FROM events WHERE id NOT IN (SELECT id FROM events ORDER BY id DESC LIMIT 20000)")
	return e
}

// Export returns decrypted data ONLY to the caller's encrypted-backup path.
func (s *Store) Export(ctx context.Context) ([]byte, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT kind,id,data,secret FROM records WHERE kind NOT IN ('auth','admin_session','api_key')")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var k, id string
		var data, secret []byte
		if e = rows.Scan(&k, &id, &data, &secret); e != nil {
			return nil, e
		}
		r := map[string]any{"kind": k, "id": id, "data": json.RawMessage(data)}
		if len(secret) > 0 {
			raw, err := s.vault.Open(k+":"+id, secret)
			if err != nil {
				return nil, err
			}
			r["secret"] = json.RawMessage(raw)
		}
		out = append(out, r)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return json.Marshal(map[string]any{"version": 1, "records": out})
}
