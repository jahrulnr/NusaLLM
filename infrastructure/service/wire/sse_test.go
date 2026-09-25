package wire

import (
	"strings"
	"testing"
)

// Mixed-delimiter streams must split at the earliest boundary — an
// \n\n-terminated event before a \r\n\r\n one used to be swallowed whole.
func TestSplitSSEMixedDelimiters(t *testing.T) {
	sc := ScanSSE(strings.NewReader("data: a\n\ndata: b\r\n\r\ndata: c\n\n"))
	var events []string
	for sc.Scan() {
		events = append(events, string(SSEData(sc.Bytes())))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if len(events) != len(want) {
		t.Fatalf("events = %v", events)
	}
	for i, e := range events {
		if e != want[i] {
			t.Fatalf("event %d = %q, want %q", i, e, want[i])
		}
	}
}

func TestSSEData(t *testing.T) {
	block := ": keepalive\n\ndata: one\ndata: two\n"
	if got := string(SSEData([]byte(block))); got != "one\ntwo" {
		t.Fatalf("data join = %q", got)
	}
	if got := SSEData([]byte(": only a comment\n")); got != nil {
		t.Fatalf("comment-only block = %q, want nil", got)
	}
}
