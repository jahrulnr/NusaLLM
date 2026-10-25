package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/redact"
	repolog "github.com/jahrulnr/nusallm/infrastructure/repository/log"
)

// The local driver's fallback root is the delivery's workspace root —
// never the process cwd, which a "" value would silently resolve to
// inside CheckRoot.
func TestLocalEnvironmentBindsDeliveryWorkspaceRoot(t *testing.T) {
	bundle := localEnvironment("/srv/root", nil, nil, nil)
	if got := bundle.FallbackRoot(); got != "/srv/root" {
		t.Fatalf("FallbackRoot = %q, want /srv/root", got)
	}
	if bundle.Workspace() != nil {
		t.Fatal("Workspace store should pass through untouched")
	}
}

func TestComposeRejectsEmptyWorkspaceRoot(t *testing.T) {
	if _, err := compose(config.Config{}, nil, "", ""); err == nil {
		t.Fatal("compose with an empty workspace root must fail")
	}
}

func TestComposeInstallsAndRegistersSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.NewConfig()
	cfg.Storage.Dir = t.TempDir()
	eng, err := compose(cfg, nil, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.conversations.Shutdown(t.Context()); eng.logger.Close() })
	schemas, err := eng.conversations.ToolSchemas(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	permissions := map[string]enum.ToolPermission{}
	for _, schema := range schemas {
		permissions[schema.Name()] = schema.Permission()
	}
	if permissions["skills"] != enum.ToolPermissionGeneral || permissions["read_skill"] != enum.ToolPermissionAgent || permissions["custom_tools"] != enum.ToolPermissionSystem {
		t.Fatalf("skills not registered: %v", permissions)
	}
	for _, name := range []string{"skill-creator", "tool-creator"} {
		if _, err := os.Stat(filepath.Join(cfg.Storage.Dir, "builtin-skills", name, "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
	launcher := "python3"
	if runtime.GOOS == "windows" {
		launcher += ".cmd"
	}
	if _, err := os.Stat(filepath.Join(cfg.Storage.Dir, "runtimes", "bin", launcher)); err != nil {
		t.Fatal(err)
	}
}

func TestRedactWithoutBodiesProviderRequest(t *testing.T) {
	dir := t.TempDir()
	l, err := repolog.New(dir, repolog.Options{Redact: redact.WithoutBodies})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	secret := "sk-live-123456789012345678901234567890"
	payload := json.RawMessage([]byte(`{"model":"m","messages":[{"role":"user","content":"` + secret + `"}]}`))
	if err := l.Append(t.Context(), "provider.request", payload); err != nil {
		t.Fatal(err)
	}
	l.Close()

	raw, err := os.ReadFile(filepath.Join(dir, "active.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, secret) {
		t.Fatal("request payload leaked into log")
	}
	if !strings.Contains(s, `"omitted":true`) {
		t.Fatalf("expected omitted marker, got %s", s)
	}
}

func TestRedactWithoutBodiesProviderResponseKeepsMeta(t *testing.T) {
	dir := t.TempDir()
	l, err := repolog.New(dir, repolog.Options{Redact: redact.WithoutBodies})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := l.Append(t.Context(), "provider.response", map[string]any{
		"status": 200, "request_id": "req-1", "body": json.RawMessage(`{"choices":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	raw, _ := os.ReadFile(filepath.Join(dir, "active.jsonl"))
	s := string(raw)
	if !strings.Contains(s, `"request_id":"req-1"`) || strings.Contains(s, `"choices"`) {
		t.Fatalf("response meta should survive without body: %s", s)
	}
}

func TestRedactWithoutBodiesOmitsConversationAndStream(t *testing.T) {
	dir := t.TempDir()
	l, err := repolog.New(dir, repolog.Options{Redact: redact.WithoutBodies})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		kind  string
		value any
	}{
		{"provider.request", json.RawMessage(`{"messages":[{"content":"conversation-marker"}]}`)},
		{"provider.response", map[string]any{"request_id": "req-1", "body": json.RawMessage(`{"content":"conversation-marker"}`)}},
		{"provider.error", map[string]any{"status": 400, "body": json.RawMessage(`{"content":"conversation-marker"}`)}},
		{"provider.stream", []any{map[string]any{"content": "conversation-marker"}}},
	} {
		if err := l.Append(t.Context(), entry.kind, entry.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "active.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "conversation-marker") {
		t.Fatalf("conversation content reached log: %s", raw)
	}
	if !strings.Contains(string(raw), `"request_id":"req-1"`) || !strings.Contains(string(raw), `"chunks":1`) {
		t.Fatalf("diagnostic metadata missing: %s", raw)
	}
}
