package appgateway

import (
	"os"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
)

func newApp(t *testing.T) (*GatewayApp, drepo.SecretStore, string) {
	t.Helper()
	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	dir := t.TempDir()
	repo := repogateway.NewGatewayRepository(dir, secrets)
	return New(repo, secrets, nil), secrets, dir
}

//go:fix inline
func strptr(s string) *string { return new(s) }

func TestUpsertGetRoundtrip(t *testing.T) {
	app, secrets, dir := newApp(t)
	off := false
	maxOut := 4096
	in := GatewayInput{
		ID:       "openrouter",
		Name:     "OpenRouter",
		Style:    enum.StyleCompletion,
		Endpoint: "https://openrouter.ai/api",
		ApiKey:   new("sk-or-literal"),
		Stream:   &off,
		Models: []ModelInput{{
			ID:              "claude-opus-4",
			MaxOutputTokens: &maxOut,
			Effort:          enum.EffortHigh,
			ModelType:       enum.ModelTypeText,
			Style:           enum.StyleMessages,
			ContextWindow:   200_000,
			InputModalities: []enum.Modality{enum.ModalityText, enum.ModalityImage},
			Pricing:         &valueobject.Pricing{},
		}},
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	// The literal vaulted under the canonical name; the record carries
	// only its "secret:" reference.
	got, err := secrets.Get(t.Context(), "gateway/openrouter/api_key")
	if err != nil || got != "sk-or-literal" {
		t.Fatalf("vault = %q, %v", got, err)
	}
	raw, _ := os.ReadFile(dir + "/openrouter.json")
	if strings.Contains(string(raw), "sk-or-literal") ||
		!strings.Contains(string(raw), "secret:gateway/openrouter/api_key") {
		t.Fatalf("record must carry the key name only, got: %s", raw)
	}

	info, err := app.Get(t.Context(), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if info.Endpoint != in.Endpoint || info.Style != enum.StyleCompletion ||
		info.KeyRef != "secret:gateway/openrouter/api_key" || info.Stream {
		t.Fatalf("info mismatch: %+v", info)
	}
	if len(info.Models) != 1 || info.Models[0].ID() != "claude-opus-4" {
		t.Fatalf("models mismatch: %+v", info.Models)
	}
	m := info.Models[0]
	if m.Effort() != enum.EffortHigh || m.ModelType() != enum.ModelTypeText ||
		m.Style() != enum.StyleMessages ||
		m.ContextWindow() != 200_000 || !m.AcceptsInput(enum.ModalityImage) ||
		m.MaxOutputTokens() == nil || *m.MaxOutputTokens() != 4096 {
		t.Fatalf("model fields mismatch: %+v", m)
	}
	// The read-modify-write codec must carry the pin — a projection-based
	// rewrite (rename, key rotate) must never silently drop it.
	if got := InputFromInfo(info).Models[0].Style; got != enum.StyleMessages {
		t.Fatalf("InputFromInfo dropped the style pin: %q", got)
	}
}

// ApiKey nil keeps the stored credential — and keeps it as the stored
// ref, so an env: indirection survives an unrelated rename instead of
// being pinned to the value it resolved to that day.
func TestUpsertKeepKeyPreservesEnvRef(t *testing.T) {
	app, _, _ := newApp(t)
	t.Setenv("TEST_GW_KEY", "sk-from-env")
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleMessages,
		Endpoint: "https://x", ApiKey: new("env:TEST_GW_KEY"),
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	in.ApiKey = nil // keep — rename only
	in.Name = "renamed"
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	info, err := app.Get(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if info.KeyRef != "env:TEST_GW_KEY" {
		t.Fatalf("keep flattened env ref to %q", info.KeyRef)
	}
	if info.Name != "renamed" {
		t.Fatal("rename lost")
	}
}

// Rotation writes the new literal into the same vault entry; a pointer
// to "" deletes the vault value along with the record's ref.
func TestUpsertRotateAndClearKey(t *testing.T) {
	app, secrets, _ := newApp(t)
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleMessages,
		Endpoint: "https://x", ApiKey: new("sk-v1"),
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	in.ApiKey = new("sk-v2")
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(t.Context(), "gateway/g/api_key"); got != "sk-v2" {
		t.Fatalf("rotated vault = %q", got)
	}
	info, _ := app.Get(t.Context(), "g")
	if info.KeyRef != "secret:gateway/g/api_key" {
		t.Fatalf("rotated key ref = %q", info.KeyRef)
	}

	in.ApiKey = new("")
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	info, _ = app.Get(t.Context(), "g")
	if info.KeyRef != "" {
		t.Fatalf("cleared key still has ref %q", info.KeyRef)
	}
	if _, err := secrets.Get(t.Context(), "gateway/g/api_key"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("cleared key still in vault")
	}
}

func TestUpsertValidation(t *testing.T) {
	app, _, _ := newApp(t)
	base := GatewayInput{ID: "g", Name: "n", Endpoint: "https://x"}

	bad := base
	bad.Style = enum.Style("bogus")
	if _, err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("bad style → %v, want 400", err)
	}

	bad = base
	bad.Style = enum.StyleMessages
	bad.Endpoint = ""
	if _, err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("missing endpoint → %v, want 400", err)
	}

	bad = base
	bad.Style = enum.StyleMessages
	bad.Models = []ModelInput{{ID: "a"}, {ID: "a"}}
	if _, err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("dup model ids → %v, want 400", err)
	}

	bad = base
	bad.Style = enum.StyleMessages
	bad.Models = []ModelInput{{ID: "a", InputModalities: []enum.Modality{enum.Modality("bogus")}}}
	if _, err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("bad modality → %v, want 400", err)
	}

	bad = base
	bad.Style = enum.StyleMessages
	bad.Models = []ModelInput{{ID: "a", Style: enum.Style("bogus")}}
	if _, err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("bad model style → %v, want 400", err)
	}
}

