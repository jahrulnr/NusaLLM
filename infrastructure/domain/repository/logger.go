package repository

import "context"

// Logger is a write-only diagnostic sink — verbose by design (raw provider
// payloads belong here). One JSONL record per Append; kind tags the entry
// for grep ("provider.response", "send.error"). Rotation, buffering, and
// retention are implementation concerns.
type Logger interface {
	// Append serializes entry as one JSONL record under kind.
	Append(ctx context.Context, kind string, entry any) error
	// Close flushes and releases the underlying file.
	Close() error
}
