// Package skills embeds NusaLLM's builtin agent skills.
package skills

import (
	"embed"
	"io/fs"
)

//go:embed all:skill-creator all:tool-creator
var embedded embed.FS

// Builtins returns the embedded skill tree for application installation.
func Builtins() fs.FS { return embedded }
