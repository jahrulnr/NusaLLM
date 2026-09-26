package term

import (
	"bytes"
	"testing"
)

func TestWriteTerminalEscapesControlCharacters(t *testing.T) {
	var out bytes.Buffer
	input := "red\x1b[31m\rtext\n\tend"
	if _, err := Write(&out, input); err != nil {
		t.Fatal(err)
	}
	want := `red\u001b[31m\u000dtext
	end`
	if got := out.String(); got != want {
		t.Fatalf("safe terminal text = %q, want %q", got, want)
	}
}
