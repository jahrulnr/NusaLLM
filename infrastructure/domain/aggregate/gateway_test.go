package aggregate

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func TestNewGateway(t *testing.T) {
	for _, c := range []struct {
		id, endpoint string
		style        enum.Style
	}{
		{"", "https://x", enum.StyleMessages},
		{"g", "", enum.StyleMessages},
		{"g", "https://x", ""},
	} {
		if _, err := NewGateway(c.id, "n", c.endpoint, c.style); err == nil {
			t.Fatalf("expected error for %+v", c)
		}
	}
	g, err := NewGateway("g", "n", "https://x", enum.StyleCompletion)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	if !g.StreamMode() {
		t.Fatal("stream should default on")
	}
	g.DisableStream()
	if g.StreamMode() {
		t.Fatal("DisableStream did not take")
	}
}

// Style resolves the wire driver — an unknown value passes validate today
// and then fails TextService resolution on every turn. The constructor
// must reject what no style registry contains, the same decision the
// store re-runs on hydrate.
func TestNewGatewayRejectsUnknownStyle(t *testing.T) {
	if _, err := NewGateway("g", "n", "https://x", enum.Style("bogus")); err == nil {
		t.Fatal("constructor accepted a style no driver speaks")
	}
}