func TestList(t *testing.T) {
	app, _, _ := newApp(t)
	for _, id := range []string{"b", "a"} {
		in := GatewayInput{ID: id, Name: "n", Style: enum.StyleMessages, Endpoint: "https://x"}
		if _, err := app.Upsert(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	list, err := app.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Fatalf("List = %+v", list)
	}
}

// A literal sitting in the record (hand-edited file) is key material —
// it must not cross the projection. KeyRef collapses to "" while KeySet
// still reports the credential exists.
func TestGetSanitizesStoredLiteral(t *testing.T) {
	app, _, dir := newApp(t)
	raw := `{"id":"lit","style":"messages","endpoint":"https://x","apiKey":"sk-plain-in-file"}`
	if err := os.WriteFile(dir+"/lit.json", []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := app.Get(t.Context(), "lit")
	if err != nil {
		t.Fatal(err)
	}
	if info.KeyRef != "" || !info.KeySet || info.Broken {
		t.Fatalf("stored literal must show KeySet only: %+v", info)
	}
}

// Switching an auto-vaulted key to an env: ref must drop the canonical
// vault entry — the rotated-away material can't linger in the store.
func TestTransitionToEnvCleansVault(t *testing.T) {
	app, secrets, _ := newApp(t)
	t.Setenv("TEST_GW_ENV", "sk-env")
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleMessages,
		Endpoint: "https://x", ApiKey: new("sk-v1"),
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	in.ApiKey = new("env:TEST_GW_ENV")
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(t.Context(), "gateway/g/api_key"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("transition to env: left vault entry behind")
	}
	info, _ := app.Get(t.Context(), "g")
	if info.KeyRef != "env:TEST_GW_ENV" || info.Broken {
		t.Fatalf("info after transition: %+v", info)
	}
}

// A "secret:<key>" value is a caller-managed indirection — stored
// verbatim, no canonical vault write — and Broken reflects whether it
// resolves, so an unset env var or missing secret shows instead of
// hiding the record.
func TestSecretRefPassthroughAndBroken(t *testing.T) {
	app, secrets, _ := newApp(t)
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleMessages,
		Endpoint: "https://x", ApiKey: new("secret:custom/key"),
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	info, err := app.Get(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if info.KeyRef != "secret:custom/key" || !info.Broken {
		t.Fatalf("unresolvable secret ref must mark Broken: %+v", info)
	}
	if _, err := secrets.Get(t.Context(), "gateway/g/api_key"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("caller-managed ref wrote to the canonical vault key")
	}
	if err := secrets.Set(t.Context(), "custom/key", "v"); err != nil {
		t.Fatal(err)
	}
	info, _ = app.Get(t.Context(), "g")
	if info.Broken {
		t.Fatal("resolved ref still marked Broken")
	}
}

// A gateway whose ref can't resolve still renders — fields and all —
// marked Broken, and a file that can't even decode lists as a stub.
func TestBrokenGatewaysSurface(t *testing.T) {
	app, _, dir := newApp(t)
	in := GatewayInput{
		ID: "sick", Name: "n", Style: enum.StyleMessages,
		Endpoint: "https://x", ApiKey: new("env:DEFINITELY_UNSET_APP"),
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/dead.json", []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := app.Get(t.Context(), "sick")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Broken || info.Endpoint != "https://x" {
		t.Fatalf("unresolvable key should render as Broken, not error: %+v", info)
	}

	list, err := app.List(t.Context())
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	var dead GatewayInfo
	for _, gi := range list {
		if gi.ID == "dead" {
			dead = gi
		}
	}
	if dead.ID != "dead" || !dead.Broken {
		t.Fatalf("corrupt file must list as Broken stub: %+v", list)
	}
}

// Ref schemes with no name after the prefix fail at write, not at the
// next Load.
func TestEmptyRefRejected(t *testing.T) {
	app, _, _ := newApp(t)
	for _, key := range []string{"env:", "secret:"} {
		in := GatewayInput{
			ID: "g", Name: "n", Style: enum.StyleMessages,
			Endpoint: "https://x", ApiKey: &key,
		}
		if _, err := app.Upsert(t.Context(), in); apperror.As(err).StatusCode() != 400 {
			t.Fatalf("apiKey %q → %v, want 400", key, err)
		}
	}
}

func TestGetDeleteNotFound(t *testing.T) {
	app, _, _ := newApp(t)
	if _, err := app.Get(t.Context(), "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Get missing → %v", err)
	}
	if err := app.Delete(t.Context(), "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Delete missing → %v", err)
	}

	in := GatewayInput{ID: "g", Name: "n", Style: enum.StyleMessages, Endpoint: "https://x"}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(t.Context(), "g"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Get(t.Context(), "g"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("deleted gateway still gets")
	}
}

// Server-owned hidden fields: optionalContent never crosses a transport,
// so a full-document upsert must carry it forward for surviving model
// ids. nil keeps the stored map, a non-nil map overrides, a dropped id
// loses it with the record.
func TestUpsertPreservesOptionalContent(t *testing.T) {
	app, _, _ := newApp(t)
	ctx := t.Context()

	// First upsert sets the hidden map directly (the CLI path).
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleMessages, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m1", OptionalContent: map[string]string{
			"gemini.thought_signature": "sig-1"}}},
	}
	if _, err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}

	// A full replace omitting the field preserves it.
	in.Models = []ModelInput{{ID: "m1"}}
	if _, err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	info, err := app.Get(ctx, "g")
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Models[0].OptionalContent()["gemini.thought_signature"]; got != "sig-1" {
		t.Fatalf("surviving model lost hidden content: %v", info.Models[0].OptionalContent())
	}

	// A non-nil map is an explicit override — an empty one clears.
	in.Models = []ModelInput{{ID: "m1", OptionalContent: map[string]string{}}}
	if _, err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	info, _ = app.Get(ctx, "g")
	if len(info.Models[0].OptionalContent()) != 0 {
		t.Fatalf("explicit empty map must override stored: %v", info.Models[0].OptionalContent())
	}

	// Dropping the model id drops its hidden content with the record;
	// re-adding the id later starts empty.
	in.Models = []ModelInput{{ID: "m1", OptionalContent: map[string]string{
		"gemini.thought_signature": "sig-2"}}, {ID: "m2"}}
	if _, err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Models = []ModelInput{{ID: "m2"}} // m1's record is gone here
	if _, err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Models = []ModelInput{{ID: "m2"}, {ID: "m1"}}
	if _, err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	info, _ = app.Get(ctx, "g")
	for _, m := range info.Models {
		if m.ID() == "m1" && len(m.OptionalContent()) != 0 {
			t.Fatalf("re-added model must not resurrect dropped content: %v",
				m.OptionalContent())
		}
	}
}

