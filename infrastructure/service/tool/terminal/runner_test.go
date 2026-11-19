//go:build linux

package toolexec

import (
	"bytes"
	"strings"
	"testing"
)

// The three runner outcomes must stay distinct: a nonzero exit is tool
// evidence, a signal death is reported as a signal (not an exit code), and
// a command that cannot start is a Go error.
func TestLocalRunnerExitSignalAndStartFailure(t *testing.T) {
	runner := NewLocalRunner(nil)
	var out bytes.Buffer

	res, err := runner.Run(t.Context(), "exit 5", "", nil, &out)
	if err != nil || res.Code != 5 || res.Signal != "" || res.Err != nil {
		t.Fatalf("exit 5 => %+v, err %v", res, err)
	}

	res, err = runner.Run(t.Context(), "kill -9 $$", "", nil, &out)
	if err != nil {
		t.Fatalf("signal death returned a Go error: %v", err)
	}
	if res.Code != 0 || !strings.HasPrefix(res.Signal, "signal:") {
		t.Fatalf("signal death => %+v, want a signal description", res)
	}

	if _, err := runner.Run(t.Context(), "true", "/nonexistent-workspace-for-runner-test", nil, &out); err == nil {
		t.Fatal("unstartable command must return a Go error")
	}
}

// A process that exits 0 while a descendant keeps the output pipe open
// trips WaitDelay — the error is not an *exec.ExitError, so it lands in
// ProcessExit.Err and the tool renders stdlib plumbing as a command
// error on a successful run. The leader's real exit is 0.
func TestLocalRunnerStrayPipeHolderIsNotAnError(t *testing.T) {
	runner := NewLocalRunner(nil)
	var out bytes.Buffer
	res, err := runner.Run(t.Context(), "sleep 6 & echo done", "", nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 || res.Signal != "" || res.Err != nil {
		t.Fatalf("clean exit with a stray pipe holder => %+v", res)
	}
	if !strings.Contains(out.String(), "done") {
		t.Fatalf("output = %q", out.String())
	}
}

// stdout and stderr share one writer, so the merged text keeps arrival
// order rather than concatenating the streams.
func TestLocalRunnerMergesStreams(t *testing.T) {
	var out bytes.Buffer
	if _, err := NewLocalRunner(nil).Run(t.Context(), "echo o1; echo e1 >&2; echo o2", "", nil, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	i, j, k := strings.Index(got, "o1"), strings.Index(got, "e1"), strings.Index(got, "o2")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Fatalf("merged stream order wrong: %q", got)
	}
}
