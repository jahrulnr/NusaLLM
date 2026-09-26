package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

type Config struct {
	App          App                `json:"-"`
	Conversation ConversationConfig `json:"conversation"`
	Service      ServiceConfig      `json:"service"`
	Storage      StorageConfig      `json:"storage"`
	Log          LogConfig          `json:"log"`
}

// Normalize runs each section's normalization — invalid values clamp to
// unconfigured, zeros fill from `default` tags.
func (c *Config) Normalize() {
	c.Conversation.Normalize()
	c.Service.Normalize()
	c.Storage.Normalize()
	c.Log.Normalize()
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
	fresh, err := Validate(raw)
	if err != nil {
		return err
	}
	*c = fresh
	return nil
}

// Validate decodes one config document strictly — unknown keys or
// trailing garbage are rejected so a typo fails loudly instead of
// silently tuning nothing — then normalizes. Callers mutating the file
// validate the candidate before it replaces the old one.
func Validate(raw []byte) (Config, error) {
	var fresh Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fresh); err != nil {
		return Config{}, apperror.NewInternalError("config load failed", err)
	}
	// More() only reports inside an open array/object — a trailing `}` or
	// `]` slips through it. A second decode must hit io.EOF or the file
	// carries garbage past the document.
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, apperror.NewInternalError("config load failed",
			fmt.Errorf("trailing data after the JSON document"))
	}
	fresh.Normalize()
	return fresh, nil
}

// WriteFileAtomic persists a config document via tmp + fsync + rename —
// the repo write convention: a crash mid-write leaves either the old
// file or nothing, never a torn config. The tmp name is unique per call
// (CreateTemp) so two processes writing concurrently cannot truncate
// each other's staged bytes. A symlinked path is resolved first — the
// rename replaces the real file, not the link. An existing file's mode
// is preserved; new files default to 0o644.
func WriteFileAtomic(path string, raw []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
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
