package repogateway

import (
	"context"
	"errors"
	"os"

	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Delete removes <id>.json plus its auto-stored secret — a dangling
// "secret:gateway/<id>/api_key" key would be orphaned garbage.
// Conversations referencing the route keep a dangling id — resolution
// failure is the application's fallback.
func (r *GatewayRepository) Delete(ctx context.Context, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	err := os.Remove(r.file(id))
	if errors.Is(err, os.ErrNotExist) {
		return apperror.NewNotFoundError("gateway "+id, err)
	}
	if err != nil {
		return err
	}
	if r.secrets != nil {
		if err := r.secrets.Delete(ctx, drepo.GatewaySecretKey(id)); err != nil &&
			apperror.As(err).StatusCode() != apperror.StatusNotFound {
			return err
		}
	}
	return nil
}
