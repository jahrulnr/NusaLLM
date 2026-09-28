package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	repolog "github.com/jahrulnr/nusallm/infrastructure/repository/log"
)

func TestRedactWithoutBodiesProviderRequest(t *testing.T) {
	dir := t.TempDir()
	l, err := repolog.New(dir, repolog.Options{Redact: redactWithoutBodies})
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
	l, err := repolog.New(dir, repolog.Options{Redact: redactWithoutBodies})
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
