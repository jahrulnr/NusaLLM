package aggregate

import (
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
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
