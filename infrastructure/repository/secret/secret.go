// Package reposecret implements the SecretStore port on sqlite — a plain
// key-value table. Not encryption: values sit in the file as plaintext,
// but the binary container keeps them out of casual cat/grep inspection,
// and the port leaves room for an encrypted backend later.
package reposecret

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is a sqlite-backed SecretStore.
type Store struct {
	db *sql.DB
}

// New opens (and migrates) the store at path — a file or ":memory:".
// The db is clamped 0600 and its parent 0700: SQLite's rollback journal
// is created by the driver with process-umask permissions — a crash
// mid-commit would leave plaintext values beside the db, so the file's
// own mode is not enough; the directory must refuse group/other too.
func New(path string) (*Store, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		// A pre-existing looser dir is reclamped — MkdirAll alone is a
		// no-op there, and 0755 would leave the journal sidecars (and
		// the db's directory slot) exposed. A bare filename resolves to
		// "." — the caller's own directory, which is not ours to tighten.
		if dir != "." {
			if fi, err := os.Stat(dir); err == nil {
				if mode := fi.Mode().Perm(); mode&0o077 != 0 {
					if err := os.Chmod(dir, mode&^0o077); err != nil {
						return nil, fmt.Errorf("reclamp secret dir %s: %w", dir, err)
					}
				}
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer keeps the file coherent
	// Busy timeout so a short-lived writer (gateway add --key-stdin)
	// retries while the REPL's store holds the write lock, instead of
	// failing instantly with SQLITE_BUSY.
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS secrets (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		// Values sit plaintext in the file — clamp the db itself, not
		// just the dir, so a pre-existing 0644 file is fixed too.
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }
