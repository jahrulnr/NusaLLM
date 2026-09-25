package reposecret

import (
	"context"
	"database/sql"
	"errors"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Get returns the stored value — 404 for an unknown key.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", apperror.NewNotFoundError("secret "+key, err)
	}
	if err != nil {
		return "", err
	}
	return v, nil
}
