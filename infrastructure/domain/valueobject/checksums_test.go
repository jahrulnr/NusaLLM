package valueobject

import (
	"strings"
	"testing"
)

const (
	digestA = "2f5d5c4c9b1f0bb1e3c3e0b3a1b3f2d4c5e6f708192a3b4c5d6e7f8091a2b3c4"
	digestB = "0000000000000000000000000000000000000000000000000000000000000000"
)

func TestParseChecksumsReadsSha256sumOutput(t *testing.T) {
	c, err := ParseChecksums(digestA + "  nusallm-linux-amd64\n" + digestB + "  nusallm-linux-arm64\n\n")
	if err != nil {
		t.Fatalf("ParseChecksums error: %v", err)
	}
	if err := c.Verify("nusallm-linux-amd64", digestA); err != nil {
		t.Fatalf("amd64 should verify: %v", err)
	}
	if err := c.Verify("nusallm-linux-arm64", digestB); err != nil {
		t.Fatalf("arm64 should verify: %v", err)
	}
}

func TestParseChecksumsAcceptsBinaryModeMarkerAndUppercase(t *testing.T) {
	c, err := ParseChecksums(strings.ToUpper(digestA) + " *nusallm-linux-amd64")
	if err != nil {
		t.Fatalf("ParseChecksums error: %v", err)
	}
	if err := c.Verify("nusallm-linux-amd64", digestA); err != nil {
		t.Fatalf("binary-mode line should verify: %v", err)
	}
}

func TestParseChecksumsRejectsMalformedLines(t *testing.T) {
	for _, in := range []string{
		digestA,                                 // no name
		digestA + " name extra",                 // too many fields
		"abc  name",                             // digest too short
		strings.Repeat("g", 64) + "  name",      // not hex
		digestA + "  dup\n" + digestB + "  dup", // same name twice
	} {
		if _, err := ParseChecksums(in); err == nil {
			t.Fatalf("ParseChecksums(%q) accepted malformed input", in)
		}
	}
}

func TestVerifyRejectsMismatchAndUnlisted(t *testing.T) {
	c, _ := ParseChecksums(digestA + "  nusallm-linux-amd64")
	if err := c.Verify("nusallm-linux-amd64", digestB); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("mismatch not reported: %v", err)
	}
	if err := c.Verify("nusallm-linux-arm64", digestA); err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("unlisted name not reported: %v", err)
	}
}
