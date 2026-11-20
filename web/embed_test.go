package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAssetsContainEverySurface(t *testing.T) {
	for _, name := range []string{
		"index.html", "app.js", "favicon.svg", "styles/app.css",
		"lib/api.js", "lib/chat.js", "lib/diagram.js", "lib/markdown.js", "lib/socket.js", "lib/store.js", "lib/turn.js",
		"components/chat.js", "components/composer.js", "components/icons.js", "components/pickers.js",
		"components/settings.js", "components/sidebar.js", "components/transcript.js", "components/ui.js",
		"vendor/preact/htm-preact.mjs", "vendor/marked/marked.esm.js", "vendor/dompurify/purify.es.mjs",
		"vendor/highlight/highlight.min.mjs", "vendor/tailwind/tailwind.browser.js",
		"vendor/slim-select/slimselect.es.js", "vendor/slim-select/slimselect.css", "vendor/pele/pele.global.js",
	} {
		if _, err := fs.Stat(Assets, name); err != nil {
			t.Errorf("missing embedded asset %s: %v", name, err)
		}
	}
}

// Every script and stylesheet is served from this origin — no CDN.
func TestIndexLoadsOnlyLocalAssets(t *testing.T) {
	raw, err := fs.ReadFile(Assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"http://", "https://", "//cdn"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("index.html references external resource %q", bad)
		}
	}
}
