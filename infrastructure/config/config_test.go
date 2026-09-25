package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	if err := c.Load(writeConfig(t, `{"conversation":{"mailbox_cap":8,"max_turn_steps":3},"service":{"idle_timeout":"10s"}}`)); err != nil {
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
	if err := c.Load(writeConfig(t, `{"conversation":{"mailbox_cap":0}}`)); err != nil {
		t.Fatal(err)
	}
	if c.Conversation.MailboxCap != 128 {
		t.Fatalf("explicit zero must fall back to tag default, got %d", c.Conversation.MailboxCap)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	var c Config
	if err := c.Load(writeConfig(t, `{"conversation":{"mailbox_cap":8},"bogus":true}`)); err == nil {
		t.Fatal("unknown key must fail loudly")
	}
	if err := c.Load(writeConfig(t, `{"conversation":{"mailbox_cap":8,"typo":1}}`)); err == nil {
		t.Fatal("unknown nested key must fail loudly")
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	var c Config
	if err := c.Load(writeConfig(t, `{nope`)); err == nil {
		t.Fatal("malformed JSON must fail")
	}
	if err := c.Load(writeConfig(t, `{"conversation":{"mailbox_cap":8}} {"extra":true}`)); err == nil {
		t.Fatal("trailing garbage must fail")
	}
}

func TestNormalizeClampsNegatives(t *testing.T) {
	c := Config{Conversation: ConversationConfig{MailboxCap: -1, MaxTurnStep: -5}}
	c.Normalize()
	if c.Conversation.MailboxCap != 128 || c.Conversation.MaxTurnStep != 16 {
		t.Fatalf("negatives must clamp to tag defaults, got %+v", c.Conversation)
	}
	s := ServiceConfig{IdleTimeout: -1}
	s.Normalize()
	if s.IdleTimeout != Duration(90_000_000_000) {
		t.Fatalf("negative duration must clamp to tag default, got %v", s.IdleTimeout)
	}
}

func TestLoadMissingFile(t *testing.T) {
	var c Config
	if err := c.Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestLoadEmptyPathYieldsDefaults(t *testing.T) {
	c := NewConfig()
	c.Conversation.MailboxCap = 1 // simulate a caller mutation
	if err := c.Load(""); err != nil {
		t.Fatal(err)
	}
	if c.Conversation.MailboxCap != 1 {
		t.Fatal("empty path must not clobber set values")
	}
	c.Conversation.MailboxCap = 0
	if err := c.Load(""); err != nil {
		t.Fatal(err)
	}
	if c.Conversation.MailboxCap != 128 {
		t.Fatal("empty path must still fill zero fields from tags")
	}
}
