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
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer keeps the file coherent
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
