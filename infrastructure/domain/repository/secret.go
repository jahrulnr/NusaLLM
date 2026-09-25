package repository

import "context"

// SecretStore is a key-value port for credential material — api keys,
// passwords, tokens. Records elsewhere carry a "secret:<key>" reference
// instead of plaintext; the value resolves through this port at load.
type SecretStore interface {
	// Get returns the stored value — 404 apperror for an unknown key.
	Get(ctx context.Context, key string) (string, error)
	// Set upserts key → value.
	Set(ctx context.Context, key, value string) error
	// Delete removes key — 404 apperror when absent.
	Delete(ctx context.Context, key string) error
}
