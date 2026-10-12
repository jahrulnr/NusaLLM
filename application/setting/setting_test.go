package setting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	// Unknown keys are ignored, not fatal — and any schema write prunes
	// them, since the file is rewritten from the validated document.
	if err := app.Validate(); err != nil {
		t.Fatalf("unknown key must be ignored: %v", err)
	}
	if _, err := app.Set("log.max_lines", "1"); err != nil {
		t.Fatalf("set must work while unknown keys exist: %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || strings.Contains(string(raw), "bogus") {
		t.Fatalf("set must prune unknown keys: %v %q", err, raw)
	}
	if err := os.WriteFile(path, []byte(`{"bogus":true,"log":{"max_files":5}}`), 0o644); err != nil {
		t.Fatal(err)
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

// leafCase is one dotted config leaf: the raw value Set takes and the
// JSON value the stored document then holds. Every value differs from
// its default, so a write that is lost instead of serialized shows up
// as a value that reverted.
type leafCase struct {
	key   string
	value string
	want  any
}

func concurrentLeaves() []leafCase {
	return []leafCase{
		{"server.port", "11998", float64(11998)},
		{"conversation.mailbox_cap", "321", float64(321)},
		{"conversation.max_turn_steps", "654", float64(654)},
		{"conversation.compact_budget_pct", "42", float64(42)},
		{"conversation.compact_keep_tail", "6", float64(6)},
		{"service.header_timeout", `"45s"`, "45s"},
		{"service.idle_timeout", `"20s"`, "20s"},
		{"log.max_lines", "1234", float64(1234)},
		{"log.max_files", "7", float64(7)},
	}
}

// leafOf resolves one dotted key in a config document; a missing key
// yields nil, which no expected value equals.
func leafOf(t *testing.T, raw []byte, key string) any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode document for %s: %v", key, err)
	}
	var node any = doc
	for _, seg := range strings.Split(key, ".") {
		section, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		if node, ok = section[seg]; !ok {
			return nil
		}
	}
	return node
}

// releaseAll runs n writers, holds every one at a closed-channel gate,
// and returns once all have finished — the writers enter the
// read-modify-write together, with no sleep standing in for a barrier.
func releaseAll(n int, run func(i int)) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			run(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

// Concurrent leaf writes all land. Set is a whole-document
// read-modify-write, so without serialization every writer that read
// before the last write rewrites the file from its own stale read and
// its key reverts to the default even though Set reported success. One
// shared *SettingApp over a real file; the assertion is on the stored
// document, not on timing.
func TestConcurrentSetDoesNotLoseUpdates(t *testing.T) {
	leaves := concurrentLeaves()
	app, path := newApp(t)

	docs := make([][]byte, len(leaves))
	errs := make([]error, len(leaves))
	releaseAll(len(leaves), func(i int) {
		docs[i], errs[i] = app.Set(leaves[i].key, leaves[i].value)
	})

	// The document each Set returned is the writer's own view of the
	// file, so it must carry the writer's leaf...
	for i, leaf := range leaves {
		if errs[i] != nil {
			t.Fatalf("Set(%s) = %v", leaf.key, errs[i])
		}
		if got := leafOf(t, docs[i], leaf.key); !reflect.DeepEqual(got, leaf.want) {
			t.Errorf("Set(%s) returned a document with %s = %v, want %v",
				leaf.key, leaf.key, got, leaf.want)
		}
	}

	// ...and so must what the file holds afterwards, for every leaf.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaf := range leaves {
		if got := leafOf(t, raw, leaf.key); !reflect.DeepEqual(got, leaf.want) {
			t.Errorf("%s = %v on disk, want %v — a concurrent write was lost",
				leaf.key, got, leaf.want)
		}
	}
}

// Concurrent removals all land for the same reason — Unset reads the
// whole document, deletes one leaf, and rewrites the file, so an
// unsynchronized removal that ran on a stale read restores whatever the
// writer before it deleted. A removal is not an absent line: the
// validated document is rewritten whole, so the leaf returns to its
// default value.
func TestConcurrentUnsetDoesNotLoseRemovals(t *testing.T) {
	leaves := concurrentLeaves()
	app, path := newApp(t)
	for _, leaf := range leaves {
		if _, err := app.Set(leaf.key, leaf.value); err != nil {
			t.Fatalf("seed %s: %v", leaf.key, err)
		}
	}
	defaults, err := json.Marshal(config.NewConfig())
	if err != nil {
		t.Fatal(err)
	}

	docs := make([][]byte, len(leaves))
	errs := make([]error, len(leaves))
	releaseAll(len(leaves), func(i int) {
		docs[i], errs[i] = app.Unset(leaves[i].key)
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, leaf := range leaves {
		if errs[i] != nil {
			t.Fatalf("Unset(%s) = %v", leaf.key, errs[i])
		}
		want := leafOf(t, defaults, leaf.key)
		if got := leafOf(t, docs[i], leaf.key); !reflect.DeepEqual(got, want) {
			t.Errorf("Unset(%s) returned a document with %s = %v, want the default %v",
				leaf.key, leaf.key, got, want)
		}
		if got := leafOf(t, raw, leaf.key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v on disk after its concurrent removal, want the default %v — a concurrent removal was lost",
				leaf.key, got, want)
		}
	}
}
