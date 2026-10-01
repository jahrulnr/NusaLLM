package repository

import (
	"context"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// ConversationInfo is a lightweight read projection for inspecting a
// conversation without parsing its message history — conversation lists,
// pagination headers, and inspection UIs consume it.
type ConversationInfo struct {
	ID          string
	Title       string
	Route       string
	ModelID     string
	Workspace   string
	Environment enum.Environment
	Usage       valueobject.Usage
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// MessageCount is the total logical message count across sealed segments
	// plus the live tail; ActiveCount covers the live tail only — the slice
	// Load returns and the range most UI turns live in.
	MessageCount   int
	ActiveCount    int
	SealedSegments int
}

// Conversation is the conversation store port. Ordering contract: Save
// creates/upserts the record and must precede AppendMessage. Errors carry
// the apperror contract — a missing convID surfaces as 404, distinct from
// corruption or I/O failures.
type Conversation interface {
	// Save upserts the conversation-level record: id, route/modelID binding,
	// title, system prompt, tools catalog, accumulated usage. Messages are
	// persisted per turn via AppendMessage. The bound gateway is a separate
	// aggregate — resolved via the Gateway repository by Route().
	Save(ctx context.Context, conv *aggregate.Conversation) error

	// AppendMessage appends one message (a turn) to the stored history. The
	// bool reports whether the message line committed even if a later metadata
	// projection update fails.
	AppendMessage(ctx context.Context, convID string, msg entity.Message) (persisted bool, err error)

	// UpdateToolCallOutput patches the stored output of a previously
	// appended tool call — a call is persisted pending execution, and its
	// raw output, fixed latency, and any media references it
	// produced arrive later. Each call is an independent patch: concurrent
	// completions of parallel tool calls are safe by construction
	// (implementations append patch records rather than rewriting stored
	// lines), and a crash mid-batch leaves the remaining calls honestly
	// pending instead of losing every result. callID resolves globally
	// across segments: a call compacted before its output arrived is still
	// patched correctly.
	UpdateToolCallOutput(ctx context.Context, convID string, call entity.ToolCall) error

	// Compact seals messages[:dropBefore] into the next numbered archive
	// segment and restarts the live history as [summary] + tail — mirroring
	// Conversation.Compact, which the caller must apply to the aggregate as
	// well. Callers must not split a tool-call/result pair at the boundary.
	Compact(ctx context.Context, convID string, summary entity.Message, dropBefore int) error

	// RewriteFrom is the edit primitive — the persistence counterpart of
	// Conversation.RewriteFrom. It locates a user message anywhere in
	// history, rewrites its content, and discards every message recorded
	// after it. A target in the live tail truncates active.jsonl. A target
	// in a sealed segment promotes that segment — plus every earlier one —
	// back into active.jsonl so the model regains the full context of that
	// era; later segments and the old active tail are deleted. 404 when the
	// id is nowhere in history, 400 when it belongs to a non-user message.
	RewriteFrom(ctx context.Context, convID, msgID, content string) error

	// Info returns the lightweight projection for one conversation.
	Info(ctx context.Context, convID string) (ConversationInfo, error)

	// List returns infos for all stored conversations, newest activity
	// first — backs the conversation list UI.
	List(ctx context.Context) ([]ConversationInfo, error)

	// Load rebuilds the conversation's LIVE context: route/modelID bindings
	// plus the active segment — the messages the model actually sees. Sealed
	// archive segments stay on disk; LoadAt reaches them. The Gateway and
	// Model objects are resolved separately — repositories may span
	// multiple aggregates.
	Load(ctx context.Context, convID string) (*aggregate.Conversation, error)

	// LoadAt returns messages[start:end] over the global logical history —
	// sealed segments first, then the live tail — in append order. Indexes
	// clamp to bounds; a negative start counts from the end
	// (LoadAt(id, -10, 0) reads the latest 10), and end <= 0 reads through
	// the most recent message.
	LoadAt(ctx context.Context, convID string, start, end int) ([]entity.Message, error)

	// Delete removes the whole conversation and its owned files, including
	// tool output, while leaving other conversations untouched.
	Delete(ctx context.Context, convID string) error
}

// RepairAction selects how RepairOrphan resolves an uncommitted sealed
// segment: discard it as a duplicate of the live tail, or commit it as
// recovered history.
type RepairAction string

const (
	RepairDiscardDuplicate RepairAction = "discard-duplicate"
	RepairCommit           RepairAction = "commit"
)

func (a RepairAction) String() string { return string(a) }

// ConversationRepairer is the offline recovery port for the conversation
// store — separate from Conversation on purpose: repair rewrites storage
// invariants that runtime operations assume hold, so jobs and brokers
// holding Conversation must never see the verb. Only operator tooling
// obtains it, and only while no other process uses the store.
type ConversationRepairer interface {
	// RepairOrphan reconciles the one extra sealed segment an interrupted
	// Compact can leave between publishing the segment and its metadata
	// counter: RepairDiscardDuplicate removes a segment whose messages
	// still lead the live tail; RepairCommit publishes a unique one and
	// recomputes the stored counters. Callers stop the application first.
	RepairOrphan(ctx context.Context, convID string, action RepairAction) error
}
