package valueobject

import "strings"

// Key-ref prefixes — the stored-secret scheme shared by gateway records
// and their readers: "env:NAME" resolves from the environment,
// "secret:<key>" hits the SecretStore. Any other value is literal key
// material. Writers persist refs verbatim; readers may surface a ref but
// never the resolved value.
const (
	KeyRefEnv    = "env:"
	KeyRefSecret = "secret:"
)

// IsKeyRef reports stored-secret ref shape.
func IsKeyRef(v string) bool {
	return strings.HasPrefix(v, KeyRefEnv) || strings.HasPrefix(v, KeyRefSecret)
}

// KeyRefName splits a ref into the name it points at. ok is false when v
// is not a ref; a "" name with ok means a bare prefix — a malformed ref
// naming nothing.
func KeyRefName(v string) (name string, ok bool) {
	if n, ok := strings.CutPrefix(v, KeyRefEnv); ok {
		return n, true
	}
	if n, ok := strings.CutPrefix(v, KeyRefSecret); ok {
		return n, true
	}
	return "", false
}

// SecretRef builds the "secret:<key>" form for a vault entry name.
func SecretRef(key string) string { return KeyRefSecret + key }
