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

func strptr(s string) *string { return &s }

func TestUpsertGetRoundtrip(t *testing.T) {
	app, secrets, dir := newApp(t)
	off := false
	maxOut := 4096
	in := GatewayInput{
		ID:       "openrouter",
		Name:     "OpenRouter",
		Style:    enum.StyleCompletion,
		Endpoint: "https://openrouter.ai/api",
		ApiKey:   strptr("sk-or-literal"),
		Stream:   &off,
		Models: []ModelInput{{
			ID:              "claude-opus-4",
			MaxOutputTokens: &maxOut,
			Effort:          enum.EffortHigh,
			ModelType:       enum.ModelTypeText,
			ContextWindow:   200_000,
			InputModalities: []enum.Modality{enum.ModalityText, enum.ModalityImage},
			Pricing:         &valueobject.Pricing{},
		}},
	}
	if err := app.Upsert(t.Context(), in); err != nil {
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
		m.ContextWindow() != 200_000 || !m.AcceptsInput(enum.ModalityImage) ||
		m.MaxOutputTokens() == nil || *m.MaxOutputTokens() != 4096 {
		t.Fatalf("model fields mismatch: %+v", m)
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
		Endpoint: "https://x", ApiKey: strptr("env:TEST_GW_KEY"),
	}
	if err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	in.ApiKey = nil // keep — rename only
	in.Name = "renamed"
	if err := app.Upsert(t.Context(), in); err != nil {
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
		Endpoint: "https://x", ApiKey: strptr("sk-v1"),
	}
	if err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}

	in.ApiKey = strptr("sk-v2")
	if err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(t.Context(), "gateway/g/api_key"); got != "sk-v2" {
		t.Fatalf("rotated vault = %q", got)
	}
	info, _ := app.Get(t.Context(), "g")
	if info.KeyRef != "secret:gateway/g/api_key" {
		t.Fatalf("rotated key ref = %q", info.KeyRef)
	}

	in.ApiKey = strptr("")
	if err := app.Upsert(t.Context(), in); err != nil {
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
	if err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("bad style → %v, want 400", err)
	}

	bad = base
	bad.Style = enum.StyleMessages
	bad.Endpoint = ""
	if err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("missing endpoint → %v, want 400", err)
	}

	bad = base
	bad.Style = enum.StyleMessages
	bad.Models = []ModelInput{{ID: "a"}, {ID: "a"}}
	if err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("dup model ids → %v, want 400", err)
	}

	bad = base
	bad.Style = enum.StyleMessages
	bad.Models = []ModelInput{{ID: "a", InputModalities: []enum.Modality{enum.Modality("bogus")}}}
	if err := app.Upsert(t.Context(), bad); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("bad modality → %v, want 400", err)
	}
}

func TestList(t *testing.T) {
	app, _, _ := newApp(t)
	for _, id := range []string{"b", "a"} {
		in := GatewayInput{ID: id, Name: "n", Style: enum.StyleMessages, Endpoint: "https://x"}
		if err := app.Upsert(t.Context(), in); err != nil {
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
		Endpoint: "https://x", ApiKey: strptr("sk-v1"),
	}
	if err := app.Upsert(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	in.ApiKey = strptr("env:TEST_GW_ENV")
	if err := app.Upsert(t.Context(), in); err != nil {
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
		Endpoint: "https://x", ApiKey: strptr("secret:custom/key"),
	}
	if err := app.Upsert(t.Context(), in); err != nil {
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
		Endpoint: "https://x", ApiKey: strptr("env:DEFINITELY_UNSET_APP"),
	}
	if err := app.Upsert(t.Context(), in); err != nil {
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
		if err := app.Upsert(t.Context(), in); apperror.As(err).StatusCode() != 400 {
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
	if err := app.Upsert(t.Context(), in); err != nil {
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
	if err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}

	// A full replace omitting the field preserves it.
	in.Models = []ModelInput{{ID: "m1"}}
	if err := app.Upsert(ctx, in); err != nil {
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
	if err := app.Upsert(ctx, in); err != nil {
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
	if err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Models = []ModelInput{{ID: "m2"}} // m1's record is gone here
	if err := app.Upsert(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Models = []ModelInput{{ID: "m2"}, {ID: "m1"}}
	if err := app.Upsert(ctx, in); err != nil {
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
