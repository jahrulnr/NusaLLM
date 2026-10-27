package service

import (
	"context"
	"io"

	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// ReleaseSource reports the published releases of NusaLLM and streams their
// files. The adapter owns the transport; the domain sees only what it returns.
type ReleaseSource interface {
	Latest(ctx context.Context) (valueobject.Release, error)
	// Fetch streams one published file into dst and reports the bytes written.
	// A file larger than limit is an error, and a failed fetch may have written
	// part of dst, so callers discard what they staged on error.
	Fetch(ctx context.Context, url string, dst io.Writer, limit int64) (int64, error)
}
