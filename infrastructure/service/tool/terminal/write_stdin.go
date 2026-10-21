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
	minWriteYield     = 250 * time.Millisecond
	maxWriteYield     = 30 * time.Second
	// Empty writes are polls: they wait longer by default and may park
	// much longer — a tailing process rewards patience; the cap keeps a
	// single call from monopolizing the turn.
	defaultPollYield = 5 * time.Second
	minPollYield     = 5 * time.Second
	maxPollYield     = 300 * time.Second
	// postWriteSettle gives a tty child a beat to react after a write so
	// its output lands inside this call's window — codex pauses 100 ms
	// for the same reason.
	postWriteSettle = 100 * time.Millisecond
)

// WriteStdinTool feeds an open session and polls its output — an empty
// chars is a pure poll, so there is no separate read verb. A single
// Ctrl-C byte is an interrupt, not literal input — the mapping lives in
// the session's Interrupt, which injects the byte on a pty and signals
// the process group on pipes. Whole calls run inside the store's Interact
// so concurrent calls on one session serialize and a reaped id fails
// closed instead of landing on a reused session.
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
		"Write characters to an existing terminal session from exec_session and return its recent output. Writing requires the session's tty backend — pipes sessions have closed stdin and accept only the interrupt. An empty chars polls without writing. A single \\u0003 sends Ctrl-C (SIGINT) instead of literal input.",
		json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"integer","description":"Session id returned by exec_session"},"chars":{"type":"string","description":"Characters to write to the session's stdin — empty polls without writing; \"\\u0003\" alone sends Ctrl-C. Only tty sessions have open stdin"},"yield_time_ms":{"type":"integer","description":"Milliseconds to wait for output — writes default 250, range 250-30000; empty polls default 5000, range 5000-300000"}},"required":["session_id"]}`),
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

	var text string
	err := tool.store.Interact(ctx, convID, input.SessionID, func(sess dservice.TerminalSession) error {
		yield := defaultWriteYield
		if input.Chars != "" {
			if input.Chars == "\x03" {
				if err := sess.Interrupt(); err != nil {
					// Codex swallows a write racing with process exit and
					// still collects the final output — an interrupt that
					// lands on a just-dead session is not an error.
					if exited, _ := sess.Exited(); !exited {
						return err
					}
				}
			} else if err := sess.Write([]byte(input.Chars)); err != nil {
				if errors.Is(err, dservice.ErrTerminalStdinClosed) {
					// Codex's StdinClosed wording — the model learns
					// the remedy, not just the failure.
					return errors.New("stdin is closed for this session; rerun exec_session with tty=true to keep stdin open")
				}
				if exited, _ := sess.Exited(); !exited {
					return err
				}
			} else {
				// Let the child react before polling — its response
				// then lands in this window instead of the next poll.
				timer := time.NewTimer(postWriteSettle)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			yield = clampYield(input.YieldTimeMS, defaultWriteYield, minWriteYield, maxWriteYield)
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
			return err
		}
		text = formatPoll(sess.ID(), poll, start, call.OutputWithMiddleTruncateWindow(poll.Output, poll.Output, len(poll.Output), ""))
		return nil
	})
	switch {
	case errors.Is(err, dservice.ErrTerminalSessionGone):
		return dservice.ToolResult{}, fmt.Errorf("session %d not found — it may have exited or been reaped", input.SessionID)
	case err != nil:
		return dservice.ToolResult{}, err
	}
	return dservice.ToolResult{Text: text}, nil
}
