package config

import (
	"os"
	"strings"
)

// StorageConfig locates the on-disk state — every repository roots under
// one data dir so a deployment moves it in a single place.
type StorageConfig struct {
	Dir string `json:"dir" default:"./data"`
}

// Normalize fills unset fields from the `default` tags and expands a
// leading "~" to the user's home — a literal "~" dir under the process
// cwd is never what the file's author meant.
func (c *StorageConfig) Normalize() {
	ApplyDefaults(c)
	if c.Dir == "~" || strings.HasPrefix(c.Dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			c.Dir = home + c.Dir[1:]
		}
	}
}
