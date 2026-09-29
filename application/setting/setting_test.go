package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
)

func newApp(t *testing.T) (*SettingApp, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	raw, err := json.Marshal(config.NewConfig())
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return New(repoconfig.NewLocal(), path), path
}

func TestUnsetExplainsNonSectionValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"storage":123}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(repoconfig.NewLocal(), path).Unset("storage.dir")
	if err == nil || !strings.Contains(apperror.As(err).Caused(), `non-section value at "storage"`) || !strings.Contains(apperror.As(err).Caused(), `unset storage`) {
		t.Fatalf("unset diagnostic = %v", err)
	}
	if _, err := New(repoconfig.NewLocal(), path).Unset("storage"); err != nil {
		t.Fatalf("suggested repair failed: %v", err)
	}
}

func TestSetReportsPreexistingInvalidField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"service":{"idle_timeout":"bogus"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(repoconfig.NewLocal(), path).Set("log.max_files", "5")
	if err == nil || !strings.Contains(apperror.As(err).Caused(), `after setting "log.max_files"`) || !strings.Contains(apperror.As(err).Caused(), `invalid duration`) {
		t.Fatalf("set diagnostic = %v", err)
	}
}

func TestSetDoesNotReplaceUndecodableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := []byte(`{"service":`)
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(repoconfig.NewLocal(), path).Set("log.max_files", "5"); err == nil {
		t.Fatal("set must reject undecodable existing file")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("invalid file was changed: %q, %v", after, err)
	}
}

func TestMutationsDoNotReplaceNullDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	before := []byte("null\n")
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}
	app := New(repoconfig.NewLocal(), path)
	for _, mutate := range []struct {
		name string
		run  func() error
	}{
		{"set", func() error { _, err := app.Set("log.max_files", "5"); return err }},
		{"unset", func() error { _, err := app.Unset("log.max_files"); return err }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			if err := mutate.run(); err == nil || !strings.Contains(apperror.As(err).Caused(), "not a JSON object") {
				t.Fatalf("mutation of null document = %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("null document was changed: %q, %v", after, err)
			}
		})
	}
}

func TestValidateMissingFileExplainsCreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	err := New(repoconfig.NewLocal(), path).Validate()
	if err == nil || !strings.Contains(apperror.As(err).Caused(), "config set") || !strings.Contains(apperror.As(err).Caused(), path) {
		t.Fatalf("missing file guidance = %v", err)
	}
}

func TestShowMergesDefaultsOverFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"service":{"idle_timeout":"5s"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := New(repoconfig.NewLocal(), path).Show()
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("show output: %v", err)
	}
	if got := doc["service"].(map[string]any)["idle_timeout"]; got != "5s" {
		t.Fatalf("idle_timeout = %v, want 5s", got)
	}
	if _, ok := doc["conversation"].(map[string]any)["mailbox_cap"]; !ok {
		t.Fatal("defaults missing from show output")
	}
}

func TestSetPatchesLeafAndPersists(t *testing.T) {
	app, path := newApp(t)
	if _, err := app.Set("service.idle_timeout", "2m"); err != nil {
		t.Fatalf("set: %v", err)
	}
	var cfg config.Config
	if err := cfg.Load(repoconfig.NewLocal(), path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if time.Duration(cfg.Service.IdleTimeout) != 2*time.Minute {
		t.Fatalf("idle_timeout = %v, want 2m", cfg.Service.IdleTimeout)
	}
}

func TestSetScalarsAndRejectsUnknownKeys(t *testing.T) {
	app, path := newApp(t)
	if _, err := app.Set("log.max_files", "5"); err != nil {
		t.Fatalf("set integer: %v", err)
	}
	var doc map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["log"].(map[string]any)["max_files"] != float64(5) {
		t.Fatal("max_files was not persisted as a JSON number")
	}
	for _, key := range []string{"nope", "service.nope", "service.idle_timeout.x"} {
		if _, err := app.Set(key, "1"); err == nil {
			t.Fatalf("set %q should fail", key)
		}
	}
	if _, err := app.Set("service.idle_timeout", "not-a-duration"); err == nil {
		t.Fatal("set should reject a value the schema cannot decode")
	}
}

func TestSetWithoutFileFails(t *testing.T) {
	if _, err := New(repoconfig.NewLocal(), "").Set("log.max_files", "5"); err == nil {
		t.Fatal("set without a config path should fail")
	}
	if err := New(repoconfig.NewLocal(), "").Validate(); err == nil {
		t.Fatal("validate without a config path should fail")
	}
}

func TestSetFillsMissingLeaves(t *testing.T) {
	// A sparse file — partial configs are legal to Load — must accept
	// schema leaves the file never mentioned.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"storage":{"dir":"/tmp/x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := New(repoconfig.NewLocal(), path)
	if _, err := app.Set("log.max_files", "5"); err != nil {
		t.Fatalf("set absent leaf: %v", err)
	}
	var cfg config.Config
	if err := cfg.Load(repoconfig.NewLocal(), path); err != nil {
		t.Fatal(err)
	}
	if cfg.Log.MaxFiles != 5 {
		t.Fatal("max_files not persisted")
	}
}

func TestSetRejectsSectionsNullAndTypeMismatch(t *testing.T) {
	app, path := newApp(t)
	before, _ := os.ReadFile(path)
	for _, kv := range [][2]string{
		{"service", "{}"},             // wiping a section is not a leaf set
		{"service", "null"},           // use unset
		{"service.idle_timeout", "5"}, // duration leaf wants a string
		{"log.max_files", `"yes"`},    // integer leaf wants a number
	} {
		if _, err := app.Set(kv[0], kv[1]); err == nil {
			t.Fatalf("set %s %s should fail", kv[0], kv[1])
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected sets must leave the file untouched")
	}
}

func TestSetCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if _, err := New(repoconfig.NewLocal(), path).Set("log.max_lines", "42"); err != nil {
		t.Fatalf("set on missing file: %v", err)
	}
	var cfg config.Config
	if err := cfg.Load(repoconfig.NewLocal(), path); err != nil {
		t.Fatal(err)
	}
	if cfg.Log.MaxLines != 42 {
		t.Fatalf("max_lines = %d", cfg.Log.MaxLines)
	}
}

func TestUnsetRemovesUnknownKeyAndLeaf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"bogus":true,"log":{"max_files":5}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := New(repoconfig.NewLocal(), path)
	if err := app.Validate(); err == nil {
		t.Fatal("fixture file must be invalid")
	}
	if _, err := app.Set("log.max_lines", "1"); err == nil {
		t.Fatal("set must fail while the file is invalid")
	}
	if _, err := app.Unset("bogus"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if err := app.Validate(); err != nil {
		t.Fatalf("validate after unset: %v", err)
	}
	if _, err := app.Unset("log.max_files"); err != nil {
		t.Fatalf("unset leaf: %v", err)
	}
	var cfg config.Config
	if err := cfg.Load(repoconfig.NewLocal(), path); err != nil {
		t.Fatal(err)
	}
	if cfg.Log.MaxFiles != 9 {
		t.Fatal("max_files should be back at default after unset")
	}
	if _, err := app.Unset("nope"); err == nil {
		t.Fatal("unset of a missing key should fail")
	}
}