// An endpoint is the base URL every provider call is built from. Saving
// "not a url" succeeds today (only emptiness is checked) and every later
// send fails as a generic network error; the UI even says "saved."
// Reject a non-absolute, non-http(s) endpoint at write time.
func TestUpsertRejectsMalformedEndpoint(t *testing.T) {
	app, _, _ := newApp(t)
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion,
		Endpoint: "not a url",
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("malformed endpoint → %v, want 400", err)
	}
}

// Generation parameters are provider contracts. A negative temperature
// is never valid and a probability outside [0,1] is not a TopP; accepting
// them stores a gateway that can only fail at send time. Reject at write.
func TestUpsertRejectsOutOfRangeModelParams(t *testing.T) {
	app, _, _ := newApp(t)
	neg := -0.5
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", Temperature: &neg}},
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("negative temperature → %v, want 400", err)
	}
	over := 1.5
	in.Models = []ModelInput{{ID: "m", TopP: &over}}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("TopP > 1 → %v, want 400", err)
	}
}

// Temperature has an upper bound as well as a lower one. Providers cap it
// at 2 (some at 1); storing 100 guarantees a rejected request on every
// send. The negative-side check alone leaves this the other half.
func TestUpsertRejectsExcessiveTemperature(t *testing.T) {
	app, _, _ := newApp(t)
	hot := 100.0
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", Temperature: &hot}},
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("temperature 100 → %v, want 400", err)
	}
}

