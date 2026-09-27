// Package web embeds the production WebUI assets into the binary so the
// built nusallm executable serves the interface without the source tree.
package web

import "embed"

//go:embed index.html app.js favicon.svg styles/*.css lib/*.js widgets/*/*.js widgets/*/*.css
var Assets embed.FS
