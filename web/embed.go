// Package web embeds the WebUI assets into the binary so the built
// nusallm executable serves the interface without the source tree.
package web

import "embed"

//go:embed index.html app.js favicon.svg styles lib components vendor
var Assets embed.FS
