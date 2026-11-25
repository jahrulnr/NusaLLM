package repoconversation

// T-27 — a call's verdict and exit status persist with the result: on the
// message record, and on the late-result patch line. A record written before
// the fields existed still decodes, as "no verdict".

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

func TestToolCallOutcomeRoundTrip(t *testing.T) {
	msg, err := entity.NewMessage(enum.RoleAssistant, "")
	if err != nil {
		t.Fatal(err)
	}
	failed, _ := entity.NewToolCall("call_failed", "exec", "{}", enum.TooltypeOrigin)
	failed.SetOutput("boom\n(exit 3)")
	failed.SetOutcome(enum.ToolOutcomeFailed, &valueobject.ExitStatus{Code: 3})
	clean, _ := entity.NewToolCall("call_ok", "exec", "{}", enum.TooltypeOrigin)
	clean.SetOutput("done\n(exit 0)")
	clean.SetOutcome(enum.ToolOutcomeOK, &valueobject.ExitStatus{Code: 0})
	canceled, _ := entity.NewToolCall("call_canceled", "exec", "{}", enum.TooltypeOrigin)
	canceled.SetOutput("interrupted by user")
	canceled.SetOutcome(enum.ToolOutcomeCanceled, nil)
	for _, tc := range []entity.ToolCall{failed, clean, canceled} {
		if err := msg.AddToolCall(tc); err != nil {
			t.Fatal(err)
		}
	}

	decoded, err := decodeMessage(encodeMessage(msg), strictDecode)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]entity.ToolCall{}
	for _, tc := range decoded.ToolCalls() {
		got[tc.ID()] = tc
	}
	if o, e := got["call_failed"].Outcome(), got["call_failed"].Exit(); o != enum.ToolOutcomeFailed || e == nil || e.Code != 3 || e.Signal != "" {
		t.Fatalf("failed call = %q/%+v, want failed with code 3", o, e)
	}
	if o, e := got["call_ok"].Outcome(), got["call_ok"].Exit(); o != enum.ToolOutcomeOK || e == nil || e.Code != 0 {
		t.Fatalf("clean call = %q/%+v, want ok with code 0", o, e)
	}
	if o, e := got["call_canceled"].Outcome(), got["call_canceled"].Exit(); o != enum.ToolOutcomeCanceled || e != nil {
		t.Fatalf("canceled call = %q/%+v, want canceled with no exit", o, e)
	}
}

// A late result lands as a patch line; the verdict must ride with it, or a
// reload would see an output with no verdict.
func TestToolOutputPatchCarriesOutcome(t *testing.T) {
	call, _ := entity.NewToolCall("call_1", "exec", "{}", enum.TooltypeOrigin)
	call.SetOutput("boom\n(exit 3)")
	call.SetOutcome(enum.ToolOutcomeFailed, &valueobject.ExitStatus{Code: 3})

	line := toolOutputLine(call)
	if line.Outcome != "failed" || line.Exit == nil || line.Exit.Code != 3 {
		t.Fatalf("patch line = outcome %q exit %+v, want failed/3", line.Outcome, line.Exit)
	}
}

// A record from before the field existed has no outcome key and no exit:
// decode keeps it as "no verdict" instead of guessing from the text.
func TestToolCallRecordWithoutOutcomeDecodes(t *testing.T) {
	msg, _ := entity.NewMessage(enum.RoleAssistant, "")
	legacy, _ := entity.NewToolCall("call_legacy", "exec", "{}", enum.TooltypeOrigin)
	legacy.SetOutput("error: something broke")
	if err := msg.AddToolCall(legacy); err != nil {
		t.Fatal(err)
	}
	record := encodeMessage(msg)
	if record.ToolCalls[0].Outcome != "" || record.ToolCalls[0].Exit != nil {
		t.Fatalf("an unset verdict must encode as absent: %+v", record.ToolCalls[0])
	}
	decoded, err := decodeMessage(record, strictDecode)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.ToolCalls()[0]; got.Outcome() != "" || got.Exit() != nil {
		t.Fatalf("legacy call = %q/%+v, want no verdict", got.Outcome(), got.Exit())
	}
}