// A model cannot reserve more output tokens than its whole context
// window. The budgeting code subtracts OutputLimit from ContextWindow;
// storing 2k output over a 1k window leaves no room for input and makes
// every turn compact/thrash before the provider even sees it.
func TestUpsertRejectsOutputLimitAboveContext(t *testing.T) {
	app, _, _ := newApp(t)
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", ContextWindow: 1000, OutputLimit: 2000}},
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("output limit above context → %v, want 400", err)
	}
}

// Presence/frequency penalties are bounded knobs too (OpenAI-style
// providers use [-2, 2]). A value of 100 is impossible on every surface
// this gateway can target; storing it guarantees a provider rejection.
func TestUpsertRejectsAbsurdPenalty(t *testing.T) {
	app, _, _ := newApp(t)
	penalty := 100.0
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", PresencePenalty: &penalty}},
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("presence penalty 100 → %v, want 400", err)
	}
}

// MaxOutputTokens is the per-request output cap. It cannot exceed the
// whole context window either; the previous cross-check covers only the
// catalog OutputLimit, leaving a request cap that asks for more tokens
// than the model can hold.
func TestUpsertRejectsMaxOutputTokensAboveContext(t *testing.T) {
	app, _, _ := newApp(t)
	cw, maxOut := 1000, 2000
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", ContextWindow: cw, MaxOutputTokens: &maxOut}},
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("maxOutputTokens above context → %v, want 400", err)
	}
}

// The catalog's OutputLimit is the provider's maximum response length;
// MaxOutputTokens is the per-request cap we ask for. Asking for more
// than the declared maximum is impossible and should be rejected at
// write time instead of on every send.
func TestUpsertRejectsMaxOutputTokensAboveOutputLimit(t *testing.T) {
	app, _, _ := newApp(t)
	maxOut := 8000
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", ContextWindow: 8192, OutputLimit: 4096, MaxOutputTokens: &maxOut}},
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("maxOutputTokens above outputLimit → %v, want 400", err)
	}
}

