// Package setup provisions bootstrap state the process needs before the
// use cases run — today, a config file when none exists yet.
package setup

import (
	"encoding/json"

	"github.com/jahrulnr/nusallm/infrastructure/config"
)

// EnsureConfig guarantees a config file at path. An empty path means the
// process runs on tag defaults and nothing is written; an existing file —
// directory included — is left untouched. A missing file is generated
// from the default tags so the operator gets a self-documenting starting
// point instead of a bare load failure. Returns whether it created one.
func EnsureConfig(store config.Store, path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if exists, err := store.Exists(path); err != nil {
		return false, err
	} else if exists {
		return false, nil
	}
	raw, err := json.MarshalIndent(config.NewConfig(), "", "  ")
	if err != nil {
		return false, err
	}
	if err := store.WriteAtomic(path, append(raw, '\n')); err != nil {
		return false, err
	}
	return true, nil
}
