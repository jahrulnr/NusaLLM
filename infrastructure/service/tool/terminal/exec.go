package toolexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

const (
	defaultExecTimeout = 120 * time.Second
	maxExecTimeout     = 10 * time.Minute
	maxExecSpillBytes  = 32 << 20 // cap spill file growth
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
	schema  valueobject.ToolSchema
	outputs drepo.ToolOutput
	runner  service.CommandRunner
}

func NewExecTool(outputs drepo.ToolOutput, runner service.CommandRunner) (service.Tool, error) {
	if outputs == nil {
		return nil, errors.New("tool output repository is required")
	}
	if runner == nil {
		return nil, errors.New("command runner is required")
	}
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
	return &ExecTool{schema: schema, outputs: outputs, runner: runner}, nil
}

func (tool *ExecTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *ExecTool) Execute(ctx context.Context, call entity.ToolCall, emit service.ToolEmitter) (service.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return service.ToolResult{}, err
	}
	convID := service.ConversationID(ctx)
	if err := valueobject.ValidID(convID); err != nil {
		return service.ToolResult{}, fmt.Errorf("exec conversation id: %w", err)
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
		// Clamp in milliseconds first — a huge timeout_ms converted
		// straight to a Duration would overflow int64 into a negative
		// and kill the command instantly.
		timeout = time.Duration(min(input.TimeoutMS, int(maxExecTimeout.Milliseconds()))) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	budget := call.OutputBudget()
	// Reserve before starting the process, but create the file only on spill.
	spill, err := tool.outputs.Reserve(ctx, convID, call.ID())
	if err != nil {
		return service.ToolResult{}, err
	}
	spillPath := spill.Reference()

	sink := &outputSink{emit: emit, budget: budget, spill: spill}
	// The turn's workspace is the shell's working directory. The runner
	// chooses where the command actually executes; output still flows into
	// one sink regardless of backend.
	exit, err := tool.runner.Run(ctx, input.Command, service.WorkspaceDir(ctx), sink)
	if err != nil {
		return service.ToolResult{}, fmt.Errorf("exec: %w", err)
	}

	// Exit status is data the model must see — a nonzero exit is not a
	// Go error. It belongs at the end of the stream, so it lands inside
	// the tail of the excerpt and at the end of the spill file.
	suffix := ""
	switch {
	case exit.Code > 0:
		suffix = fmt.Sprintf("\n(exit %d)", exit.Code)
	case ctx.Err() != nil:
		suffix = fmt.Sprintf("\n(killed: %v)", ctx.Err())
	case exit.Signal != "":
		suffix = fmt.Sprintf("\n(%s)", exit.Signal)
	case exit.Err != nil:
		suffix = fmt.Sprintf("\n(%v)", exit.Err)
	}

	sink.mu.Lock()
	head := sink.buf.String()
	tail := string(sink.tail)
	total := sink.total
	spilled := sink.spilled
	spillErr := sink.spillErr
	if sink.writer != nil {
		sink.writeSpill([]byte(suffix))
		_ = sink.writer.Close()
	}
	spillWritten := sink.spillWritten
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
		toWrite := full
		if len(toWrite) > maxExecSpillBytes {
			toWrite = toWrite[:maxExecSpillBytes]
			ref = fmt.Sprintf("spilled output (truncated at %d MiB): %s", maxExecSpillBytes>>20, spillPath)
		}
		f, err := spill.Open()
		if err == nil {
			var written int
			written, err = f.Write([]byte(toWrite))
			if err == nil && written != len(toWrite) {
				err = io.ErrShortWrite
			}
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			ref = fmt.Sprintf("spill to %s failed: %v", spillPath, err)
		}
		return service.ToolResult{Text: call.OutputWithMiddleTruncateWindow(full, full, len(full), ref)}, nil
	}

	// Spilled mid-run: the file holds the stream up to the spill cap, and
	// the excerpt is assembled from the frozen head and the rolling tail.
	tail += suffix
	ref := "full output: " + spillPath
	switch {
	case spillErr != nil:
		ref = fmt.Sprintf("spill to %s failed: %v", spillPath, spillErr)
	case spillWritten < total+len(suffix):
		// The cap dropped stream bytes — "full" would be a lie.
		ref = fmt.Sprintf("spilled output (truncated at %d MiB): %s", maxExecSpillBytes>>20, spillPath)
	}
	return service.ToolResult{Text: call.OutputWithMiddleTruncateWindow(head, tail, total+len(suffix), ref)}, nil
}

// outputSink is the single writer behind cmd.Stdout+Stderr: one pipe
// merges both streams in arrival order, and every Write forwards a live
// preview chunk to emit. Memory stays bounded by the call budget: while
// output fits it buffers outright; the first Write past the budget opens
// the spill file, freezes buf as the head region, and keeps only a
// rolling tail window — the stream continues into the file up to
// maxExecSpillBytes.
type outputSink struct {
	mu           sync.Mutex
	buf          bytes.Buffer // head region — frozen once spilled
	tail         []byte       // rolling tail once spilled
	writer       io.WriteCloser
	spill        drepo.ToolOutputSpill
	spillErr     error
	spilled      bool
	spillWritten int
	total        int // everything ever written, for the omitted-byte count
	budget       int
	emit         service.ToolEmitter
}

func (s *outputSink) writeSpill(p []byte) {
	if s.writer == nil || s.spillErr != nil || len(p) == 0 || s.spillWritten >= maxExecSpillBytes {
		return
	}
	n := len(p)
	if s.spillWritten+n > maxExecSpillBytes {
		n = maxExecSpillBytes - s.spillWritten
	}
	if n > 0 {
		// Count only bytes actually written and latch the first failure —
		// the guard above then stops further writes and the marker
		// reports "failed" instead of claiming "full output".
		written, err := s.writer.Write(p[:n])
		s.spillWritten += written
		if err == nil && written < n {
			err = io.ErrShortWrite
		}
		s.spillErr = err
	}
}

func (s *outputSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.total += len(p)
	if s.spilled {
		if s.writer != nil {
			s.writeSpill(p)
		}
		s.tail = append(s.tail, p...)
		if len(s.tail) > s.budget {
			s.tail = s.tail[len(s.tail)-s.budget:]
		}
	} else {
		s.buf.Write(p)
		if s.buf.Len() > s.budget {
			s.spilled = true
			if f, err := s.spill.Open(); err == nil {
				s.writer = f
				s.writeSpill(s.buf.Bytes())
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
