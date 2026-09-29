package repository

import (
	"context"
	"io"
)

// ToolOutput stores a conversation-owned tool output stream. Reserve checks
// the owner and allocates a reference; Open creates the file only if output
// actually exceeds the in-memory budget.
type ToolOutput interface {
	Reserve(ctx context.Context, conversationID, callID string) (ToolOutputSpill, error)
}

type ToolOutputSpill interface {
	Reference() string
	Open() (io.WriteCloser, error)
}
