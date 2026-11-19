// Package reposecret implements the SecretStore port on sqlite — a plain
// key-value table. Not encryption: values sit in the file as plaintext,
// but the binary container keeps them out of casual cat/grep inspection,
// and the port leaves room for an encrypted backend later.
package reposecret

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is a sqlite-backed SecretStore.
type Store struct {
	db *sql.DB
}

// New opens (and migrates) the store at path — a file or ":memory:".
// A missing parent is created private; an existing one belongs to the
// caller and is never chmodded — it may be a shared mount, /tmp, or a CI
// workspace whose access other processes rely on. Sidecar privacy does
// not need the parent: SQLite creates the rollback journal with the
// database file's own mode, and the db is clamped 0600 before any value
// is written, so every journal that can carry secrets inherits 0600 too.
func New(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer keeps the file coherent
	// Busy timeout so a short-lived writer (gateway add --key-stdin)
	// retries while the REPL's store holds the write lock, instead of
	// failing instantly with SQLITE_BUSY. The first statement also
	// materializes the db file.
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		// Values sit plaintext in the file — clamp the db itself, so a
		// pre-existing 0644 file is fixed too. This runs before the
		// migration below: the journal inherits the db's mode at
		// creation, so even the schema transaction's sidecar is 0600.
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, err
		}
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
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }
