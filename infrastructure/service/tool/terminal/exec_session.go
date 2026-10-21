package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

const (
	defaultSessionYield = 10 * time.Second
	minSessionYield     = 250 * time.Millisecond
	maxSessionYield     = 30 * time.Second
)

// ExecSessionTool opens a persistent terminal session — the session
// outlives the call, so a process still running when the yield expires
// returns its id for follow-up through write_stdin. The "Process running
// with session ID N" header line is the contract the model keys on.
type ExecSessionTool struct {
	schema valueobject.ToolSchema
	store  dservice.TerminalSessions
}

func NewExecSessionTool(store dservice.TerminalSessions) (dservice.Tool, error) {
	if store == nil {
		return nil, errors.New("terminal session store is required")
	}
	schema, err := valueobject.NewToolSchema(
		"exec_session",
		"Run a shell command in a persistent terminal session. Waits yield_time_ms, then returns the output plus either the exit code or a session id for ongoing interaction through write_stdin. Use tty only for programs that require a real terminal or interactive stdin (isatty checks, password prompts, REPLs) — the default pipes backend suits everything else but has closed stdin, so write_stdin on it only accepts the Ctrl-C byte. Sessions survive turn interruption and die with the conversation.",
		json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute"},"tty":{"type":"boolean","description":"Allocate a pseudo-terminal — required for isatty-checking programs and any interactive stdin through write_stdin; default false"},"yield_time_ms":{"type":"integer","description":"Milliseconds to wait for initial output before returning the session id — default 10000, clamped 250-30000"}},"required":["command"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &ExecSessionTool{schema: schema, store: store}, nil
}

func (tool *ExecSessionTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *ExecSessionTool) Execute(ctx context.Context, call entity.ToolCall, emit dservice.ToolEmitter) (dservice.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dservice.ToolResult{}, err
	}
	convID := dservice.ConversationID(ctx)
	if err := valueobject.ValidID(convID); err != nil {
		return dservice.ToolResult{}, fmt.Errorf("exec_session conversation id: %w", err)
	}
	var input struct {
		Command     string `json:"command"`
		TTY         bool   `json:"tty"`
		YieldTimeMS int    `json:"yield_time_ms"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return dservice.ToolResult{}, fmt.Errorf("invalid exec_session input: %w", err)
	}
	if strings.TrimSpace(input.Command) == "" {
		return dservice.ToolResult{}, errors.New("command is required")
	}

	yield := clampYield(input.YieldTimeMS, defaultSessionYield, minSessionYield, maxSessionYield)
	sess, err := tool.store.Open(ctx, convID, dservice.TerminalOpenRequest{
		Command: input.Command,
		Dir:     dservice.WorkspaceDir(ctx),
		TTY:     input.TTY,
	})
	if err != nil {
		return dservice.ToolResult{}, fmt.Errorf("exec_session: %w", err)
	}
	// The id must reach the model or the session is an unkillable
	// orphan — any early return (cancel, error) before the response is
	// built terminates it ourselves, through the store so the registry
	// entry goes too.
	delivered := false
	defer func() {
		if !delivered {
			tool.store.Terminate(convID, sess.ID())
		}
	}()

	start := time.Now()
	poll := sess.Collect(ctx, yield, func(chunk string) {
		if emit != nil {
			emit(chunk)
		}
	})
	if err := ctx.Err(); err != nil {
		return dservice.ToolResult{}, err
	}
	delivered = true
	return dservice.ToolResult{Text: formatPoll(sess.ID(), poll, start, call.OutputWithMiddleTruncateWindow(poll.Output, poll.Output, len(poll.Output), ""))}, nil
}

// clampYield applies a default when unset and bounds the wait — codex's
// yield_time_ms contract keeps the tool responsive without letting a
// poll park the turn forever.
func clampYield(ms int, fallback, floor, cap time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	got := time.Duration(ms) * time.Millisecond
	if got < floor {
		return floor
	}
	return min(got, cap)
}

// formatPoll renders the codex contract: an exit line or the session-id
// line the model uses to keep driving the process, the wall time, and
// the pre-truncation byte count — codex's original_token_count, in the
// bytes unit our budgets use — so the model always knows the scale of
// what was collected before windowing, including bytes the buffer cap
// dropped. The drained output itself is windowed to the call budget —
// head+tail with an omission marker, same rule the entity applies to
// oversized tool output.
func formatPoll(id int, poll dservice.TerminalPoll, start time.Time, body string) string {
	var header string
	if poll.Running {
		header = fmt.Sprintf("Process running with session ID %d", id)
	} else {
		header = fmt.Sprintf("Process exited with code %d", poll.ExitCode)
	}
	return fmt.Sprintf("%s\nWall time: %.3f seconds\nOriginal output: %d bytes\nOutput:\n%s", header, time.Since(start).Seconds(), poll.TotalBytes, body)
}
