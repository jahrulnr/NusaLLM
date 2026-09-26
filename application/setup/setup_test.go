package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/config"
)

func TestEnsureConfigGeneratesLoadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "config.json")
	created, err := EnsureConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("missing file must be created")
	}
	var c config.Config
	if err := c.Load(path); err != nil {
		t.Fatalf("generated file must load: %v", err)
	}
	if c.Storage.Dir != "./data" || c.Conversation.MailboxCap != 128 {
		t.Fatalf("generated file must carry tag defaults: %+v", c)
	}
}

func TestEnsureConfigLeavesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := []byte(`{"storage":{"dir":"./custom"}}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing file must not be reported as created")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatal("existing file must be left untouched")
	}
}

func TestEnsureConfigEmptyPath(t *testing.T) {
	created, err := EnsureConfig("")
	if err != nil || created {
		t.Fatal("empty path must be a no-op")
	}
}

func TestEnsureConfigRejectsExistingDirectory(t *testing.T) {
	// A directory at the config path is not a missing file — creating a
	// config inside it would silently change the operator's intent.
	path := t.TempDir()
	created, err := EnsureConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing directory must not be overwritten")
	}
}
