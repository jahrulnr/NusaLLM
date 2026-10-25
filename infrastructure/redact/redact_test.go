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

// Credential-bearing field names real provider and OAuth error payloads
// use — refresh_token, id_token, session_token, token, key,
// private_key — never reach the log unmasked under the default Value
// policy.
func TestCredentialFieldNames(t *testing.T) {
	in := map[string]any{
		"refresh_token": "rt-secret-123",
		"id_token":      "jwt-secret",
		"session_token": "sess-secret",
		"token":         "tok-secret",
		"key":           "generic-key-secret",
		"private_key":   "not-pem-shaped-key",
	}
	out := Value(in).(map[string]any)
	for k := range in {
		if out[k] != Mask {
			t.Fatalf("credential-shaped key %q passed through unmasked: %v", k, out[k])
		}
	}
}

// Common credential shapes beyond Bearer/sk-*/AIza*/PEM mask in
// free-form strings — provider error text embeds them verbatim.
func TestTokenShapes(t *testing.T) {
	shapes := []string{
		"ghp_abcdefghij0123456789abcdefghij",                                        // GitHub PAT
		"gho_abcdefghij0123456789abcdefghij",                                        // GitHub OAuth
		"hf_abcdefghij0123456789abcdefghij",                                         // HuggingFace
		"xoxb-123456789012-abcdefghijklmnop",                                        // Slack bot token
		"sk_live_4eC39HqLyjWDarjtT1zdp7dc",                                          // Stripe live key
		"github_pat_11AAAAAAA0abcdefghij_0123456789abcdefghijklmnopqrstuvwxyzABCDE", // GitHub fine-grained
		"ya29.a0AfB_byAbCdEfGhIjKlMnOpQrStUv",                                       // Google OAuth access token
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", // JWT
	}
	for _, s := range shapes {
		if got := String("token=" + s); got != "token="+Mask {
			t.Fatalf("unmasked token shape survived: %s → %s", s, got)
		}
	}
}

// WithoutBodies deletes the top-level "body" key and masks
// credential-shaped fields through Value — the production redactor wired
// in cmd/nusallm. Free-text fields like "detail" stay verbatim: they are
// diagnostic context, not credential-shaped names (embedded shapes still
// mask via valuePatterns).
func TestWithoutBodiesKeepsCredentialShapedFields(t *testing.T) {
	in := map[string]any{
		"status":        401,
		"body":          map[string]any{"error": "dropped"},
		"refresh_token": "rt-secret-123",
		"detail":        "bad refresh_token rt-secret-123",
		"nested":        map[string]any{"authorization": "Bearer sekrit"},
	}
	out := WithoutBodies("provider.error", in).(map[string]any)
	if _, hasBody := out["body"]; hasBody {
		t.Fatal("body should be dropped")
	}
	if out["refresh_token"] != Mask {
		t.Fatalf("refresh_token field must mask, got %v", out["refresh_token"])
	}
	if n := out["nested"].(map[string]any); n["authorization"] != Mask {
		t.Fatalf("nested authorization must mask, got %v", n["authorization"])
	}
	if out["detail"] != "bad refresh_token rt-secret-123" {
		t.Fatalf("non-credential free text must pass through, got %v", out["detail"])
	}
}
