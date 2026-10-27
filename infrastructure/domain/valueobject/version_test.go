package valueobject

import "testing"

func TestParseVersionAcceptsStrictForm(t *testing.T) {
	cases := map[string]string{
		"0.0.0":  "0.0.0",
		"0.10.4": "0.10.4",
		"12.0.7": "12.0.7",
		"1.2.03": "1.2.3", // leading zeros are digits, rendered canonically
	}
	for in, want := range cases {
		v, err := ParseVersion(in)
		if err != nil {
			t.Fatalf("ParseVersion(%q) error: %v", in, err)
		}
		if v.String() != want {
			t.Fatalf("ParseVersion(%q).String() = %q, want %q", in, v.String(), want)
		}
	}
}

func TestParseVersionRejectsEverythingElse(t *testing.T) {
	for _, in := range []string{
		"", "dev", "v0.1.0", "0.1", "0.1.0.1", "0.1.", ".1.0", "0.-1.0",
		"+0.1.0", "0.1.0-rc1", "0.1.0 ", " 0.1.0", "0.a.0", "1e2.0.0",
		"99999999999999999999999999.0.0",
	} {
		if v, err := ParseVersion(in); err == nil {
			t.Fatalf("ParseVersion(%q) = %v, want error", in, v)
		}
	}
}

func TestNewerThanOrdersNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.0", "0.0.0", true},
		{"0.0.1", "0.0.0", true},
		{"0.10.0", "0.9.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.0.0", "0.0.0", false},
		{"0.0.0", "0.0.1", false},
		{"0.9.9", "0.10.0", false},
	}
	for _, c := range cases {
		a, _ := ParseVersion(c.a)
		b, _ := ParseVersion(c.b)
		if got := a.NewerThan(b); got != c.want {
			t.Fatalf("%s.NewerThan(%s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
