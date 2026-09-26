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
