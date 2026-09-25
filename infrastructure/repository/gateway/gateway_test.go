package repogateway

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	reposecret "github.com/jahrulnr/llm-base/infrastructure/repository/secret"
)

func newRepo(t *testing.T) drepo.Gateway {
	t.Helper()
	return NewGatewayRepository(t.TempDir(), nil)
}

func TestSaveLoadRoundtrip(t *testing.T) {
	repo := newRepo(t)
	g, err := aggregate.NewGateway("openrouter", "OpenRouter", "https://openrouter.ai/api", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	g.SetApiKey("sk-or-literal")

	m, _ := entity.NewModel("claude-opus-4", "openrouter")
	m.SetTemperature(0.7)
	m.SetMaxOutputTokens(4096)
	m.SetEffort(enum.EffortHigh)
	m.SetModelType(enum.ModelTypeText)
	m.SetContextWindow(200_000)
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
		gm.ContextWindow() != 200_000 {
		t.Fatalf("model fields mismatch: %+v", gm)
	}
	if !gm.AcceptsInput(enum.ModalityImage) || gm.AcceptsInput(enum.ModalityVideo) {
		t.Fatal("modalities mismatch")
	}
	if gm.Pricing().InputPerM() != 15 {
		t.Fatal("pricing lost")
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
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, _ := aggregate.NewGateway("g", "n", "https://x", enum.StyleMessages)
			_ = repo.Save(t.Context(), &g)
		}()
	}
	wg.Wait()
	if _, err := repo.Load(t.Context(), "g"); err != nil {
		t.Fatalf("concurrent saves left unreadable file: %v", err)
	}
}
