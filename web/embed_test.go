package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestAssetsContainEverySurface(t *testing.T) {
	required := []string{
		"index.html",
		"app.js",
		"favicon.svg",
		"styles/theme.css",
		"lib/api-client.js",
		"lib/socket-client.js",
		"lib/dom.js",
		"lib/icons.js",
		"lib/markdown.js",
		"lib/highlight.js",
		"lib/turn-projection.js",
		"lib/mermaid-runtime.js",
		"vendor/mermaid/mermaid.min.js",
		"vendor/mermaid/LICENSE",
		"vendor/mermaid/README.md",
	}
	widgets := []string{
		"app-shell", "nav-sidebar", "conversation-header", "message-thread",
		"chat-composer", "model-picker", "settings-page", "provider-manager",
		"settings-editor", "tool-catalog", "toast-stack",
		"markdown-view", "code-block", "thinking-block", "tool-activity",
		"mermaid-diagram",
	}
	for _, w := range widgets {
		required = append(required,
			"widgets/"+w+"/"+w+".js",
			"widgets/"+w+"/"+w+".css",
		)
	}
	for _, name := range required {
		if _, err := fs.Stat(Assets, name); err != nil {
			t.Errorf("missing embedded asset %s: %v", name, err)
		}
	}
}

// The entry document must stay a wrapper: it loads the theme, mounts
// <nusa-app>, and imports /app.js — no application markup lives here.
func TestIndexIsWrapperOnly(t *testing.T) {
	raw, err := fs.ReadFile(Assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	for _, want := range []string{
		`<nusa-app>`,
		`href="/styles/theme.css"`,
		`src="/app.js"`,
		`type="module"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	banned := regexp.MustCompile(`<(form|section|article|input|textarea|button)\b`)
	if m := banned.FindString(html); m != "" {
		t.Errorf("index.html carries application markup %q — widgets own that", m)
	}
	// The pinned vendor bundle is a classic script that must register
	// globalThis.mermaid before the app module evaluates.
	vendor := strings.Index(html, `src="/vendor/mermaid/mermaid.min.js"`)
	app := strings.Index(html, `src="/app.js"`)
	if vendor < 0 || app < 0 || vendor > app {
		t.Errorf("index.html must load the vendored mermaid bundle before /app.js")
	}
}

// app-shell upgrades <nusa-app> synchronously on definition; importing
// it before the leaves would let property writes land as expandos that
// shadow the real setters. The import must come last.
func TestAppJSImportsShellLast(t *testing.T) {
	raw, err := fs.ReadFile(Assets, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	shell := strings.Index(src, "app-shell/app-shell.js")
	if shell < 0 {
		t.Fatal("app.js does not import app-shell")
	}
	for _, w := range []string{
		"nav-sidebar", "conversation-header", "message-thread",
		"chat-composer", "model-picker", "settings-page", "provider-manager",
		"settings-editor", "tool-catalog", "toast-stack",
		"markdown-view", "code-block", "thinking-block", "tool-activity",
		"mermaid-diagram",
	} {
		leaf := strings.Index(src, "widgets/"+w+"/"+w+".js")
		if leaf < 0 {
			t.Errorf("app.js does not import widget %s", w)
		} else if leaf > shell {
			t.Errorf("app.js imports %s after app-shell — upgrade order broken", w)
		}
	}
	// code-block must define before markdown-view, which instantiates it;
	// all four renderers define before message-thread, their consumer.
	orders := [][2]string{
		{"code-block", "markdown-view"},
		{"mermaid-diagram", "markdown-view"},
		{"mermaid-diagram", "message-thread"},
		{"code-block", "message-thread"},
		{"markdown-view", "message-thread"},
		{"thinking-block", "message-thread"},
		{"tool-activity", "message-thread"},
	}
	for _, pair := range orders {
		a := strings.Index(src, "widgets/"+pair[0]+"/"+pair[0]+".js")
		b := strings.Index(src, "widgets/"+pair[1]+"/"+pair[1]+".js")
		if a < 0 || b < 0 {
			t.Errorf("missing import for %s or %s", pair[0], pair[1])
		} else if a > b {
			t.Errorf("app.js imports %s after %s", pair[0], pair[1])
		}
	}
}
