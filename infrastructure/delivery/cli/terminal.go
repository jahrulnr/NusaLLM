package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

func writeTerminal(out io.Writer, text string) (int, error) {
	var safe strings.Builder
	safe.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t' || unicode.IsPrint(r):
			safe.WriteRune(r)
		case r <= 0xffff:
			fmt.Fprintf(&safe, "\\u%04x", r)
		default:
			fmt.Fprintf(&safe, "\\U%08x", r)
		}
	}
	result := safe.String()
	n, err := io.WriteString(out, result)
	if err == nil && n != len(result) {
		return n, io.ErrShortWrite
	}
	return n, err
}
