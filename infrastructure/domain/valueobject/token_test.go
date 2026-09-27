package valueobject

import (
	"testing"
)

func TestFormatScaledTokens(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1K"},
		{200_000, "200K"},
		{256_000, "256K"},
		{1_000_000, "1M"},
		{1_500_000, "1.5M"},
	} {
		if got := FormatScaledTokens(tc.n); got != tc.want {
			t.Fatalf("FormatScaledTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
