// Package setup provisions bootstrap state the process needs before the
// use cases run — today, a config file when none exists yet.
package setup

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"

	"github.com/jahrulnr/nusallm/infrastructure/config"
)

// EnsureConfig guarantees a config file at path. An empty path means the
// process runs on tag defaults and nothing is written; an existing file —
// directory included — is left untouched. A missing file is generated
// from the default tags so the operator gets a self-documenting starting
// point instead of a bare load failure. Returns whether it created one.
func EnsureConfig(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	raw, err := json.MarshalIndent(config.NewConfig(), "", "  ")
	if err != nil {
		return false, err
	}
	if err := config.WriteFileAtomic(path, append(raw, '\n')); err != nil {
		return false, err
	}
	return true, nil
}
