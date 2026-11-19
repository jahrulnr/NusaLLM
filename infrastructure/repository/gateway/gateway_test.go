package repogateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
)

//go:fix inline
func ptr[T any](v T) *T { return new(v) }

func newRepo(t *testing.T) drepo.Gateway {
	t.Helper()
	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	return NewGatewayRepository(t.TempDir(), secrets)
}

func TestSaveLoadRoundtrip(t *testing.T) {
	repo := newRepo(t)
	g, err := aggregate.NewGateway("openrouter", "OpenRouter", "https://openrouter.ai/api", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	g.SetApiKey("sk-or-literal")

	m, _ := entity.NewModel("claude-opus-4", "openrouter")
	m.SetTemperature(new(0.7))
	m.SetMaxOutputTokens(new(4096))
	m.SetEffort(enum.EffortHigh)
	m.SetModelType(enum.ModelTypeText)
	m.SetStyle(enum.StyleMessages)
	m.SetContextWindow(200_000)
	m.SetName("Claude Opus 4")
	m.SetCanonical("anthropic/claude-opus-4-20250514")
	m.SetAliasOf("anthropic/claude-opus-4")
	m.SetOutputLimit(32_000)
	m.SetSupportedEfforts([]enum.Effort{enum.EffortLow, enum.EffortMax})
	m.SetInputModalities([]enum.Modality{enum.ModalityText, enum.ModalityImage})
	p, _ := valueobject.NewPricing(15, 75, 1.5, 18.75)
	m.SetPricing(p)
	m.SetOptionalContent("openrouter.provider_order", "anthropic")
	g.SetModels([]entity.Model{m})

	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Load(t.Context(), "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint() != "https://openrouter.ai/api" || got.Style() != enum.StyleCompletion ||
		got.ApiKey() != "sk-or-literal" || !got.StreamMode() {
		t.Fatalf("gateway fields mismatch: %+v", got)
	}
	gm, ok := got.Model("claude-opus-4")
	if !ok {
		t.Fatal("catalog model missing")
	}
	if *gm.Temperature() != 0.7 || *gm.MaxOutputTokens() != 4096 ||
		gm.Effort() != enum.EffortHigh || gm.ModelType() != enum.ModelTypeText ||
		gm.Style() != enum.StyleMessages || gm.ContextWindow() != 200_000 {
		t.Fatalf("model fields mismatch: %+v", gm)
	}
	if !gm.AcceptsInput(enum.ModalityImage) || gm.AcceptsInput(enum.ModalityVideo) {
		t.Fatal("modalities mismatch")
	}
	if gm.Pricing().InputPerM() != 15 {
		t.Fatal("pricing lost")
	}
	if gm.Name() != "Claude Opus 4" || gm.Canonical() != "anthropic/claude-opus-4-20250514" ||
		gm.AliasOf() != "anthropic/claude-opus-4" ||
		gm.OutputLimit() != 32_000 || len(gm.SupportedEfforts()) != 2 ||
		gm.SupportedEfforts()[1] != enum.EffortMax {
		t.Fatalf("catalog metadata lost: %+v", gm)
	}
	if gm.OptionalContent()["openrouter.provider_order"] != "anthropic" {
		t.Fatal("optionalContent lost")
	}
}

// "env:NAME" keys resolve from the environment at Load — the file itself
// stays credential-free.
func TestApiKeyEnvRef(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("TEST_GW_KEY", "secret-from-env")
	g, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
	g.SetApiKey("env:TEST_GW_KEY")
	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}

	// File stores the ref, not the secret.
	raw, _ := os.ReadFile(repo.(*GatewayRepository).file("g"))
	if string(raw) == "" || strings.Contains(string(raw), "secret-from-env") {
		t.Fatal("apiKey leaked into storage")
	}
	got, err := repo.Load(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if got.ApiKey() != "secret-from-env" {
		t.Fatal("env ref not resolved")
	}
}

// A literal key with no SecretStore is a deployment defect — refuse to
// persist instead of silently writing plaintext into the JSON file.
func TestApiKeyLiteralNeedsStore(t *testing.T) {
	repo := NewGatewayRepository(t.TempDir(), nil)
	g, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
	g.SetApiKey("sk-literal")
	if err := repo.Save(t.Context(), &g); apperror.As(err).StatusCode() != 500 {
		t.Fatalf("literal key without secret store must fail: %v", err)
	}
}

// With a SecretStore injected, a literal apiKey is vaulted at Save and the
// file only carries its "secret:" reference — Load resolves it back.
func TestApiKeySecretRef(t *testing.T) {
	dir := t.TempDir()
	secrets, err := reposecret.New(filepath.Join(dir, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewGatewayRepository(dir, secrets)

	g, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
	g.SetApiKey("sk-real-key")
	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(dir + "/g.json")
	if strings.Contains(string(raw), "sk-real-key") {
		t.Fatal("plaintext key leaked into storage")
	}
	if !strings.Contains(string(raw), "secret:gateway/g/api_key") {
		t.Fatalf("expected secret ref in file, got: %s", raw)
	}

	got, err := repo.Load(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if got.ApiKey() != "sk-real-key" {
		t.Fatal("secret ref not resolved")
	}

	// Rotation: re-save a new literal → same ref, new stored value.
	g2, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
	g2.SetApiKey("sk-rotated")
	if err := repo.Save(t.Context(), &g2); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.Load(t.Context(), "g")
	if got.ApiKey() != "sk-rotated" {
		t.Fatal("rotated key not resolved")
	}

	// Delete cascades into the store.
	if err := repo.Delete(t.Context(), "g"); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(t.Context(), "gateway/g/api_key"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("secret orphaned after gateway delete")
	}
}

func TestListAndDelete(t *testing.T) {
	repo := newRepo(t)
	for _, id := range []string{"a", "b"} {
		g, _ := aggregate.NewGateway(id, "n", "https://x", enum.StyleMessages)
		if err := repo.Save(t.Context(), &g); err != nil {
			t.Fatal(err)
		}
	}
	list, err := repo.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID() != "a" || list[1].ID() != "b" {
		t.Fatalf("List = %+v", list)
	}
	if err := repo.Delete(t.Context(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Load(t.Context(), "a"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("deleted gateway still loads")
	}
}

func TestNotFound(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.Load(t.Context(), "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Load missing: %v", err)
	}
	if err := repo.Delete(t.Context(), "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Delete missing: %v", err)
	}
}

// Inspect reads without resolving: an unresolvable ref still loads, and a
// file that can't decode at all surfaces as a nil-Gateway snapshot rather
// than hiding — the management plane's view, unlike Load's picker view.
// KeyResolved reports the same resolution Load enforces, non-fatally.
func TestInspect(t *testing.T) {
	repo := newRepo(t)
	g, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
	g.SetApiKey("env:DEFINITELY_UNSET_INSPECT")
	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}

	snap, err := repo.Inspect(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Gateway == nil || snap.ApiKeyRef != "env:DEFINITELY_UNSET_INSPECT" ||
		snap.KeyResolved {
		t.Fatalf("Inspect = %+v", snap)
	}

	// The probe tracks the resolution source — set env flips the flag.
	t.Setenv("DEFINITELY_UNSET_INSPECT", "now-set")
	snap, err = repo.Inspect(t.Context(), "g")
	if err != nil || !snap.KeyResolved {
		t.Fatalf("resolved env ref must report KeyResolved: %+v, %v", snap, err)
	}

	if err := os.WriteFile(repo.(*GatewayRepository).file("bad"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err = repo.Inspect(t.Context(), "bad")
	if err != nil || snap.Gateway != nil || snap.ID != "bad" {
		t.Fatalf("corrupt Inspect = %+v, %v", snap, err)
	}

	list, err := repo.InspectList(t.Context())
	if err != nil || len(list) != 2 || list[1].Gateway == nil {
		t.Fatalf("InspectList = %+v, %v", list, err)
	}

	if _, err := repo.Inspect(t.Context(), "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Inspect missing: %v", err)
	}
}

// An "env:NAME" ref to an unset variable must fail fast at Load — silently
// resolving to "" turns a config error into an upstream 401.
func TestApiKeyEnvUnset(t *testing.T) {
	repo := newRepo(t)
	g, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
	g.SetApiKey("env:DEFINITELY_UNSET_VAR_XYZ")
	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Load(t.Context(), "g"); err == nil {
		t.Fatal("unset env ref must fail, not resolve to empty key")
	}
}

// Ids become filenames — separators and parent refs must be rejected.
func TestInvalidIDRejected(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.Load(t.Context(), "../escape"); err == nil ||
		apperror.As(err).StatusCode() != 400 {
		t.Fatalf("traversal id → %v, want 400", err)
	}
	g, _ := aggregate.NewGateway("../x", "n", "https://x", enum.StyleMessages)
	if err := repo.Save(t.Context(), &g); err == nil {
		t.Fatal("traversal id must not Save")
	}
}

// Concurrent Save on one id shares a .tmp path — the mutex must serialize.
func TestConcurrentSave(t *testing.T) {
	repo := newRepo(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			g, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
			_ = repo.Save(t.Context(), &g)
		})
	}
	wg.Wait()
	if _, err := repo.Load(t.Context(), "g"); err != nil {
		t.Fatalf("concurrent saves left unreadable file: %v", err)
	}
}

// List skips record-level problems (corrupt JSON) but never swallows a
// genuine I/O fault — a gateway whose file exists but can't be read
// fails the listing rather than silently vanishing. Load-vs-Delete is
// already safe by construction: rename and unlink are atomic at the name
// level, so no torn read is possible.
func TestListPropagatesUnreadableRecord(t *testing.T) {
	dir := t.TempDir()
	repo := NewGatewayRepository(dir, nil)

	rec := `{"id":"g1","style":"completion","endpoint":"https://x","stream":false}`
	if err := os.WriteFile(filepath.Join(dir, "g1.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "g2.json"), []byte(rec[:len(rec)-1]), 0o600); err != nil {
		t.Fatal(err)
	}
	// g1 exists and decodes; make it unreadable.
	if err := os.Chmod(filepath.Join(dir, "g1.json"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "g1.json"), 0o600) })

	got, err := repo.List(t.Context())
	if err == nil {
		t.Fatalf("List returned %d gateways and no error — an unreadable record must fail the listing, not hide", len(got))
	}
}

// Write validation rejects an explicit maxOutputTokens=0, but a record
// written before that rule (or repaired by hand) still loads into an
// active gateway with the zero pointer intact — every send then carries
// max_tokens:0. Load must not activate an impossible request param:
// reject the record or normalize the zero back to unset.
func TestLoadNormalizesZeroMaxOutputTokens(t *testing.T) {
	dir := t.TempDir()
	rec := `{"id":"g","style":"completion","endpoint":"https://x","stream":true,"models":[{"id":"m","maxOutputTokens":0}]}`
	if err := os.WriteFile(filepath.Join(dir, "g.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := NewGatewayRepository(dir, nil).Load(t.Context(), "g")
	if err != nil {
		return // rejecting the invalid record is also safe
	}
	m, ok := g.Model("m")
	if !ok {
		t.Fatal("model missing after load")
	}
	if v := m.MaxOutputTokens(); v != nil && *v == 0 {
		t.Fatalf("loaded gateway still sends max_tokens=0")
	}
}

// Write-time validation covers temperature, topP, penalties, and the
// context/output cross-field rule; stored records must not bypass it.
// A hand-edited or pre-rule record with impossible generation params
// otherwise becomes an active gateway that sends every request with
// values the provider rejects.
func TestLoadRejectsOrNormalizesInvalidGenerationParams(t *testing.T) {
	dir := t.TempDir()
	rec := `{
		"id":"g","style":"completion","endpoint":"https://x","stream":true,
		"models":[{"id":"m","contextWindow":8192,"outputLimit":16384,
			"temperature":5,"topP":2,"presencePenalty":3,"frequencyPenalty":-4}]
	}`
	if err := os.WriteFile(filepath.Join(dir, "g.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := NewGatewayRepository(dir, nil).Load(t.Context(), "g")
	if err != nil {
		return // rejecting the record is safe
	}
	m, ok := g.Model("m")
	if !ok {
		t.Fatal("model missing after load")
	}
	if v := m.Temperature(); v != nil && (*v < 0 || *v > 2) {
		t.Fatalf("loaded temperature=%v outside [0,2]", *v)
	}
	if v := m.TopP(); v != nil && (*v < 0 || *v > 1) {
		t.Fatalf("loaded topP=%v outside [0,1]", *v)
	}
	if v := m.PresencePenalty(); v != nil && (*v < -2 || *v > 2) {
		t.Fatalf("loaded presencePenalty=%v outside [-2,2]", *v)
	}
	if v := m.FrequencyPenalty(); v != nil && (*v < -2 || *v > 2) {
		t.Fatalf("loaded frequencyPenalty=%v outside [-2,2]", *v)
	}
	if m.ContextWindow() > 0 && m.OutputLimit() > m.ContextWindow() {
		t.Fatalf("loaded outputLimit=%d exceeds contextWindow=%d", m.OutputLimit(), m.ContextWindow())
	}
}

// A catalog is keyed by model id; Upsert rejects duplicates, but a
// hand-edited or older record can still carry them. Loading it makes
// Model(id) first-match and leaves the second row unreachable while the
// picker displays both. Load must reject the record or collapse the
// duplicate before the gateway becomes active.
func TestLoadRejectsOrCollapsesDuplicateModelIDs(t *testing.T) {
	dir := t.TempDir()
	rec := `{"id":"g","style":"completion","endpoint":"https://x","stream":true,
		"models":[{"id":"m","contextWindow":200000},{"id":"m","contextWindow":400000}]}`
	if err := os.WriteFile(filepath.Join(dir, "g.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := NewGatewayRepository(dir, nil).Load(t.Context(), "g")
	if err != nil {
		return // rejecting the record is safe
	}
	seen := map[string]bool{}
	for _, m := range g.Models() {
		if seen[m.ID()] {
			t.Fatalf("loaded gateway keeps duplicate model id %q", m.ID())
		}
		seen[m.ID()] = true
	}
}

// A record written before a validation rule — or repaired by hand — must
// not activate negative capacity limits: a negative context hides the
// model from the picker while looking "declared", and a negative image
// cap silently fails open because every check is `limit > 0`. Write
// validation rejects these values; hydrate must reject or normalize them
// just like the generation-param cases above.
func TestLoadRejectsOrNormalizesNegativeResourceLimits(t *testing.T) {
	dir := t.TempDir()
	rec := `{"id":"g","style":"completion","endpoint":"https://x","stream":true,
		"models":[{"id":"m","contextWindow":-1,"outputLimit":-2,"maxImages":-3}]}`
	if err := os.WriteFile(filepath.Join(dir, "g.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := NewGatewayRepository(dir, nil).Load(t.Context(), "g")
	if err != nil {
		return // rejecting the invalid record is also safe
	}
	m, ok := g.Model("m")
	if !ok {
		t.Fatal("model missing after load")
	}
	if m.ContextWindow() < 0 || m.OutputLimit() < 0 || m.MaxImages() < 0 {
		t.Fatalf("loaded negative limits: context=%d output=%d images=%d",
			m.ContextWindow(), m.OutputLimit(), m.MaxImages())
	}
}

type failingSecretDelete struct{}

func (failingSecretDelete) Get(context.Context, string) (string, error) { return "", nil }
func (failingSecretDelete) Set(context.Context, string, string) error   { return nil }
func (failingSecretDelete) Delete(context.Context, string) error {
	return errors.New("secret store unavailable")
}
func (failingSecretDelete) Close() error { return nil }

// Delete removes the record before its vault cleanup. If the cleanup
// fails, returning an error while the record is already gone makes the
// user retry a delete that can only answer 404. Either tolerate the
// orphaned vault entry or keep the record so the retry is meaningful.
func TestDeleteKeepsRecordWhenSecretCleanupFails(t *testing.T) {
	dir := t.TempDir()
	repo := NewGatewayRepository(dir, failingSecretDelete{})
	g, err := aggregate.NewGateway("g", "n", "https://x", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	g.SetApiKey("secret:gateway/g/api_key")
	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	err = repo.Delete(t.Context(), "g")
	if err == nil {
		return // tolerating the orphaned secret is the other safe outcome
	}
	if _, statErr := os.Stat(filepath.Join(dir, "g.json")); statErr != nil {
		t.Fatalf("delete reported failure after removing the record: %v", statErr)
	}
}

// Delete cleans the vault before removing the record. If the record
// removal then fails, the record remains with a dangling secret ref and
// can no longer be loaded. A delete failure must leave a usable record;
// cleanup order must not trade a transient filesystem error for a broken
// gateway.
func TestDeleteKeepsRecordUsableWhenFileRemovalFails(t *testing.T) {
	dir := t.TempDir()
	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755); _ = secrets.Close() })
	repo := NewGatewayRepository(dir, secrets)
	g, err := aggregate.NewGateway("g", "n", "https://x", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	g.SetApiKey("sk-live")
	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(t.Context(), "g"); err == nil {
		t.Fatal("delete succeeded despite a read-only directory")
	}
	if _, err := repo.Load(t.Context(), "g"); err != nil {
		t.Fatalf("failed delete left the record unusable: %v", err)
	}
}

type failingSecretStore struct{ drepo.SecretStore }

func (failingSecretStore) Get(context.Context, string) (string, error) {
	return "", errors.New("secret store bus down")
}

// A transient vault outage is an operational failure, not a corrupt
// gateway. Inspect's contract says genuine I/O faults propagate; treating
// it as KeyResolved=false makes every gateway appear broken during an
// outage and hides the real failure from the operator.
func TestInspectPropagatesSecretStoreOutage(t *testing.T) {
	dir := t.TempDir()
	repo := NewGatewayRepository(dir, failingSecretStore{})
	g, err := aggregate.NewGateway("g", "n", "https://x", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	g.SetApiKey("secret:gateway/g/api_key")
	if err := repo.Save(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Inspect(t.Context(), "g"); err == nil {
		t.Fatal("secret store outage was reported as a broken record, not an error")
	}
}
