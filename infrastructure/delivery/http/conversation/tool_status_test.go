package conversation

// T-27 — the call's status is the recorded verdict, not a reading of its
// output text: pending only while no result exists, unknown only for a record
// that never carried one.

import (
	"encoding/json"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

func TestToolCallStatusProjectsTheVerdict(t *testing.T) {
	pending, _ := entity.NewToolCall("c_pending", "exec", "{}", enum.TooltypeOrigin)

	ok, _ := entity.NewToolCall("c_ok", "exec", "{}", enum.TooltypeOrigin)
	ok.SetOutput("done\n(exit 0)")
	ok.SetOutcome(enum.ToolOutcomeOK, &valueobject.ExitStatus{Code: 0})

	failed, _ := entity.NewToolCall("c_failed", "exec", "{}", enum.TooltypeOrigin)
	failed.SetOutput("boom\n(exit 3)")
	failed.SetOutcome(enum.ToolOutcomeFailed, &valueobject.ExitStatus{Code: 3})

	refused, _ := entity.NewToolCall("c_refused", "read_file", "{}", enum.TooltypeOrigin)
	refused.SetOutput("error: no such file")
	refused.SetOutcome(enum.ToolOutcomeFailed, nil)

	canceled, _ := entity.NewToolCall("c_canceled", "exec", "{}", enum.TooltypeOrigin)
	canceled.SetOutput("interrupted by user")
	canceled.SetOutcome(enum.ToolOutcomeCanceled, nil)

	legacy, _ := entity.NewToolCall("c_legacy", "exec", "{}", enum.TooltypeOrigin)
	legacy.SetOutput("error: written before the field")

	injected, _ := entity.NewToolCall("c_injected", "workspace", "{}", enum.TooltypeInjection)
	injected.SetOutput("pinned")

	cases := []struct {
		call   entity.ToolCall
		status string
		exit   string
	}{
		{pending, "pending", ""},
		{ok, "ok", `{"code":0}`},
		{failed, "failed", `{"code":3}`},
		{refused, "failed", ""},
		{canceled, "canceled", ""},
		{legacy, "unknown", ""},
		{injected, "unknown", ""},
	}
	for _, tc := range cases {
		got := toToolCall(tc.call)
		if got.Status != tc.status {
			t.Fatalf("%s: status = %q, want %q", tc.call.ID(), got.Status, tc.status)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: %v", tc.call.ID(), err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("%s: %v", tc.call.ID(), err)
		}
		exit, hasExit := obj["exit"]
		if tc.exit == "" {
			if hasExit {
				t.Fatalf("%s: exit present (%v), want absent", tc.call.ID(), exit)
			}
			continue
		}
		shaped, err := json.Marshal(exit)
		if err != nil {
			t.Fatalf("%s: %v", tc.call.ID(), err)
		}
		if string(shaped) != tc.exit {
			t.Fatalf("%s: exit = %s, want %s", tc.call.ID(), shaped, tc.exit)
		}
	}
}
