package toolexec

// T-27 — a process's result is data: the adapter records the verdict and the
// exit status on the ToolResult, without anyone reading the text.

import (
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

func TestExecRecordsExitAsData(t *testing.T) {
	cases := []struct {
		label   string
		command string
		outcome enum.ToolOutcome
		code    int
		signal  string
	}{
		{"clean exit", `{"command":"true"}`, enum.ToolOutcomeOK, 0, ""},
		{"failed exit", `{"command":"sh -c 'echo boom; exit 3'"}`, enum.ToolOutcomeFailed, 3, ""},
	}
	for _, tc := range cases {
		res, err := newTool(t).Execute(testContext(), mustCall(t, "o-1", tc.command, 0), nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if res.Outcome != tc.outcome {
			t.Fatalf("%s: outcome = %q, want %q (text %q)", tc.label, res.Outcome, tc.outcome, res.Text)
		}
		if res.Exit == nil {
			t.Fatalf("%s: no exit status recorded", tc.label)
		}
		if res.Exit.Code != tc.code || res.Exit.Signal != tc.signal {
			t.Fatalf("%s: exit = %+v, want code %d signal %q", tc.label, res.Exit, tc.code, tc.signal)
		}
	}
}

// The failing verdict must not depend on the text: strip the markers and the
// call is still failed, with the same exit code.
func TestExecOutcomeDoesNotReadTheText(t *testing.T) {
	res, err := newTool(t).Execute(testContext(), mustCall(t, "o-2", `{"command":"sh -c 'exit 7'"}`, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != enum.ToolOutcomeFailed || res.Exit == nil || res.Exit.Code != 7 {
		t.Fatalf("outcome = %q exit = %+v text = %q", res.Outcome, res.Exit, res.Text)
	}
}

// exec_session reports the same two facts for a polled process — the case the
// web client used to get wrong because only the header mentioned the exit.
func TestExecSessionRecordsExitAsData(t *testing.T) {
	store := NewSessionStore(nil)
	defer store.TerminateAll()
	tool, err := NewExecSessionTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := service.WithConversationID(testContext(), testConv)
	call := mustCall(t, "c1", `{"command":"sh -c 'echo boom; exit 3'","yield_time_ms":5000}`, 1<<20)
	var res service.ToolResult
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err = tool.Execute(ctx, call, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Text, "Process running with session ID") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res.Outcome != enum.ToolOutcomeFailed {
		t.Fatalf("outcome = %q, want failed (text %q)", res.Outcome, res.Text)
	}
	if res.Exit == nil || res.Exit.Code != 3 || res.Exit.Signal != "" {
		t.Fatalf("exit = %+v, want code 3", res.Exit)
	}
}

// A process that is still running has no exit yet: the call succeeded, and
// there is nothing to report as ended.
func TestExecSessionRunningHasNoExit(t *testing.T) {
	store := NewSessionStore(nil)
	defer store.TerminateAll()
	tool, err := NewExecSessionTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := service.WithConversationID(testContext(), testConv)
	res, err := tool.Execute(ctx, mustCall(t, "c1", `{"command":"sleep 60"}`, 1<<20), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != enum.ToolOutcomeOK || res.Exit != nil {
		t.Fatalf("outcome = %q exit = %+v, want ok with no exit", res.Outcome, res.Exit)
	}
}
