package enum

import "testing"

// Parse* roundtrips pin the persisted string forms and reject unknowns.
func TestParseRoundtrips(t *testing.T) {
	ok := func(name string, v interface{ String() string }, found bool) {
		if !found {
			t.Fatalf("%s: parse of %q failed", name, v.String())
		}
	}
	for _, v := range []Role{RoleSystem, RoleUser, RoleDeveloper, RoleAssistant} {
		got, found := ParseRole(v.String())
		ok("Role", v, found)
		_ = got
	}
	for _, v := range []Style{StyleCompletion, StyleResponses, StyleMessages} {
		if _, f := ParseStyle(v.String()); !f {
			t.Fatalf("Style %q not parsed", v)
		}
	}
	for _, v := range []Tooltype{TooltypeInjection, TooltypeOrigin} {
		if got, f := ParseTooltype(v.String()); !f || got != v {
			t.Fatalf("Tooltype %q roundtrip failed", v)
		}
	}
	for _, v := range []ToolPermission{ToolPermissionSystem, ToolPermissionAgent, ToolPermissionGeneral} {
		if got, f := ParseToolPermission(v.String()); !f || got != v {
			t.Fatalf("ToolPermission %q roundtrip failed", v)
		}
	}
	for _, v := range []LLMUserType{LLMUserTypeOrigin, LLMUserTypeSystem} {
		if got, f := ParseLLMUserType(v.String()); !f || got != v {
			t.Fatalf("LLMUserType %q roundtrip failed", v)
		}
	}
	for _, v := range []FinishReason{FinishReasonStop, FinishReasonToolCalls, FinishReasonLength, FinishReasonContentFilter} {
		if got, f := ParseFinishReason(v.String()); !f || got != v {
			t.Fatalf("FinishReason %q roundtrip failed", v)
		}
	}
	for _, v := range []TurnEventKind{TurnEventDelta, TurnEventReasoning, TurnEventReasoningSummary, TurnEventToolCall, TurnEventToolOutput, TurnEventFinished, TurnEventError} {
		if got, f := ParseTurnEventKind(v.String()); !f || got != v {
			t.Fatalf("TurnEventKind %q roundtrip failed", v)
		}
	}
	for _, v := range []Effort{EffortDefault, EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax} {
		if got, f := ParseEffort(v.String()); !f || got != v {
			t.Fatalf("Effort %q roundtrip failed", v)
		}
	}
	for _, v := range []ModelType{ModelTypeText, ModelTypeEmbedding, ModelTypeVideo, ModelTypeImage, ModelTypeSpeech, ModelTypeTranscription, ModelTypeAudio, ModelTypeRerank, ModelTypeDecision} {
		if got, f := ParseModelType(v.String()); !f || got != v {
			t.Fatalf("ModelType %q roundtrip failed", v)
		}
	}
	for _, v := range []Modality{ModalityText, ModalityImage, ModalityAudio, ModalityVideo, ModalityFile} {
		if got, f := ParseModality(v.String()); !f || got != v {
			t.Fatalf("Modality %q roundtrip failed", v)
		}
	}

	if _, f := ParseRole("bogus"); f {
		t.Fatal("unknown value must not parse")
	}
}

func TestEffortIsUnset(t *testing.T) {
	if !Effort("").IsUnset() || !EffortDefault.IsUnset() {
		t.Fatal("empty and default must be unset")
	}
	if EffortHigh.IsUnset() {
		t.Fatal("high must not be unset")
	}
}
