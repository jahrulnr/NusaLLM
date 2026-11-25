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
	for _, v := range []FinishReason{FinishReasonStop, FinishReasonToolCalls, FinishReasonLength, FinishReasonContentFilter, FinishReasonInterrupted} {
		if got, f := ParseFinishReason(v.String()); !f || got != v {
			t.Fatalf("FinishReason %q roundtrip failed", v)
		}
	}
}

// Provider identity branches wire behavior — OpenCode's session header
// carries a conversation id to the endpoint's host. A substring match
// cannot be that gate: a lookalike host (notopencode.ai), a sibling
// subdomain of an unrelated domain (openrouter.ai.evil.com), or the
// provider string appearing in a URL path all contain the literal but
// are different origins — matching them sends provider-scoped headers
// and quirks to the wrong host.
func TestMatchesEndpointMatchesHostOnly(t *testing.T) {
	truthy := []struct {
		p Provider
		e string
	}{
		{OpenRouter, "https://openrouter.ai/api/v1"},
		{OpenRouter, "HTTPS://OPENROUTER.AI/"}, // DNS hosts are case-insensitive
		{OpenAI, "https://api.openai.com/v1/chat/completions"},
	}
	for _, c := range truthy {
		if !c.p.MatchesEndpoint(c.e) {
			t.Fatalf("%s must match %q", c.p, c.e)
		}
	}
	falsy := []struct {
		p Provider
		e string
	}{
		{OpenCode, "https://notopencode.ai/v1"},               // host suffix ≠ host
		{OpenCode, "https://opencode.ai.evil.example.com"},    // subdomain of a foreign domain
		{OpenRouter, "https://gw.corp.example/openrouter.ai"}, // provider string in the path
		{OpenRouter, "https://openrouter.ai.proxy.internal"},
	}
	for _, c := range falsy {
		if c.p.MatchesEndpoint(c.e) {
			t.Fatalf("%s must not match %q — identity is the host, not a substring", c.p, c.e)
		}
	}
}

// Older callers parse persisted provider ids; unknown values must fail
// the parse rather than mint a provider.
func TestParseProviderRejectsUnknown(t *testing.T) {
	for _, v := range []Provider{OpenAI, OpenRouter, OpenCode, Anthropic, Deepseek, Gemini, GLM, TokenRouter} {
		if _, ok := ParseProvider(v.String()); !ok {
			t.Fatalf("ParseProvider(%q) rejected a known provider", v)
		}
	}
	if _, ok := ParseProvider("bogus"); ok {
		t.Fatal("ParseProvider accepted an unknown provider")
	}
}

func TestParseRoundtripsRemaining(t *testing.T) {
	for _, v := range []TurnEventKind{TurnEventDelta, TurnEventReasoning, TurnEventReasoningSummary, TurnEventToolCall, TurnEventToolOutput, TurnEventToolResult, TurnEventFinished, TurnEventError} {
		if got, f := ParseTurnEventKind(v.String()); !f || got != v {
			t.Fatalf("TurnEventKind %q roundtrip failed", v)
		}
	}
	for _, v := range []Effort{EffortDefault, EffortNone, EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax} {
		if got, f := ParseEffort(v.String()); !f || got != v {
			t.Fatalf("Effort %q roundtrip failed", v)
		}
	}
	for _, v := range []ModelType{ModelTypeText, ModelTypeEmbedding, ModelTypeVideo, ModelTypeImage, ModelTypeSpeech, ModelTypeTranscription, ModelTypeAudio, ModelTypeRerank, ModelTypeDecision, ModelTypeRealtime} {
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
