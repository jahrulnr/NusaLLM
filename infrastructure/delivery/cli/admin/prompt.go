package admin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"golang.org/x/term"
)

// defaultEndpoint prefills the endpoint prompt with the flagship provider
// for the chosen wire style. The hostnames come from the domain's provider
// registry (enum.Provider); scheme and base path are delivery defaults.
func defaultEndpoint(style enum.Style) string {
	if style == enum.StyleMessages {
		return "https://" + enum.Anthropic.String()
	}
	return "https://" + enum.OpenAI.String() + "/v1"
}

// promptField prints "label [default]: " and returns the line or the
// default on bare Enter.
func promptField(in *bufio.Reader, out *bufio.Writer, label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(out, "%s [%s]: ", strings.TrimRight(label, " "), def)
	} else {
		fmt.Fprintf(out, "%s: ", strings.TrimRight(label, " "))
	}
	out.Flush()
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	if v := strings.TrimSpace(line); v != "" {
		return v, nil
	}
	return def, nil
}

// promptSecret reads one hidden line — the terminal echo is off while
// ReadPassword runs, so the key never lands on screen or in scrollback.
// buffered carries the field prompter's reader: a multi-line paste can
// leave the key line already buffered there, where ReadPassword's raw
// fd read would hang waiting for bytes the buffer already holds.
func promptSecret(stdin *os.File, out *bufio.Writer, label string, buffered *bufio.Reader) (string, error) {
	return readSecretLine(stdin, out, label+": ", buffered)
}

// readSecretLine prints a sudo-style "API key: " prompt to w, then reads
// one line of secret material. On a TTY the read is hidden (echo off); on
// a pipe it consumes one line — the prompt still prints so a scripted
// caller can see what was expected. When buffered carries pending bytes
// (a paste the field prompter already consumed), the line comes from the
// buffer instead of a hidden fd read — echo was off only in effect, so
// the line is treated as secret input regardless.
func readSecretLine(stdin io.Reader, w io.Writer, label string, buffered ...*bufio.Reader) (string, error) {
	fmt.Fprint(w, label)
	if fl, ok := w.(*bufio.Writer); ok {
		fl.Flush()
	}
	if len(buffered) > 0 && buffered[0] != nil && buffered[0].Buffered() > 0 {
		line, err := buffered[0].ReadString('\n')
		fmt.Fprintln(w)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return sanitizeSecret(line), nil
	}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(w)
		if fl, ok := w.(*bufio.Writer); ok {
			fl.Flush()
		}
		return sanitizeSecret(string(raw)), err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return sanitizeSecret(line), nil
}

// sanitizeSecret strips bytes that can never be part of a key — control
// characters and the bracketed-paste markers a terminal wraps around a
// paste. A stray ESC landing in the vault produced an unbuildable
// Authorization header at send time.
func sanitizeSecret(s string) string {
	s = strings.ReplaceAll(s, "\x1b[200~", "") // paste start
	s = strings.ReplaceAll(s, "\x1b[201~", "") // paste end
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func promptAborted(out *bufio.Writer, _ error) int {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "cancelled.")
	out.Flush()
	return 1
}
