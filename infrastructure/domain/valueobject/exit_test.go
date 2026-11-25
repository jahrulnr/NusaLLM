package valueobject

import (
	"encoding/json"
	"testing"
)

// The wire form is one answer: a code, or a signal — never both, and never a
// bare number. A consumer that deep-equals the object must see exactly
// {"code":3} / {"code":0} / {"signal":"killed"}.
func TestExitStatusWireShape(t *testing.T) {
	cases := []struct {
		label string
		in    ExitStatus
		want  string
	}{
		{"clean exit", NewExitStatus(0, ""), `{"code":0}`},
		{"nonzero exit", NewExitStatus(3, ""), `{"code":3}`},
		{"signal death", NewExitStatus(-1, "killed"), `{"signal":"killed"}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if string(got) != tc.want {
			t.Fatalf("%s: %s, want %s", tc.label, got, tc.want)
		}
		var back ExitStatus
		if err := json.Unmarshal(got, &back); err != nil {
			t.Fatalf("%s: round trip: %v", tc.label, err)
		}
		if back != tc.in {
			t.Fatalf("%s: round trip = %+v, want %+v", tc.label, back, tc.in)
		}
	}
}

// A status that carries both halves, neither, or a foreign key is not a shape
// this version writes — refuse it instead of guessing.
func TestExitStatusRejectsMalformedShapes(t *testing.T) {
	for _, raw := range []string{`{"code":1,"signal":"killed"}`, `{}`, `{"code":1,"other":2}`, `3`} {
		var out ExitStatus
		if err := json.Unmarshal([]byte(raw), &out); err == nil {
			t.Fatalf("%s decoded to %+v, want an error", raw, out)
		}
	}
}

func TestExitStatusFailed(t *testing.T) {
	if NewExitStatus(0, "").Failed() {
		t.Fatal("a clean exit is not a failure")
	}
	if !NewExitStatus(3, "").Failed() {
		t.Fatal("a nonzero exit is a failure")
	}
	if !NewExitStatus(0, "killed").Failed() {
		t.Fatal("a signal death is a failure")
	}
}
