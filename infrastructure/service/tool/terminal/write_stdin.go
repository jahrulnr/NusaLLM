package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

const (
	defaultWriteYield = 250 * time.Millisecond
	maxWriteYield     = 30 * time.Second
	// Empty writes are polls: they wait longer by default and may park
	// much longer — a tailing process rewards patience; the cap keeps a
	// single call from monopolizing the turn.
	defaultPollYield = 5 * time.Second
	minPollYield     = 5 * time.Second
	maxPollYield     = 300 * time.Second
)

// WriteStdinTool feeds an open session and polls its output — an empty
// chars is a pure poll, so there is no separate read verb. A single
// Ctrl-C byte is an interrupt, not literal input — the mapping lives in
// the session's Interrupt, which injects the byte on a pty and signals
// the process group on pipes.
type WriteStdinTool struct {
	schema valueobject.ToolSchema
	store  dservice.TerminalSessions
}

func NewWriteStdinTool(store dservice.TerminalSessions) (dservice.Tool, error) {
	if store == nil {
		return nil, errors.New("terminal session store is required")
	}
	schema, err := valueobject.NewToolSchema(
		"write_stdin",
		"Write characters to an existing terminal session from exec_session and return its recent output. An empty chars polls without writing. A single \\u0003 sends Ctrl-C (SIGINT) instead of literal input.",
		json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"integer","description":"Session id returned by exec_session"},"chars":{"type":"string","description":"Characters to write to the session's stdin — empty polls without writing; \"\\u0003\" alone sends Ctrl-C"},"yield_time_ms":{"type":"integer","description":"Milliseconds to wait for output — writes default 250, cap 30000; empty polls wait at least 5000, cap 300000"}},"required":["session_id"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &WriteStdinTool{schema: schema, store: store}, nil
}

func (tool *WriteStdinTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *WriteStdinTool) Execute(ctx context.Context, call entity.ToolCall, emit dservice.ToolEmitter) (dservice.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dservice.ToolResult{}, err
	}
	convID := dservice.ConversationID(ctx)
	if err := valueobject.ValidID(convID); err != nil {
		return dservice.ToolResult{}, fmt.Errorf("write_stdin conversation id: %w", err)
	}
	var input struct {
		SessionID   int    `json:"session_id"`
		Chars       string `json:"chars"`
		YieldTimeMS int    `json:"yield_time_ms"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return dservice.ToolResult{}, fmt.Errorf("invalid write_stdin input: %w", err)
	}
	sess, ok := tool.store.Get(convID, input.SessionID)
	if !ok {
		return dservice.ToolResult{}, fmt.Errorf("session %d not found — it may have exited or been reaped", input.SessionID)
	}

	yield := defaultWriteYield
	if input.Chars != "" {
		if input.Chars == "\x03" {
			if err := sess.Interrupt(); err != nil {
				return dservice.ToolResult{}, err
			}
		} else if err := sess.Write([]byte(input.Chars)); err != nil {
			return dservice.ToolResult{}, err
		}
		yield = clampYield(input.YieldTimeMS, defaultWriteYield, 0, maxWriteYield)
	} else {
		// Poll semantics: the floor is the codex default so a caller
		// asking for a short wait still gets a meaningful window.
		yield = clampYield(input.YieldTimeMS, defaultPollYield, minPollYield, maxPollYield)
	}

	start := time.Now()
	poll := sess.Collect(ctx, yield, func(chunk string) {
		if emit != nil {
			emit(chunk)
		}
	})
	if err := ctx.Err(); err != nil {
		// Interruption leaves the session alive — the model already
		// knows this id and may resume it from another call.
		return dservice.ToolResult{}, err
	}
	return dservice.ToolResult{Text: formatPoll(sess.ID(), poll, start, call.OutputWithMiddleTruncateWindow(poll.Output, poll.Output, len(poll.Output), ""))}, nil
}
