package valueobject

import "testing"

// IDs are path segments and wire identities. They are copied into
// filenames, JSON, URLs, and terminal/log lines; control bytes have no
// representation there. Accepting them lets one client mint an id that is
// not addressable by the next request and can break the record it names.
func TestValidIDRejectsControlCharacters(t *testing.T) {
	for _, raw := range []string{"a\nb", "a\rb", "a\tb", "a\x00b", "a\x1bb"} {
		if err := ValidID(raw); err == nil {
			t.Errorf("ValidID(%q) accepted a control character", raw)
		}
	}
}
