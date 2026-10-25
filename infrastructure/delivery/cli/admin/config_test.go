package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CONFIG_PATH", "")
	var out, errs bytes.Buffer
	if code := Config([]string{"path"}, &out, &errs); code != 0 || strings.TrimSpace(out.String()) != "(no config file — running on defaults)" || errs.Len() != 0 {
		t.Fatalf("path exit %d, out=%q, err=%q", code, out.String(), errs.String())
	}
}

func TestResolveConfigPathPrefersEnvOverInstalledFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	installed := filepath.Join(home, ".config", "nusallm", "config.json")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("environment", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("CONFIG_PATH", "/exported")
		if got := resolveConfigPath(); got != "/exported" {
			t.Fatalf("path = %q, want /exported", got)
		}
	})

	t.Run("dotenv", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("CONFIG_PATH", "")
		if err := os.WriteFile(".env", []byte("CONFIG_PATH=/from-dotenv\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolveConfigPath(); got != "/from-dotenv" {
			t.Fatalf("path = %q, want /from-dotenv", got)
		}
	})
}

func TestResolveConfigPathUsesInstalledFileWhenUnset(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CONFIG_PATH", "")
	installed := filepath.Join(home, ".config", "nusallm", "config.json")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveConfigPath(); got != installed {
		t.Fatalf("path = %q, want %q", got, installed)
	}
}

func TestResolveConfigPathInstalledIgnoresDotEnv(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CONFIG_PATH", "")
	if err := os.WriteFile(".env", []byte("CONFIG_PATH=/from-dotenv\nNUSALLM_TEST_DOTENV_KEEP=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev, had := os.LookupEnv("NUSALLM_TEST_DOTENV_KEEP")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("NUSALLM_TEST_DOTENV_KEEP", prev)
			return
		}
		_ = os.Unsetenv("NUSALLM_TEST_DOTENV_KEEP")
	})
	prevInstall := runningFromInstall
	runningFromInstall = func() bool { return true }
	t.Cleanup(func() { runningFromInstall = prevInstall })

	want, err := userConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("config stat err = %v, want missing", err)
	}
	if got := resolveConfigPath(); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if os.Getenv("NUSALLM_TEST_DOTENV_KEEP") != "1" {
		t.Fatal(".env keys other than CONFIG_PATH were not loaded")
	}
}

func TestResolveConfigPathInstalledKeepsExportedEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CONFIG_PATH", "/exported")
	if err := os.WriteFile(".env", []byte("CONFIG_PATH=/from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := runningFromInstall
	runningFromInstall = func() bool { return true }
	t.Cleanup(func() { runningFromInstall = prev })
	if got := resolveConfigPath(); got != "/exported" {
		t.Fatalf("path = %q, want /exported", got)
	}
}

func TestEnsureConfigInstalledFileUsesAbsoluteDataDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	path, err := userConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := userDataDir()
	if err != nil {
		t.Fatal(err)
	}
	var errs bytes.Buffer
	if err := EnsureConfig(path, &errs); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Validate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Dir != data {
		t.Fatalf("storage.dir = %q, want %q", cfg.Storage.Dir, data)
	}
	if !strings.Contains(errs.String(), path) {
		t.Fatalf("stderr = %q, want the config path", errs.String())
	}
}

func TestResolveConfigPathStaysEmptyWithoutInstalledFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CONFIG_PATH", "")
	if got := resolveConfigPath(); got != "" {
		t.Fatalf("path = %q, want empty", got)
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

// loadEnvFile imports every unset key in .env into the process
// environment — the documented contract (provider `env:` secret refs
// resolve through os.Getenv, and the file exists to carry them). Keys
// that steer child processes, path resolution, or the provider wire are
// refused: an LD_PRELOAD from a cloned repo's .env would load into
// every tool subprocess, XDG_* would silently redirect config and data
// lookup, and a proxy or CA override would MITM the requests that carry
// gateway keys.
func TestDotEnvLoadsSecretRefsAndRejectsSteeringKeys(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte(
		"CONFIG_PATH=/tmp/from-dotenv/config.json\n"+
			"NUSALLM_PROVIDER_KEY=sk-live\n"+
			"LD_PRELOAD=/tmp/evil.so\n"+
			"BASH_ENV=/tmp/evil.sh\n"+
			"NODE_OPTIONS=--require /tmp/evil.js\n"+
			"XDG_CONFIG_HOME=/tmp/dotenv-xdg\n"+
			"HTTPS_PROXY=http://attacker:8080\n"+
			"SSL_CERT_FILE=/tmp/attacker-ca.pem\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"CONFIG_PATH", "NUSALLM_PROVIDER_KEY", "LD_PRELOAD", "BASH_ENV", "NODE_OPTIONS", "XDG_CONFIG_HOME", "HTTPS_PROXY", "SSL_CERT_FILE"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	loadEnvFile(env)
	if got := os.Getenv("CONFIG_PATH"); got != "/tmp/from-dotenv/config.json" {
		t.Fatalf("CONFIG_PATH = %q — the file's documented bootstrap key must still load", got)
	}
	if got := os.Getenv("NUSALLM_PROVIDER_KEY"); got != "sk-live" {
		t.Fatalf("NUSALLM_PROVIDER_KEY = %q — env: secret refs must still resolve", got)
	}
	for _, k := range []string{"LD_PRELOAD", "BASH_ENV", "NODE_OPTIONS", "XDG_CONFIG_HOME", "HTTPS_PROXY", "SSL_CERT_FILE"} {
		if got := os.Getenv(k); got != "" {
			t.Fatalf("%s = %q — process-steering keys must never load from .env", k, got)
		}
	}
}

// A variable already present in the real environment is never
// overwritten — the one safeguard the loader has.
func TestDotEnvNeverOverridesExportedEnv(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("NUSALLM_DOTENV_VAR=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NUSALLM_DOTENV_VAR", "from-shell")
	loadEnvFile(env)
	if got := os.Getenv("NUSALLM_DOTENV_VAR"); got != "from-shell" {
		t.Fatalf("exported env overwritten: %q", got)
	}
}
