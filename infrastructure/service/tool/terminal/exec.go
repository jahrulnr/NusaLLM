package toolexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
)

const (
	defaultExecTimeout = 120 * time.Second
	maxExecTimeout     = 10 * time.Minute
	// waitDelay caps how long Wait may block on output pipes a stray
	// grandchild inherited — without it a detached process stalls the
	// tool forever even after its shell is dead.
	waitDelay = 5 * time.Second
)

// ExecTool runs a shell command and returns its merged output. It is
// inherently unsandboxed — there is no root to confine it to — so the
// schema's ToolPermissionAgent is the gate the application turn
// consults before invoking it.
type ExecTool struct {
	schema valueobject.ToolSchema
}

func NewExecTool() (service.Tool, error) {
	schema, err := valueobject.NewToolSchema(
		"run_terminal",
		"Run a shell command and return its combined stdout and stderr.",
		json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Shell command to execute"},"timeout_ms":{"type":"integer","description":"Timeout in milliseconds — default 120000, capped at 600000"}},"required":["command"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &ExecTool{schema: schema}, nil
}

func (tool *ExecTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *ExecTool) Execute(ctx context.Context, call entity.ToolCall, emit service.ToolEmitter) (service.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return service.ToolResult{}, err
	}
	var input struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil {
		return service.ToolResult{}, fmt.Errorf("invalid run_terminal input: %w", err)
	}
	if strings.TrimSpace(input.Command) == "" {
		return service.ToolResult{}, errors.New("command is required")
	}

	timeout := defaultExecTimeout
	if input.TimeoutMS > 0 {
		timeout = min(time.Duration(input.TimeoutMS)*time.Millisecond, maxExecTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := shellCommand(ctx, input.Command)
	cmd.WaitDelay = waitDelay
	// stdout and stderr share one writer: os/exec then wires both child
	// fds to a single pipe, so the merged stream keeps arrival order and
	// each Write below is one preview chunk.
	sink := &outputSink{emit: emit}
	cmd.Stdout = sink
	cmd.Stderr = sink
	if err := cmd.Start(); err != nil {
		return service.ToolResult{}, fmt.Errorf("run_terminal: %w", err)
	}
	runErr := cmd.Wait()

	full := sink.String()
	// Exit status is data the model must see — a nonzero exit is not a
	// Go error. The suffix lands at the end of full, so truncation keeps
	// it inside the tail.
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0:
		full += fmt.Sprintf("\n(exit %d)", exitErr.ExitCode())
	case ctx.Err() != nil:
		full += fmt.Sprintf("\n(killed: %v)", ctx.Err())
	case errors.As(runErr, &exitErr):
		full += fmt.Sprintf("\n(%s)", exitErr) // signal death, e.g. "signal: killed"
	default:
		full += fmt.Sprintf("\n(%v)", runErr)
	}

	budget := call.OutputBudget()
	if len(full) <= budget {
		return service.ToolResult{Text: full}, nil
	}

	// Over budget: spill the full output to a temp file and return a
	// head+tail excerpt naming it — never drop output silently.
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, call.ID())
	ref := filepath.Join(os.TempDir(), "llm-base-exec-"+safe+".log")
	if err := os.WriteFile(ref, []byte(full), 0o600); err != nil {
		ref = fmt.Sprintf("spill to %s failed: %v", ref, err)
	} else {
		ref = "full output: " + ref
	}
	return service.ToolResult{Text: truncateOutput(full, budget, ref)}, nil
}

// outputSink is the single writer behind cmd.Stdout+Stderr: one pipe
// merges both streams in arrival order, and every Write forwards a live
// preview chunk to emit.
type outputSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	emit service.ToolEmitter
}

func (s *outputSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	n, _ := s.buf.Write(p)
	s.mu.Unlock()
	// Emit outside the lock: the emitter is synchronous, and a slow
	// consumer must not serialize the two streams against each other.
	if s.emit != nil {
		s.emit(string(p[:n]))
	}
	return n, nil
}

func (s *outputSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// truncateOutput keeps a head and tail of full inside budget, joined by
// a marker carrying the omitted byte count and where the full output
// went. Head gets ~60% of the room — command failures usually show up
// early.
func truncateOutput(full string, budget int, ref string) string {
	const markerFmt = "\n\n[... %d bytes omitted — %s ...]\n\n"
	// omitted never exceeds len(full), so sizing the room against a
	// marker with that many digits bounds the real marker's length.
	room := budget - len(fmt.Sprintf(markerFmt, len(full), ref))
	if room <= 0 {
		return full[:budget] // budget too small for even the marker
	}
	head := room * 3 / 5
	tailStart := len(full) - (room - head)
	// Cut on rune boundaries so log excerpts stay valid UTF-8.
	for head > 0 && !utf8.RuneStart(full[head]) {
		head--
	}
	for tailStart < len(full) && !utf8.RuneStart(full[tailStart]) {
		tailStart++
	}
	return full[:head] + fmt.Sprintf(markerFmt, tailStart-head, ref) + full[tailStart:]
}
