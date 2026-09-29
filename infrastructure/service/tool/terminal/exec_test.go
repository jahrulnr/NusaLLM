//go:build linux

package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

func newTool(t *testing.T) service.Tool {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool, err := NewExecTool(mustOutputRepo(t, root), NewLocalRunner())
	if err != nil {
		t.Fatalf("NewExecTool: %v", err)
	}
	return tool
}

func mustOutputRepo(t *testing.T, root string) drepo.ToolOutput {
	t.Helper()
	repo, err := repoconversation.NewToolOutputRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func testContext() context.Context {
	return service.WithConversationID(context.Background(), "c1")
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

type fakeRunner struct {
	command   string
	workspace string
}

func (r *fakeRunner) Run(_ context.Context, command, workspace string, output io.Writer) (service.ProcessExit, error) {
	r.command, r.workspace = command, workspace
	_, err := output.Write([]byte("remote output"))
	return service.ProcessExit{Code: 7}, err
}

func TestExecUsesInjectedRunner(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	tool, err := NewExecTool(mustOutputRepo(t, root), runner)
	if err != nil {
		t.Fatal(err)
	}
	ctx := service.WithWorkspaceDir(testContext(), "/remote/work")
	var preview string
	result, err := tool.Execute(ctx, mustCall(t, "remote-1", `{"command":"echo ignored"}`, 0), func(chunk string) { preview += chunk })
	if err != nil {
		t.Fatal(err)
	}
	if runner.command != "echo ignored" || runner.workspace != "/remote/work" {
		t.Fatalf("runner saw %q in %q", runner.command, runner.workspace)
	}
	if preview != "remote output" || result.Text != "remote output\n(exit 7)" {
		t.Fatalf("preview=%q result=%q", preview, result.Text)
	}
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
	res, err := newTool(t).Execute(testContext(),
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
	res, err := newTool(t).Execute(testContext(),
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
	res, err := newTool(t).Execute(testContext(),
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
	res, err := newTool(t).Execute(testContext(),
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
	res, err := newTool(t).Execute(testContext(), call, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Text
	if len(out) > budget {
		t.Fatalf("result exceeds budget: %d > %d", len(out), budget)
	}
	_, spill, ok := strings.Cut(out, "full output: ")
	if !ok {
		t.Fatalf("spill marker missing: %q", out)
	}
	spill, _, _ = strings.Cut(spill, " ...]")
	if !strings.HasPrefix(out, "1\n2\n3") || !strings.HasSuffix(out, "2000\n") {
		t.Fatalf("missing head or tail: %q...", out[:80])
	}
	if !strings.Contains(out, "bytes omitted") || !strings.Contains(out, "full output: "+spill) {
		t.Fatalf("marker missing count or spill path: %q", out)
	}
	// The file persists for the session — the marker's path must stay
	// valid for a follow-up read.
	data, err := os.ReadFile(spill)
	if err != nil {
		t.Fatalf("spill file must persist after execute: %v", err)
	}
	if !strings.HasPrefix(string(data), "1\n2\n3") || !strings.HasSuffix(string(data), "2000\n") {
		t.Fatalf("spill file missing head or tail: %q...%q", data[:20], data[len(data)-20:])
	}
}

func TestExecSpillBelongsToConversationAndSurvivesAge(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool, err := NewExecTool(mustOutputRepo(t, root), NewLocalRunner())
	if err != nil {
		t.Fatal(err)
	}
	ctx := service.WithConversationID(context.Background(), "c1")
	result, err := tool.Execute(ctx, mustCall(t, "owned", `{"command":"seq 1 2000"}`, 400), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, path, ok := strings.Cut(result.Text, "full output: ")
	if !ok {
		t.Fatalf("spill marker missing: %q", result.Text)
	}
	path, _, _ = strings.Cut(path, " ...]")
	if filepath.Dir(path) != filepath.Join(root, "c1", "tool-output") {
		t.Fatalf("spill outside conversation: %s", path)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(ctx, mustCall(t, "later", `{"command":"true"}`, 400), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("conversation-owned spill expired before conversation deletion: %v", err)
	}
}

func TestExecDoesNotCreateMissingConversation(t *testing.T) {
	root := t.TempDir()
	tool, err := NewExecTool(mustOutputRepo(t, root), NewLocalRunner())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(testContext(), mustCall(t, "missing", `{"command":"echo hello"}`, 400), nil)
	if err == nil {
		t.Fatal("exec must not recreate a deleted conversation")
	}
	if _, err := os.Stat(filepath.Join(root, "c1")); !os.IsNotExist(err) {
		t.Fatalf("missing conversation was created: %v", err)
	}
}

func TestExecRejectsInvalidConversationIDBeforeRunning(t *testing.T) {
	root := t.TempDir()
	tool, err := NewExecTool(mustOutputRepo(t, root), NewLocalRunner())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "unexpected-command")
	ctx := service.WithConversationID(context.Background(), "../c2")
	input := fmt.Sprintf(`{"command":%q}`, "touch "+marker)
	_, err = tool.Execute(ctx, mustCall(t, "bad-id", input, 400), nil)
	if err == nil || !strings.Contains(err.Error(), "conversation id") {
		t.Fatalf("invalid conversation id must be rejected: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran for invalid conversation id: %v", err)
	}
}

func TestExecRejectsConversationFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "c1"), []byte("not a room"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, err := NewExecTool(mustOutputRepo(t, root), NewLocalRunner())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(testContext(), mustCall(t, "file-room", `{"command":"echo hello"}`, 400), nil)
	if err == nil || !strings.Contains(err.Error(), "exec conversation c1 is not a directory") {
		t.Fatalf("file must not be accepted as a conversation: %v", err)
	}
}

func TestExecExitSuffixOverflowSpillsWholeOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool, err := NewExecTool(mustOutputRepo(t, root), NewLocalRunner())
	if err != nil {
		t.Fatal(err)
	}
	const budget = 400
	command := `printf '%0400d' 0; exit 7`
	res, err := tool.Execute(testContext(), mustCall(t, "suffix-overflow", `{"command":"`+command+`"}`, budget), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > budget || !strings.Contains(res.Text, "bytes omitted") || !strings.HasSuffix(res.Text, "(exit 7)") {
		t.Fatalf("suffix-overflow excerpt = %q", res.Text)
	}
	_, path, ok := strings.Cut(res.Text, "full output: ")
	if !ok {
		t.Fatalf("missing spill marker: %q", res.Text)
	}
	path, _, _ = strings.Cut(path, " ...]")
	if filepath.Dir(path) != filepath.Join(root, "c1", "tool-output") {
		t.Fatalf("spill path outside room: %q", path)
	}
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != budget+len("\n(exit 7)") || !strings.HasSuffix(string(full), "\n(exit 7)") {
		t.Fatalf("spill does not contain the full result: %q", full)
	}
}

func TestRepeatedCallIDKeepsDistinctSpillMarkers(t *testing.T) {
	tool := newTool(t)
	paths := make([]string, 0, 2)
	for range 2 {
		result, err := tool.Execute(testContext(),
			mustCall(t, "repeat-id", `{"command":"seq 1 2000"}`, 400), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, path, ok := strings.Cut(result.Text, "full output: ")
		if !ok {
			t.Fatalf("missing spill marker: %q", result.Text)
		}
		path, _, _ = strings.Cut(path, " ...]")
		paths = append(paths, path)
		t.Cleanup(func() { _ = os.Remove(path) })
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("marker target unavailable: %v", err)
		}
	}
	if paths[0] == paths[1] {
		t.Fatalf("reused call ID overwrote earlier spill: %s", paths[0])
	}
}

func TestSpillCapReportsTruncation(t *testing.T) {
	result, err := newTool(t).Execute(testContext(),
		mustCall(t, "spill-cap", `{"command":"yes x | head -c 33554433"}`, 400), nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("spilled output (truncated at %d MiB): ", maxExecSpillBytes>>20)
	_, rest, ok := strings.Cut(result.Text, marker)
	if !ok {
		t.Fatalf("spill cap marker missing: %q", result.Text)
	}
	path, _, _ := strings.Cut(rest, " ...]")
	t.Cleanup(func() { _ = os.Remove(path) })
	info, err := os.Stat(path)
	if err != nil || info.Size() != maxExecSpillBytes {
		t.Fatalf("spill cap file = %v, %v", info, err)
	}
}

func TestExecTimeoutMsOverflowClamps(t *testing.T) {
	// timeout_ms near int64 max overflows the Duration multiply — the
	// clamp must land before conversion, so a fast command still runs.
	for _, ms := range []int64{math.MaxInt64, 600001, 9000000000000000000} {
		res, err := newTool(t).Execute(testContext(),
			mustCall(t, "huge-timeout",
				fmt.Sprintf(`{"command":"echo ok","timeout_ms":%d}`, ms), 0), nil)
		if err != nil {
			t.Fatalf("Execute(timeout_ms=%d): %v", ms, err)
		}
		if !strings.Contains(res.Text, "ok") || strings.Contains(res.Text, "killed") {
			t.Fatalf("timeout_ms=%d must clamp to the cap, not expire instantly: %q", ms, res.Text)
		}
	}
}

func TestExecNegativeTimeoutIgnored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := newTool(t).Execute(service.WithConversationID(ctx, "c1"),
		mustCall(t, "neg-timeout", `{"command":"sleep 30","timeout_ms":-1}`, 0), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("negative timeout_ms must not shorten the run oddly: %v", elapsed)
	}
	if !strings.Contains(res.Text, "killed") {
		t.Fatalf("expected cancel kill, got %q", res.Text)
	}
}

func TestExecEmitsChunks(t *testing.T) {
	var chunks []string
	res, err := newTool(t).Execute(testContext(),
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

func TestExecSpillWriteFailureHonestMarker(t *testing.T) {
	// A non-directory at the output path makes the spill open fail.
	// The result must report that failure, not claim to have full output.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool, err := NewExecTool(mustOutputRepo(t, root), NewLocalRunner())
	if err != nil {
		t.Fatal(err)
	}
	spillDir := filepath.Join(root, "c1", "tool-output")
	if err := os.WriteFile(spillDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(testContext(),
		mustCall(t, "full-1", `{"command":"seq 1 2000"}`, 400), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Text, "spill to "+spillDir+string(os.PathSeparator)) || !strings.Contains(res.Text, " failed:") ||
		strings.Contains(res.Text, "full output:") {
		t.Fatalf("spill failure must surface in the marker: %q", res.Text)
	}
}

func TestExecRequiresCommand(t *testing.T) {
	for _, input := range []string{`{"command":"  "}`, `{"command":""}`, `{"timeout_ms":1000}`} {
		if _, err := newTool(t).Execute(testContext(),
			mustCall(t, "empty-1", input, 0), nil); err == nil {
			t.Fatalf("expected error for %s", input)
		}
	}
}

// failingSpillWriter accepts a bounded number of bytes and then either
// short-writes or fails — the fault injection the injected writer port
// makes possible.
type failingSpillWriter struct {
	accept int
	err    error
	wrote  int
	calls  int
}

func (w *failingSpillWriter) Write(p []byte) (int, error) {
	w.calls++
	n := len(p)
	if n > w.accept {
		n = w.accept
	}
	w.wrote += n
	if w.err != nil {
		return n, w.err
	}
	return n, nil // short write without an error
}

func (w *failingSpillWriter) Close() error { return nil }

// The spill counts the bytes the writer accepted, and the first failure
// latches: later writes must not retry or keep counting, or the marker
// could still claim "full output" for a broken file.
func TestSpillSinkLatchesWriteFailure(t *testing.T) {
	short := &failingSpillWriter{accept: 4}
	sink := &outputSink{writer: short}
	sink.writeSpill([]byte("0123456789"))
	if sink.spillWritten != 4 {
		t.Fatalf("counted %d bytes, want the 4 the writer accepted", sink.spillWritten)
	}
	if !errors.Is(sink.spillErr, io.ErrShortWrite) {
		t.Fatalf("spillErr = %v, want io.ErrShortWrite", sink.spillErr)
	}
	sink.writeSpill([]byte("more"))
	if short.calls != 1 || sink.spillWritten != 4 {
		t.Fatalf("failure not latched: calls=%d written=%d", short.calls, sink.spillWritten)
	}
}

func TestSpillSinkCountsOnlyAcceptedBytes(t *testing.T) {
	broken := &failingSpillWriter{accept: 2, err: errors.New("disk on fire")}
	sink := &outputSink{writer: broken}
	sink.writeSpill([]byte("0123456789"))
	if sink.spillWritten != 2 || sink.spillErr == nil ||
		!strings.Contains(sink.spillErr.Error(), "disk on fire") {
		t.Fatalf("written=%d err=%v, want 2/disk on fire", sink.spillWritten, sink.spillErr)
	}
	sink.writeSpill([]byte("more"))
	if broken.calls != 1 {
		t.Fatalf("error not latched: calls=%d", broken.calls)
	}

	full := &failingSpillWriter{accept: math.MaxInt}
	ok := &outputSink{writer: full}
	ok.writeSpill([]byte("0123456789"))
	if ok.spillWritten != 10 || ok.spillErr != nil {
		t.Fatalf("written=%d err=%v, want 10/nil", ok.spillWritten, ok.spillErr)
	}
}
