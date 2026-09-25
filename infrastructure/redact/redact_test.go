package redact

import (
	"strings"
	"testing"
)

func TestKeyMasking(t *testing.T) {
	in := map[string]any{
		"api_key":       "sk-live-123456789012",
		"Authorization": "Bearer tok",
		"nested": map[string]any{
			"password": "hunter2",
			"safe":     "ok",
		},
		"list": []any{map[string]any{"x-api-key": "k"}},
	}
	out := Value(in).(map[string]any)
	if out["api_key"] != Mask || out["Authorization"] != Mask {
		t.Fatalf("keys not masked: %v", out)
	}
	if out["nested"].(map[string]any)["password"] != Mask {
		t.Fatal("nested key not masked")
	}
	if out["nested"].(map[string]any)["safe"] != "ok" {
		t.Fatal("safe value touched")
	}
	if out["list"].([]any)[0].(map[string]any)["x-api-key"] != Mask {
		t.Fatal("list-nested key not masked")
	}
}

func TestValuePatterns(t *testing.T) {
	cases := []struct{ in, want string }{
		{"header: Bearer abc.def.ghi", "header: " + Mask},
		{"key sk-liveabc123def456 leaked", "key " + Mask + " leaked"},
		{"google AIzaSyD4iE2xVSpkLLOXoyq2ueu", "google " + Mask},
		{"plain text, no secrets", "plain text, no secrets"},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Fatalf("String(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPEMBlock(t *testing.T) {
	in := "-----BEGIN PRIVATE KEY-----\nabc123\n-----END PRIVATE KEY-----"
	if got := String(in); got != Mask {
		t.Fatalf("pem not masked: %q", got)
	}
}

func TestNonSensitivePassthrough(t *testing.T) {
	in := map[string]any{"model": "x", "usage": map[string]any{"input": 100}}
	out := Value(in).(map[string]any)
	if out["model"] != "x" || out["usage"].(map[string]any)["input"] != 100 {
		t.Fatal("benign payload modified")
	}
	if strings.Contains(Mask, "real") {
		t.Fatal("unreachable")
	}
}