// Adapters append their own path (/chat/completions, /messages,
// /models/…) to the base endpoint. A query or fragment on the stored
// base therefore ends up in the wrong place ("...?api-version=x/
// chat/completions"), so a save that looks valid is broken on every
// send. Reject query/fragment bases until an adapter can merge them.
func TestUpsertRejectsEndpointQueryOrFragment(t *testing.T) {
	app, _, _ := newApp(t)
	base := GatewayInput{ID: "g", Name: "n", Style: enum.StyleCompletion}
	for _, ep := range []string{
		"https://example.test/v1?api-version=2024-01-01",
		"https://example.test/v1#frag",
	} {
		base.Endpoint = ep
		if _, err := app.Upsert(t.Context(), base); err == nil || apperror.As(err).StatusCode() != 400 {
			t.Fatalf("endpoint %q → %v, want 400", ep, err)
		}
	}
}

// MaxOutputTokens is sent verbatim as the request's max_tokens whenever
// the pointer is non-nil. A pointer to zero is therefore a request to
// cap the reply at zero tokens, which every provider rejects — the
// editor's "0" (and any API client) must not become a stored request
// that can never succeed. Nil is the unset sentinel; zero is invalid.
func TestUpsertRejectsZeroMaxOutputTokens(t *testing.T) {
	app, _, _ := newApp(t)
	zero := 0
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", MaxOutputTokens: &zero}},
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("maxOutputTokens=0 → %v, want 400", err)
	}
}

// TopK is a positive cutoff on a provider's token candidate set — an
// explicit zero cannot mean "no tokens" or "provider default" while
// still crossing the wire as top_k:0 (Gemini's range starts at 1, and
// OpenAI-compatible routes treat a non-nil top_k as a request). Write
// must reject it or normalize it to unset, never store a zero that
// every send forwards.
func TestUpsertZeroTopKDoesNotBecomeAWireZero(t *testing.T) {
	app, _, _ := newApp(t)
	zero := 0
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion, Endpoint: "https://x",
		Models: []ModelInput{{ID: "m", TopK: &zero}},
	}
	if _, err := app.Upsert(t.Context(), in); err != nil {
		return // rejecting the zero is the other safe outcome
	}
	info, err := app.Get(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	var top *int
	found := false
	for i := range info.Models {
		if info.Models[i].ID() == "m" {
			top, found = info.Models[i].TopK(), true
			break
		}
	}
	if !found {
		t.Fatal("model missing")
	}
	if top != nil && *top == 0 {
		t.Fatalf("stored topK=0 would be sent as a provider cutoff")
	}
}

// A syntactically valid http(s) URL can still carry a port outside the
// TCP range. url.Parse accepts it, but every send fails at dial time —
// the same "saved but unusable" failure the endpoint shape validation
// exists to catch. Reject the port at write time.
func TestUpsertRejectsOutOfRangeEndpointPort(t *testing.T) {
	app, _, _ := newApp(t)
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion,
		Endpoint: "https://example.test:99999",
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("out-of-range endpoint port → %v, want 400", err)
	}
}

// A trailing colon names an empty port; url.Parse accepts it but the
// request address cannot be dialed. It is the same unusable base the
// range check rejects, just represented as "no port" rather than a
// number.
func TestUpsertRejectsEmptyEndpointPort(t *testing.T) {
	app, _, _ := newApp(t)
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion,
		Endpoint: "https://example.test:",
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("empty endpoint port → %v, want 400", err)
	}
}

// Credentials in an endpoint URL are not a credential reference — they
// are secret material embedded in the stored, browser-projected base URL.
// The gateway has a first-class ApiKey field for that; reject userinfo in
// the endpoint so a password cannot leak through settings/UI/logs.
func TestUpsertRejectsEndpointUserInfo(t *testing.T) {
	app, _, _ := newApp(t)
	in := GatewayInput{
		ID: "g", Name: "n", Style: enum.StyleCompletion,
		Endpoint: "https://user:secret@example.test/v1",
	}
	if _, err := app.Upsert(t.Context(), in); err == nil || apperror.As(err).StatusCode() != 400 {
		t.Fatalf("endpoint userinfo → %v, want 400", err)
	}
}
