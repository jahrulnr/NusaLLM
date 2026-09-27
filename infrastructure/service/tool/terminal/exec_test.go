//go:build linux

package toolexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

func newTool(t *testing.T) service.Tool {
	t.Helper()
	tool, err := NewExecTool()
	if err != nil {
		t.Fatalf("NewExecTool: %v", err)
	}
	return tool
}

func mustCall(t *testing.T, id, input string, budget int) entity.ToolCall {
	t.Helper()
	call, err := entity.NewToolCall(id, "exec", input, enum.TooltypeOrigin)
	if err != nil {
		t.Fatalf("NewToolCall: %v", err)
	}
	if budget > 0 {
		call.SetOutputBudget(budget)
	}
	return call
}

func TestExecToolSchema(t *testing.T) {
	s := newTool(t).Schema()
	if s.Name() != "exec" || !s.Enabled() ||
		s.Permission() != enum.ToolPermissionAgent || !s.CallableByAgent() {
		t.Fatalf("unexpected schema: %+v", s)
	}
	var params struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(s.Parameters(), &params); err != nil ||
		len(params.Required) != 1 || params.Required[0] != "command" {
		t.Fatalf("command not the required param: %s", s.Parameters())
	}
}

func TestExecEcho(t *testing.T) {
	res, err := newTool(t).Execute(context.Background(),
		mustCall(t, "echo-1", `{"command":"echo hello"}`, 0), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Text
	if !strings.Contains(out, "hello") {
		t.Fatalf("output missing echo: %q", out)
	}
}

func TestExecMergesStderrInOrder(t *testing.T) {
	res, err := newTool(t).Execute(context.Background(),
		mustCall(t, "merge-1", `{"command":"echo o1; echo e1 >&2; echo o2"}`, 0), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Text
	o1, e1, o2 := strings.Index(out, "o1"), strings.Index(out, "e1"), strings.Index(out, "o2")
	if o1 < 0 || e1 < 0 || o2 < 0 || !(o1 < e1 && e1 < o2) {
		t.Fatalf("stderr not merged in arrival order: %q", out)
	}
}

func TestExecNonzeroExitIsOutput(t *testing.T) {
	res, err := newTool(t).Execute(context.Background(),
		mustCall(t, "exit-1", `{"command":"echo partial; exit 3"}`, 0), nil)
	if err != nil {
		t.Fatalf("nonzero exit must not be a Go error: %v", err)
	}
	out := res.Text
	if !strings.Contains(out, "partial") || !strings.Contains(out, "(exit 3)") {
		t.Fatalf("exit status not reported: %q", out)
	}
}

func TestExecTimeoutKillsTree(t *testing.T) {
	start := time.Now()
	res, err := newTool(t).Execute(context.Background(),
		mustCall(t, "kill-1", `{"command":"sleep 30","timeout_ms":200}`, 0), nil)
	if err != nil {
		t.Fatalf("killed process must not be a Go error: %v", err)
	}
	out := res.Text
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("kill took too long: %v", elapsed)
	}
	if !strings.Contains(out, "killed") {
		t.Fatalf("kill not reported: %q", out)
	}
}

func TestExecOversizedSpillsAndTruncates(t *testing.T) {
	const budget = 400
	call := mustCall(t, "oversized-1", `{"command":"seq 1 2000"}`, budget)
	res, err := newTool(t).Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Text
	if len(out) > budget {
		t.Fatalf("result exceeds budget: %d > %d", len(out), budget)
	}
	spill := filepath.Join(os.TempDir(), "nusallm-exec-oversized-1.log")
	defer os.Remove(spill)
	if !strings.HasPrefix(out, "1\n2\n3") || !strings.HasSuffix(out, "2000\n") {
		t.Fatalf("missing head or tail: %q...", out[:80])
	}
	if !strings.Contains(out, "bytes omitted") || !strings.Contains(out, "full output: "+spill) {
		t.Fatalf("marker missing count or spill path: %q", out)
	}
	data, err := os.ReadFile(spill)
	if err != nil {
		t.Fatalf("spill file: %v", err)
	}
	if !strings.HasPrefix(string(data), "1\n2\n3") || !strings.HasSuffix(string(data), "2000\n") {
		t.Fatalf("spill does not hold the full output: %d bytes", len(data))
	}
}

func TestExecEmitsChunks(t *testing.T) {
	var chunks []string
	res, err := newTool(t).Execute(context.Background(),
		mustCall(t, "emit-1", `{"command":"echo a; echo b"}`, 0),
		func(chunk string) { chunks = append(chunks, chunk) })
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Text
	if len(chunks) == 0 || strings.Join(chunks, "") != out {
		t.Fatalf("emitted chunks %q do not reassemble into %q", chunks, out)
	}
}

func TestExecRequiresCommand(t *testing.T) {
	for _, input := range []string{`{"command":"  "}`, `{"command":""}`, `{"timeout_ms":1000}`} {
		if _, err := newTool(t).Execute(context.Background(),
			mustCall(t, "empty-1", input, 0), nil); err == nil {
			t.Fatalf("expected error for %s", input)
		}
	}
}
