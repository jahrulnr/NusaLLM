package wire

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// ScanSSE returns a Scanner over an SSE stream that yields one event block
// per token. Blocks are separated by blank lines. Large payloads are
// tolerated — provider chunks can carry full messages.
func ScanSSE(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Split(SplitSSE)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	return sc
}

// SplitSSE is a bufio.SplitFunc cutting on blank-line event boundaries.
// Every SSE-speaking wire (OpenAI-compat, Anthropic, Gemini) frames the
// same way; only the payload grammar differs per family.
func SplitSSE(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.Index(data, []byte("\r\n\r\n")); i >= 0 {
		return i + 4, data[:i], nil
	}
	if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		return i + 2, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// SSEData joins a block's data: lines into one payload, skipping keepalive
// comments (":") and blank lines. Named event: lines are not interpreted —
// families that need them parse the block themselves.
func SSEData(block []byte) []byte {
	var payload bytes.Buffer
	for _, line := range strings.Split(string(block), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			if payload.Len() > 0 {
				payload.WriteByte('\n')
			}
			payload.WriteString(strings.TrimSpace(d))
		}
	}
	if payload.Len() == 0 {
		return nil
	}
	return payload.Bytes()
}
