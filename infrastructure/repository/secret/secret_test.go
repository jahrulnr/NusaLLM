package reposecret

import (
	"path/filepath"
	"testing"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
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
