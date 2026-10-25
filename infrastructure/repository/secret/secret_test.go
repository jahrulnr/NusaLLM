package reposecret

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSetGetDelete(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if err := s.Set(ctx, "gateway/x/api_key", "sk-live"); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get(ctx, "gateway/x/api_key")
	if err != nil {
		t.Fatal(err)
	}
	if v != "sk-live" {
		t.Fatalf("Get = %q", v)
	}

	// Upsert overwrites.
	if err := s.Set(ctx, "gateway/x/api_key", "sk-rotated"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get(ctx, "gateway/x/api_key"); v != "sk-rotated" {
		t.Fatalf("overwrite = %q", v)
	}

	if err := s.Delete(ctx, "gateway/x/api_key"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "gateway/x/api_key"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("deleted key still resolves")
	}
}

func TestNotFound(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	if _, err := s.Get(ctx, "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Get missing: %v", err)
	}
	if err := s.Delete(ctx, "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Delete missing: %v", err)
	}
}

// SQLite creates the rollback journal with process-umask permissions —
// a crash mid-commit leaves <db>-journal behind containing plaintext
// secret values. The db's own 0600 cannot stop that; the protection
// lives one level up: the parent must refuse group/other so no sidecar
// file is reachable.
func TestJournalSidecarCoveredByDirPerms(t *testing.T) {
	dir := t.TempDir()
	// A pre-existing permissive parent gets reclamped by New.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "secrets.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Drive a real write transaction on a second connection and hold it
	// open so the rollback journal exists on disk.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO secrets (key, value, created_at, updated_at) VALUES ('probe','SECRET-VALUE',1,1)`); err != nil {
		t.Fatal(err)
	}
	journal := dbPath + "-journal"
	if _, statErr := os.Stat(journal); statErr != nil {
		_ = tx.Rollback()
		t.Skipf("no -journal file materialized on this driver/filesystem: %v", statErr)
	}
	_ = tx.Rollback()

	dfi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dfi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("secret dir mode = %o — journal sidecars are umask-created, so the dir itself must refuse group/other", dfi.Mode().Perm())
	}
}

// A pre-existing loose parent is reclamped on open — a 0755 dir leaves
// the (0600) db reachable for unlink/replace by other users and exposes
// every umask-created sidecar file.
func TestPreExistingParentDirReclamped(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(dir, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 0700 (0755 reclamped)", fi.Mode().Perm())
	}
}
