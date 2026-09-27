// Package setting owns the operator-facing view and mutation of the JSON
// config file — goclaw's "config show/set/validate" surface as a use case.
// The file holds tunables only; credentials never live here (they are
// SecretStore material referenced by gateways). Changes apply to the file,
// so a running process picks them up on next start.
package setting

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// SettingApp fronts the config file path. There is no repository — the
// file is the store and config.Load is its reader.
type SettingApp struct {
	path string
}

// New binds the app to the resolved config path. An empty path means the
// process runs on tag defaults: Show still works, Set reports it has
// nowhere to write.
func New(path string) *SettingApp {
	return &SettingApp{path: path}
}

// Path reports the file the app edits — "" when running on defaults.
func (a *SettingApp) Path() string { return a.path }

// Show returns the effective config — file values merged over the
// `default` tags — as indented JSON. A missing file shows defaults.
func (a *SettingApp) Show() ([]byte, error) {
	cfg := config.NewConfig()
	if err := cfg.Load(a.path); err != nil {
		if a.path != "" && errors.Is(err, os.ErrNotExist) {
			return json.MarshalIndent(config.NewConfig(), "", "  ")
		}
		return nil, err
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// Validate reports whether the file decodes as a strict config document.
func (a *SettingApp) Validate() error {
	if a.path == "" {
		return errors.New("no config file configured (CONFIG_PATH unset)")
	}
	raw, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return apperror.New(400, "no config file",
			fmt.Errorf("%s does not exist — run `config set` to create it", a.path))
	}
	if err != nil {
		return apperror.NewInternalError("config load failed", err)
	}
	_, err = config.Validate(raw)
	return err
}

// Set patches one dotted key — "service.idle_timeout" — and rewrites the
// file atomically. The path must resolve to a leaf in the config schema:
// typos fail instead of adding dead keys, and missing leaves are created
// so a sparse file can be filled in. The value decodes as a JSON literal
// (5, true, "2m") with bare text taken as a string; `null` is refused —
// `config unset` is the removal path. The written file carries the full
// document in schema order so what is stored is what Load computes.
func (a *SettingApp) Set(key, rawValue string) ([]byte, error) {
	schemaLeaf, err := schemaLeaf(key)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal([]byte(rawValue), &value); err != nil {
		value = rawValue // bare text is a string value
	}
	if value == nil {
		return nil, apperror.New(400, "refused null value",
			fmt.Errorf("config key %q: use `config unset` to remove a setting", key))
	}
	if jsonType(value) != jsonType(schemaLeaf) {
		return nil, apperror.New(400, "invalid value",
			fmt.Errorf("config key %q expects a %s value like %s",
				key, jsonType(schemaLeaf), lit(schemaLeaf)))
	}

	doc, err := a.doc(true)
	if err != nil {
		return nil, err
	}
	node := doc
	segments := strings.Split(key, ".")
	for _, seg := range segments[:len(segments)-1] {
		next, ok := node[seg].(map[string]any)
		if !ok {
			if _, present := node[seg]; present {
				return nil, apperror.New(400, "invalid config file",
					fmt.Errorf("config file has a non-section value at %q — fix or remove it", seg))
			}
			next = map[string]any{}
			node[seg] = next
		}
		node = next
	}
	node[segments[len(segments)-1]] = value
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Validate(out)
	if err != nil {
		return nil, apperror.New(400, "invalid value",
			fmt.Errorf("config key %q: %v", key, causeOf(err)))
	}
	return a.write(cfg)
}

// Unset removes one dotted key — including unknown keys, which is the
// repair path when the file no longer validates. The result is
// validated before writing; the file keeps schema order.
func (a *SettingApp) Unset(key string) ([]byte, error) {
	doc, err := a.doc(false)
	if err != nil {
		return nil, err
	}
	segments := strings.Split(key, ".")
	node := doc
	for _, seg := range segments[:len(segments)-1] {
		next, ok := node[seg].(map[string]any)
		if !ok {
			return nil, apperror.New(400, "unknown setting",
				fmt.Errorf("config key %q: %q is not a section", key, seg))
		}
		node = next
	}
	leaf := segments[len(segments)-1]
	if _, ok := node[leaf]; !ok {
		return nil, apperror.New(400, "unknown setting",
			fmt.Errorf("config key %q does not exist", key))
	}
	delete(node, leaf)
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Validate(out)
	if err != nil {
		return nil, apperror.New(400, "config still invalid",
			fmt.Errorf("after removing %q: %v", key, causeOf(err)))
	}
	return a.write(cfg)
}

// doc returns the file's raw object. With create=true a missing file
// starts from an empty document; either way the result must be a JSON
// object — `null`, `[]`, or a scalar file is rejected.
func (a *SettingApp) doc(create bool) (map[string]any, error) {
	if a.path == "" {
		return nil, errors.New("no config file configured (CONFIG_PATH unset)")
	}
	raw, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		if create {
			return map[string]any{}, nil
		}
		return nil, apperror.New(400, "no config file",
			fmt.Errorf("%s does not exist", a.path))
	}
	if err != nil {
		return nil, apperror.NewInternalError("config load failed", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, apperror.New(400, "invalid config file",
			fmt.Errorf("%s does not decode: %w — fix it or start over", a.path, err))
	}
	if doc == nil {
		return nil, apperror.New(400, "invalid config file",
			errors.New("config file is not a JSON object — fix or delete it"))
	}
	return doc, nil
}

// write serializes the validated config in schema order and persists it
// atomically, returning the pretty document for the caller to echo.
func (a *SettingApp) write(cfg config.Config) ([]byte, error) {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if err := config.WriteFileAtomic(a.path, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// schemaLeaf resolves a dotted key against the schema — the defaults
// document — returning the leaf's schema value. A path that ends at or
// crosses a section is rejected: only leaves are settable.
func schemaLeaf(key string) (any, error) {
	raw, err := json.Marshal(config.NewConfig())
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	segments := strings.Split(key, ".")
	node := doc
	for _, seg := range segments {
		next, ok := node[seg]
		if !ok {
			return nil, apperror.New(400, "unknown setting",
				fmt.Errorf("config key %q does not exist", key))
		}
		if seg == segments[len(segments)-1] {
			if _, isSection := next.(map[string]any); isSection {
				return nil, apperror.New(400, "not a leaf",
					fmt.Errorf("config key %q is a section — set a leaf like %s.<field>", key, key))
			}
			return next, nil
		}
		child, ok := next.(map[string]any)
		if !ok {
			return nil, apperror.New(400, "unknown setting",
				fmt.Errorf("config key %q: %q is a leaf, not a section", key, seg))
		}
		node = child
	}
	return nil, apperror.New(400, "unknown setting",
		fmt.Errorf("config key %q does not exist", key))
}

// jsonType classifies a decoded JSON value for type-checking submitted
// values against the schema leaf.
func jsonType(v any) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	default:
		return "object"
	}
}

// lit renders a schema leaf as a JSON literal for error messages.
func lit(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

// causeOf unwraps an AppError to its internal cause for re-wrapping
// under a corrected status code.
func causeOf(err error) error {
	if c := errors.Unwrap(err); c != nil {
		return c
	}
	return err
}
