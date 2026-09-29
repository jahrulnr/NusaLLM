package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPathWorksWithInvalidStorageDir(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"storage":{"dir":123}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	var out, errs bytes.Buffer
	if code := Config([]string{"path"}, &out, &errs); code != 0 {
		t.Fatalf("config path exit %d: %s", code, errs.String())
	}
	if strings.TrimSpace(out.String()) != configPath {
		t.Fatalf("config path = %q, want %q", out.String(), configPath)
	}
}

func TestConfigUnsetRepairsInvalidStorageDir(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"storage":{"dir":123}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	var out, errs bytes.Buffer
	if code := Config([]string{"unset", "storage.dir"}, &out, &errs); code != 0 {
		t.Fatalf("config unset exit %d: %s", code, errs.String())
	}
	cfg, _, err := LoadConfig(true)
	if err != nil {
		t.Fatalf("repaired config still invalid: %v", err)
	}
	if cfg.Storage.Dir != "./data" {
		t.Fatalf("storage.dir = %q, want default", cfg.Storage.Dir)
	}
}

func TestLoadConfigLenientNormalizesMissingStorageDir(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"service":{"idle_timeout":"invalid"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	cfg, _, err := LoadConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Dir != "./data" {
		t.Fatalf("storage.dir = %q, want normalized default", cfg.Storage.Dir)
	}
}

func TestLoadConfigStrictRejectsInvalidFile(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"service":{"idle_timeout":"invalid"},"storage":{"dir":"/custom"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	if _, _, err := LoadConfig(true); err == nil {
		t.Fatal("strict load must not continue with an invalid config")
	}
}

func TestLoadConfigLenientDoesNotRetainPartiallyDecodedFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"server":{"port":70000},"service":{"idle_timeout":"invalid"},"storage":{"dir":"/custom"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	cfg, _, err := LoadConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Dir != "/custom" || cfg.Server.Port != 10998 {
		t.Fatalf("lenient config retained unrelated invalid fields: %+v", cfg)
	}
}

func TestConfigShowAndValidateUseFileContents(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"storage":{"dir":"/chosen"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	var out, errs bytes.Buffer
	if code := Config([]string{"show"}, &out, &errs); code != 0 || !strings.Contains(out.String(), `"dir": "/chosen"`) {
		t.Fatalf("show exit %d, out=%q, err=%q", code, out.String(), errs.String())
	}
	out.Reset()
	errs.Reset()
	if code := Config([]string{"validate"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "is valid") {
		t.Fatalf("validate exit %d, out=%q, err=%q", code, out.String(), errs.String())
	}
	if err := os.WriteFile(configPath, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if code := Config([]string{"validate"}, &out, &errs); code != 1 || !strings.Contains(errs.String(), "JSON object") {
		t.Fatalf("invalid validate exit %d, out=%q, err=%q", code, out.String(), errs.String())
	}
	out.Reset()
	errs.Reset()
	if code := Config([]string{"show"}, &out, &errs); code != 1 {
		t.Fatalf("invalid show exit %d, out=%q, err=%q", code, out.String(), errs.String())
	}
}

func TestLoadConfigDefaultsForUnsetOrMissingPath(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CONFIG_PATH", "")
	cfg, path, err := LoadConfig(true)
	if err != nil || path != "" || cfg.Storage.Dir != "./data" {
		t.Fatalf("unset path: cfg=%+v path=%q err=%v", cfg, path, err)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	t.Setenv("CONFIG_PATH", missing)
	cfg, path, err = LoadConfig(true)
	if err != nil || path != missing || cfg.Storage.Dir != "./data" {
		t.Fatalf("missing file: cfg=%+v path=%q err=%v", cfg, path, err)
	}
}

func TestConfigPathExportedEnvWinsOverDotEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("CONFIG_PATH=/from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", "/exported")
	var out, errs bytes.Buffer
	if code := Config([]string{"path"}, &out, &errs); code != 0 || strings.TrimSpace(out.String()) != "/exported" {
		t.Fatalf("path exit %d, out=%q, err=%q", code, out.String(), errs.String())
	}
}

func TestConfigPathWithoutConfiguredFileExplainsDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CONFIG_PATH", "")
	var out, errs bytes.Buffer
	if code := Config([]string{"path"}, &out, &errs); code != 0 || strings.TrimSpace(out.String()) != "(no config file — running on defaults)" || errs.Len() != 0 {
		t.Fatalf("path exit %d, out=%q, err=%q", code, out.String(), errs.String())
	}
}

func TestLoadConfigLenientRejectsNullDocument(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	if _, _, err := LoadConfig(false); err == nil {
		t.Fatal("store-backed admin command must not use defaults for a null config document")
	}
}
