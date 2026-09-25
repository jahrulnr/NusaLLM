package valueobject

import (
	"crypto/rand"
	"errors"
	"strings"
)

// ID is the identity value object: a single path-safe segment — ids name
// files and directories downstream, so separators and parent references
// are never legal. An empty raw value mints a collision-free id
// (prefix_<base32>); a supplied one is validated. Repositories re-guard
// the same rule at their own boundary via ValidID.
type ID struct{ v string }

// ValidID is the identity rule, exported for boundary guards — rejects
// empty and path-element-bearing values.
func ValidID(raw string) error {
	if raw == "" {
		return errors.New("id is required")
	}
	if raw == "." || raw == ".." || strings.ContainsAny(raw, "/\\") {
		return errors.New("id must not contain path elements")
	}
	return nil
}

// NewID returns a validated id. raw "" mints under prefix; otherwise raw
// must satisfy the path-safe rule.
func NewID(prefix, raw string) (ID, error) {
	if raw == "" {
		return ID{v: prefix + "_" + rand.Text()}, nil // 128-bit, no error surface
	}
	if err := ValidID(raw); err != nil {
		return ID{}, err
	}
	return ID{v: raw}, nil
}

func (i ID) String() string { return i.v }
