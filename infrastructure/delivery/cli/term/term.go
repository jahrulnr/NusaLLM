// Package term holds terminal mechanics shared across the CLI boundary:
// ANSI styling constants and the sanitize/write pair that keeps untrusted
// model or tool output from injecting escape sequences.
package term

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

const (
	Bold    = "\x1b[1m"
	Dim     = "\x1b[2m"
	Reverse = "\x1b[7m"
	Reset   = "\x1b[0m"
)

// Sanitize strips control characters so untrusted model or tool output
// cannot inject escape sequences into the terminal.
func Sanitize(text string) string {
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
	return safe.String()
}

// Write sanitizes then writes — every byte reaching the terminal goes
// through here so injection has no bypass.
func Write(out io.Writer, text string) (int, error) {
	result := Sanitize(text)
	n, err := io.WriteString(out, result)
	if err == nil && n != len(result) {
		return n, io.ErrShortWrite
	}
	return n, err
}
