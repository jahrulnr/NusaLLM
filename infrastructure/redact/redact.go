// Package redact scrubs credential material out of diagnostic payloads —
// raw provider bodies echo request headers, error details quote auth
// headers, so anything a Logger persists passes through here first.
// Pure functions over JSON-shaped values; lives in infrastructure because
// the payloads it scrubs are wire shapes the domain never sees.
package redact

import (
	"encoding/json"
	"regexp"
)

// Mask replaces sensitive material.
const Mask = "***"

// keyPattern matches map keys that carry credentials — case-insensitive,
// tolerant of separators (api_key, api-key, apikey). The token/key/private
// alternatives are deliberately broad: OAuth and provider error payloads
// name credential fields refresh_token, id_token, session_token, token,
// key, and private_key — over-masking a "key" field in diagnostics beats
// logging a live credential.
var keyPattern = regexp.MustCompile(`(?i)(api[-_]?key|access[-_]?token|auth[-_]?token|authorization|bearer|credential|password|secret|token|key|private|cookie)`)

// valuePatterns mask secrets hiding in otherwise-innocent string fields —
// token shapes, auth header values, PEM blocks.
var valuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+\S+`),                                           // Authorization header value
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`),                                     // OpenAI/OpenRouter-style keys
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{20,}`),                                   // Google API keys
	regexp.MustCompile(`\bgh[opsur]_[A-Za-z0-9]{20,}`),                               // GitHub ghp_/gho_/ghu_/ghs_/ghr_
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),                             // GitHub fine-grained PAT
	regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}`),                                      // HuggingFace
	regexp.MustCompile(`\bxox[bapors]-[A-Za-z0-9-]{10,}`),                            // Slack tokens
	regexp.MustCompile(`\b[sr]k_(live|test)_[A-Za-z0-9]{10,}`),                       // Stripe secret/restricted keys
	regexp.MustCompile(`\bwhsec_[A-Za-z0-9]{10,}`),                                   // Stripe-style webhook secrets
	regexp.MustCompile(`\bya29\.[A-Za-z0-9_-]{10,}`),                                 // Google OAuth access tokens
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`), // JWTs
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// Value deep-scrubs a JSON-shaped value (map[string]any, []any, string,
// scalars). Sensitive keys and secret-looking strings are masked; other
// values pass through untouched. Unknown nested types are left as-is —
// callers marshal to JSON anyway.
func Value(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			if keyPattern.MatchString(k) {
				out[k] = Mask
				continue
			}
			out[k] = Value(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = Value(x)
		}
		return out
	case string:
		return String(t)
	default:
		return v
	}
}

// String masks secret patterns inside a free-form string — the value of a
// non-sensitive key may still contain a token.
func String(s string) string {
	for _, p := range valuePatterns {
		s = p.ReplaceAllString(s, Mask)
	}
	return s
}

// WithoutBodies is a Logger Options.Redact policy: it keeps diagnostic
// metadata without persisting conversation content. provider.request is
// logged as unmarshaled JSON (the whole body), not a map with a "body"
// key — omit it entirely. Response/error maps still drop only their
// "body" field so status and request_id survive.
func WithoutBodies(kind string, e any) any {
	e = Value(e)
	if kind == "provider.request" {
		return map[string]any{"omitted": true}
	}
	switch v := e.(type) {
	case map[string]any:
		delete(v, "body")
		return v
	case []json.RawMessage:
		return map[string]any{"chunks": len(v)}
	case []any:
		return map[string]any{"chunks": len(v)}
	default:
		return map[string]any{"omitted": true}
	}
}
