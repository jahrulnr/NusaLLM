package valueobject

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a NusaLLM release version in MAJOR.MINOR.PATCH form — the only
// form the release pipeline tags. Labels such as "dev" (a source build with
// no release version) do not parse: they are not versions and cannot be
// ordered against one.
type Version struct {
	major, minor, patch int
}

// ParseVersion reads a strict MAJOR.MINOR.PATCH string: three dot-separated
// runs of ASCII digits and nothing else.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return Version{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: %w", s, err)
		}
		n[i] = v
	}
	return Version{major: n[0], minor: n[1], patch: n[2]}, nil
}

// String renders the version as MAJOR.MINOR.PATCH.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// NewerThan reports whether v sorts strictly after other. Components compare
// as numbers, so 0.10.0 is newer than 0.9.9.
func (v Version) NewerThan(other Version) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch > other.patch
}
