// Package store owns the SQLite database: connection pools, pragmas and
// forward-only migrations.
//
// All writes go through a single-connection writer pool so SQLite never sees
// concurrent writers; reads use a separate read-only pool.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"drool/internal/store/db"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// FileName is the database file inside the data directory.
const FileName = "drool.db"

// Store bundles the writer and reader pools with sqlc query sets.
type Store struct {
	Writer *sql.DB
	Reader *sql.DB

	// W executes queries on the writer; R on the reader.
	W *db.Queries
	R *db.Queries
}

// Open opens (creating if needed) the database at path. Migrations are not
// applied; call Migrate.
func Open(path string) (*Store, error) {
	// The database holds password hashes, encrypted credentials and the
	// audit log: owner-only, whatever the umask. SQLite gives the -wal and
	// -shm files the main file's mode.
	if f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600); err == nil {
		f.Close()
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(p, 0o600)
		}
	}
	pragmas := []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(1)",
		"busy_timeout(5000)",
	}
	dsn := func(extra ...string) string {
		q := url.Values{}
		for _, p := range append(pragmas, extra...) {
			q.Add("_pragma", p)
		}
		q.Set("_txlock", "immediate")
		return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
	}

	w, err := sql.Open("sqlite", dsn())
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxIdleTime(0)

	r, err := sql.Open("sqlite", dsn("query_only(1)"))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)

	s := &Store{Writer: w, Reader: r, W: db.New(w), R: db.New(r)}
	if err := s.Ping(context.Background()); err != nil {
		s.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return s, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	return errors.Join(s.Reader.Close(), s.Writer.Close())
}

// Ping checks both pools.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.Writer.PingContext(ctx); err != nil {
		return err
	}
	return s.Reader.PingContext(ctx)
}

// InTx runs fn inside a write transaction.
func (s *Store) InTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.Writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(db.New(tx)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("migration %q: expected NNNN_name.sql", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %q: %w", name, err)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", v, prev, name)
		}
		seen[v] = name
		body, err := fs.ReadFile(migrationFS, "migrations/"+name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// LatestVersion is the highest migration version built into this binary.
func LatestVersion() int {
	ms, err := loadMigrations()
	if err != nil || len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// ErrNewerSchema means the database was migrated by a newer binary.
var ErrNewerSchema = errors.New("database schema is newer than this binary supports")

// SchemaVersion returns the currently applied schema version (0 if none).
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var exists int
	err := s.Reader.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&exists)
	if err != nil || exists == 0 {
		return 0, err
	}
	var v sql.NullInt64
	if err := s.Reader.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

// Migrate applies all pending migrations, each in its own transaction. It
// returns the list of applied migration names.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if _, err := s.Writer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return nil, err
	}
	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return nil, err
	}
	if latest := ms[len(ms)-1].version; current > latest {
		return nil, fmt.Errorf("%w (database: %d, binary: %d)", ErrNewerSchema, current, latest)
	}
	var applied []string
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		tx, err := s.Writer.BeginTx(ctx, nil)
		if err != nil {
			return applied, err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return applied, err
		}
		if err := tx.Commit(); err != nil {
			return applied, err
		}
		applied = append(applied, m.name)
	}
	return applied, nil
}

// Now returns the current time in the database's representation (Unix ms).
func Now() int64 { return time.Now().UnixMilli() }
