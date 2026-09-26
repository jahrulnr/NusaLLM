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
		"exec",
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
		return service.ToolResult{}, fmt.Errorf("invalid exec input: %w", err)
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

	budget := call.OutputBudget()
	// The spill path is chosen before start so the sink can stream into it
	// the moment output outgrows the in-memory head — a command printing
	// unbounded output must not grow memory unboundedly with it.
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, call.ID())
	spillPath := filepath.Join(os.TempDir(), "llm-base-exec-"+safe+".log")

	cmd := shellCommand(ctx, input.Command)
	cmd.WaitDelay = waitDelay
	// stdout and stderr share one writer: os/exec then wires both child
	// fds to a single pipe, so the merged stream keeps arrival order and
	// each Write below is one preview chunk.
	sink := &outputSink{emit: emit, budget: budget, spillPath: spillPath}
	cmd.Stdout = sink
	cmd.Stderr = sink
	if err := cmd.Start(); err != nil {
		return service.ToolResult{}, fmt.Errorf("exec: %w", err)
	}
	runErr := cmd.Wait()

	// Exit status is data the model must see — a nonzero exit is not a
	// Go error. It belongs at the end of the stream, so it lands inside
	// the tail of the excerpt and at the end of the spill file.
	suffix := ""
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0:
		suffix = fmt.Sprintf("\n(exit %d)", exitErr.ExitCode())
	case ctx.Err() != nil:
		suffix = fmt.Sprintf("\n(killed: %v)", ctx.Err())
	case errors.As(runErr, &exitErr):
		suffix = fmt.Sprintf("\n(%s)", exitErr) // signal death, e.g. "signal: killed"
	default:
		suffix = fmt.Sprintf("\n(%v)", runErr)
	}

	sink.mu.Lock()
	head := sink.buf.String()
	tail := string(sink.tail)
	total := sink.total
	spilled := sink.spilled
	spillErr := sink.spillErr
	if sink.spill != nil {
		_, _ = sink.spill.Write([]byte(suffix))
		_ = sink.spill.Close()
	}
	sink.mu.Unlock()

	if !spilled {
		full := head + suffix
		if len(full) <= budget {
			return service.ToolResult{Text: full}, nil
		}
		// Never crossed the budget mid-run but overshot after the exit
		// suffix (or on a final chunk boundary) — spill the whole string
		// now; it is still fully in memory.
		ref := "full output: " + spillPath
		if err := os.WriteFile(spillPath, []byte(full), 0o600); err != nil {
			ref = fmt.Sprintf("spill to %s failed: %v", spillPath, err)
		}
		return service.ToolResult{Text: truncateOutput(full, budget, ref)}, nil
	}

	// Spilled mid-run: the file already holds everything, the excerpt is
	// assembled from the frozen head and the rolling tail.
	tail += suffix
	ref := "full output: " + spillPath
	if spillErr != nil {
		ref = fmt.Sprintf("spill to %s failed: %v", spillPath, spillErr)
	}
	return service.ToolResult{Text: truncateSpilled(head, tail, total+len(suffix), ref, budget)}, nil
}

// outputSink is the single writer behind cmd.Stdout+Stderr: one pipe
// merges both streams in arrival order, and every Write forwards a live
// preview chunk to emit. Memory stays bounded by the call budget: while
// output fits it buffers outright; the first Write past the budget opens
// the spill file, freezes buf as the head region, and keeps only a
// rolling tail window — the full stream continues into the file.
type outputSink struct {
	mu        sync.Mutex
	buf       bytes.Buffer // head region — frozen once spilled
	tail      []byte       // rolling tail once spilled
	spill     *os.File
	spillPath string
	spillErr  error
	spilled   bool
	total     int // everything ever written, for the omitted-byte count
	budget    int
	emit      service.ToolEmitter
}

func (s *outputSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.total += len(p)
	if s.spilled {
		if s.spill != nil {
			_, _ = s.spill.Write(p)
		}
		s.tail = append(s.tail, p...)
		if len(s.tail) > s.budget {
			s.tail = s.tail[len(s.tail)-s.budget:]
		}
	} else {
		s.buf.Write(p)
		if s.buf.Len() > s.budget {
			s.spilled = true
			if f, err := os.OpenFile(s.spillPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
				s.spill = f
				_, _ = f.Write(s.buf.Bytes())
			} else {
				// Degrade to head+tail without the file — the marker
				// reports the failure; memory still stays bounded.
				s.spillErr = err
			}
			s.tail = append([]byte(nil), s.buf.Bytes()[max(0, s.buf.Len()-s.budget):]...)
			s.buf.Truncate(min(s.budget, s.buf.Len()))
		}
	}
	s.mu.Unlock()
	// Emit outside the lock: the emitter is synchronous, and a slow
	// consumer must not serialize the two streams against each other.
	if s.emit != nil {
		s.emit(string(p))
	}
	return len(p), nil
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

// truncateSpilled is truncateOutput for the mid-run spill path: the head
// comes from the sink's frozen head buffer and the tail from its rolling
// window, so the omitted middle is derived rather than sliced out of a
// full string. Both sides are cut on rune boundaries like truncateOutput.
func truncateSpilled(head, tail string, total int, ref string, budget int) string {
	const markerFmt = "\n\n[... %d bytes omitted — %s ...]\n\n"
	room := budget - len(fmt.Sprintf(markerFmt, total, ref))
	if room <= 0 {
		return head[:min(budget, len(head))]
	}
	headRoom := room * 3 / 5
	tailRoom := room - headRoom
	if len(head) > headRoom {
		head = head[:headRoom]
		for len(head) > 0 && !utf8.ValidString(head) {
			head = head[:len(head)-1]
		}
	}
	if len(tail) > tailRoom {
		tail = tail[len(tail)-tailRoom:]
		for len(tail) > 0 && !utf8.ValidString(tail) {
			tail = tail[1:]
		}
	}
	return head + fmt.Sprintf(markerFmt, total-len(head)-len(tail), ref) + tail
}
