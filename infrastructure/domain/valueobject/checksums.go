package valueobject

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Checksums is a parsed SHA256SUMS file: the digest each published asset must
// hash to. It is the file `sha256sum` writes, one "<hex>  <name>" per line.
type Checksums struct {
	digests map[string]string
}

// ParseChecksums reads SHA256SUMS text. Blank lines are skipped. A name listed
// twice is an error: either entry could be the one a reader trusts.
func ParseChecksums(text string) (Checksums, error) {
	c := Checksums{digests: map[string]string{}}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return Checksums{}, fmt.Errorf("checksums line %d: want \"<sha256>  <name>\"", i+1)
		}
		digest := strings.ToLower(fields[0])
		if err := checkDigest(digest); err != nil {
			return Checksums{}, fmt.Errorf("checksums line %d: %w", i+1, err)
		}
		// sha256sum marks binary-mode entries with a leading '*'; the name is the same.
		name := strings.TrimPrefix(fields[1], "*")
		if _, dup := c.digests[name]; dup {
			return Checksums{}, fmt.Errorf("checksums list %s twice", name)
		}
		c.digests[name] = digest
	}
	return c, nil
}

// Verify reports whether digest, the lowercase hex SHA-256 of the bytes it was
// computed from, matches the checksum listed for name.
func (c Checksums) Verify(name, digest string) error {
	want, ok := c.digests[name]
	if !ok {
		return fmt.Errorf("no checksum is listed for %s", name)
	}
	if strings.ToLower(digest) != want {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	return nil
}

func checkDigest(d string) error {
	if len(d) != 64 {
		return errors.New("sha256 digest must be 64 hex characters")
	}
	if _, err := hex.DecodeString(d); err != nil {
		return errors.New("sha256 digest must be hex")
	}
	return nil
}
