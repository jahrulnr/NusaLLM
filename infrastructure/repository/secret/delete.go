package reposecret

import (
	"context"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Delete removes key — 404 when absent.
func (s *Store) Delete(ctx context.Context, key string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE key = ?`, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.NewNotFoundError("secret "+key, nil)
	}
	return nil
}
