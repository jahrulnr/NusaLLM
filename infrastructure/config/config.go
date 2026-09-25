package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

type Config struct {
	Conversation ConversationConfig `json:"conversation"`
	Service      ServiceConfig      `json:"service"`
	Storage      StorageConfig      `json:"storage"`
}

// Normalize runs each section's normalization — invalid values clamp to
// unconfigured, zeros fill from `default` tags.
func (c *Config) Normalize() {
	c.Conversation.Normalize()
	c.Service.Normalize()
	c.Storage.Normalize()
}

// NewConfig returns the defaults — every tunable reads its `default`
// struct tag, so the tag is the single source of truth and nothing
// downstream re-declares a literal.
func NewConfig() Config {
	var c Config
	c.Normalize()
	return c
}

// Load reads the JSON config file. A non-empty path replaces c entirely:
// file values win, every field left unset — absent or an explicit zero —
// falls back to its `default` tag, and unknown keys or trailing garbage
// are rejected so a typo fails loudly instead of silently tuning
// nothing. An empty path skips the file and only normalizes c in place —
// values already set survive — which keeps the
// Load(os.Getenv("CONFIG_PATH")) pattern safe.
func (c *Config) Load(path string) error {
	if path == "" {
		c.Normalize()
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return apperror.NewInternalError("config load failed", err)
	}
	var fresh Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fresh); err != nil {
		return apperror.NewInternalError("config load failed", err)
	}
	if dec.More() {
		return apperror.NewInternalError("config load failed",
			fmt.Errorf("trailing data after the JSON document"))
	}
	fresh.Normalize()
	*c = fresh
	return nil
}

// ApplyDefaults fills zero-valued fields from their `default` struct tag,
// recursing into nested sections. Only zero fields are filled — a set
// value is a choice, not a gap. A malformed tag is a programmer error and
// panics at the boundary, where it is caught immediately.
func ApplyDefaults(v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		panic("config: ApplyDefaults wants a pointer to a struct")
	}
	elem, rt := rv.Elem(), rv.Elem().Type()
	for i := range elem.NumField() {
		f, sf := elem.Field(i), rt.Field(i)
		if f.Kind() == reflect.Struct {
			ApplyDefaults(f.Addr().Interface())
			continue
		}
		if !f.CanSet() || !f.IsZero() {
			continue
		}
		tag, ok := sf.Tag.Lookup("default")
		if !ok {
			continue
		}
		switch {
		case f.Type() == reflect.TypeOf(Duration(0)):
			d, err := time.ParseDuration(tag)
			if err != nil {
				panic("config: bad default for " + sf.Name + ": " + tag)
			}
			f.SetInt(int64(d))
		case f.Kind() >= reflect.Int && f.Kind() <= reflect.Int64:
			n, err := strconv.ParseInt(tag, 10, 64)
			if err != nil {
				panic("config: bad default for " + sf.Name + ": " + tag)
			}
			f.SetInt(n)
		case f.Kind() == reflect.String:
			f.SetString(tag)
		case f.Kind() == reflect.Bool:
			b, err := strconv.ParseBool(tag)
			if err != nil {
				panic("config: bad default for " + sf.Name + ": " + tag)
			}
			f.SetBool(b)
		}
	}
}
