package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testStore struct{}

func (testStore) Read(path string) ([]byte, error)          { return os.ReadFile(path) }
func (testStore) WriteAtomic(path string, raw []byte) error { return os.WriteFile(path, raw, 0o644) }
func (testStore) Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	return err == nil, err
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFileValuesWin(t *testing.T) {
	var c Config
	if err := c.Load(testStore{}, writeConfig(t, `{"conversation":{"mailbox_cap":8,"max_turn_steps":3},"service":{"idle_timeout":"10s"}}`)); err != nil {
		t.Fatal(err)
	}
	if c.Conversation.MailboxCap != 8 || c.Conversation.MaxTurnStep != 3 {
		t.Fatalf("file values must win: %+v", c.Conversation)
	}
	if c.Service.IdleTimeout != Duration(10*time.Second) {
		t.Fatalf("duration must parse: %v", c.Service.IdleTimeout)
	}
	// Absent fields keep their tag default.
	if c.Service.HeaderTimeout != Duration(30*time.Second) {
		t.Fatalf("absent field must take tag default: %v", c.Service.HeaderTimeout)
	}
}

func TestLoadZeroMeansDefault(t *testing.T) {
	var c Config
	if err := c.Load(testStore{}, writeConfig(t, `{"conversation":{"mailbox_cap":0}}`)); err != nil {
		t.Fatal(err)
	}
	if c.Conversation.MailboxCap != 128 {
		t.Fatalf("explicit zero must fall back to tag default, got %d", c.Conversation.MailboxCap)
	}
}

func TestLoadIgnoresUnknownKeys(t *testing.T) {
	var c Config
	// Leftover keys from removed fields and comment-as-field additions
	// must not block boot — only the known keys are read.
	if err := c.Load(testStore{}, writeConfig(t, `{"conversation":{"mailbox_cap":8},"bogus":true,"_note":"x"}`)); err != nil {
		t.Fatalf("unknown keys must be ignored: %v", err)
	}
	if c.Conversation.MailboxCap != 8 {
		t.Fatalf("known key must still apply, got %d", c.Conversation.MailboxCap)
	}
	if err := c.Load(testStore{}, writeConfig(t, `{"conversation":{"mailbox_cap":8,"typo":1}}`)); err != nil {
		t.Fatalf("unknown nested key must be ignored: %v", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	var c Config
	if err := c.Load(testStore{}, writeConfig(t, `{nope`)); err == nil {
		t.Fatal("malformed JSON must fail")
	}
	if err := c.Load(testStore{}, writeConfig(t, `{"conversation":{"mailbox_cap":8}} {"extra":true}`)); err == nil {
		t.Fatal("trailing garbage must fail")
	}
	// More() only reports inside an open array/object — a stray closer
	// reads as "nothing more" to it, so the EOF check must catch it.
	for _, raw := range []string{`{"conversation":{}}}`, `{"conversation":{}}]`, `{"conversation":{}} extra`, `{"conversation":{}}42`} {
		if err := c.Load(testStore{}, writeConfig(t, raw)); err == nil {
			t.Fatalf("trailing %q must fail", raw)
		}
	}
}

func TestValidateRejectsNonObjectDocument(t *testing.T) {
	for _, raw := range []string{"null", "[]", `"text"`, "42"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Validate([]byte(raw)); err == nil {
				t.Fatalf("%s must not be a valid config document", raw)
			}
		})
	}
}

func TestNormalizeClampsNegatives(t *testing.T) {
	c := Config{Conversation: ConversationConfig{MailboxCap: -1, MaxTurnStep: -5}}
	c.Normalize()
	if c.Conversation.MailboxCap != 128 || c.Conversation.MaxTurnStep != 512 {
		t.Fatalf("negatives must clamp to tag defaults, got %+v", c.Conversation)
	}
	s := ServiceConfig{IdleTimeout: -1}
	s.Normalize()
	if s.IdleTimeout != Duration(90_000_000_000) {
		t.Fatalf("negative duration must clamp to tag default, got %v", s.IdleTimeout)
	}
}

func TestDurationMarshalEmitsHumanString(t *testing.T) {
	raw, err := json.Marshal(ServiceConfig{HeaderTimeout: Duration(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"header_timeout":"30s","idle_timeout":"0s"}` {
		t.Fatalf("marshal must emit the human string, got %s", raw)
	}
}

func TestLoadMissingFile(t *testing.T) {
	var c Config
	if err := c.Load(testStore{}, filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestLoadRequiresStoreForFile(t *testing.T) {
	var c Config
	if err := c.Load(nil, "config.json"); err == nil {
		t.Fatal("non-empty config path requires a store")
	}
	if err := c.Load(nil, ""); err != nil {
		t.Fatalf("default config must not require a store: %v", err)
	}
}

func TestAppDefaultsOnNormalize(t *testing.T) {
	var c Config
	c.Normalize()
	if c.App.Name != "NusaLLM" || c.App.Source != "https://github.com/jahrulnr/NusaLLM" {
		t.Fatalf("App defaults not applied: %+v", c.App)
	}
}

func TestLoadEmptyPathYieldsDefaults(t *testing.T) {
	c := NewConfig()
	c.Conversation.MailboxCap = 1 // simulate a caller mutation
	if err := c.Load(testStore{}, ""); err != nil {
		t.Fatal(err)
	}
	if c.Conversation.MailboxCap != 1 {
		t.Fatal("empty path must not clobber set values")
	}
	c.Conversation.MailboxCap = 0
	if err := c.Load(testStore{}, ""); err != nil {
		t.Fatal(err)
	}
	if c.Conversation.MailboxCap != 128 {
		t.Fatal("empty path must still fill zero fields from tags")
	}
}
