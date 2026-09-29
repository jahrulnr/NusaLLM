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
	// Cut at the EARLIEST boundary — checking \r\n\r\n first lets a later
	// one swallow an \n\n-terminated event sitting before it, so a mixed-
	// delimiter stream would merge (and corrupt) both events.
	i4 := bytes.Index(data, []byte("\r\n\r\n"))
	i2 := bytes.Index(data, []byte("\n\n"))
	switch {
	case i4 >= 0 && (i2 < 0 || i4 < i2):
		return i4 + 4, data[:i4], nil
	case i2 >= 0:
		return i2 + 2, data[:i2], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// SSENamed extracts the block's event: name alongside the joined data
// payload — the dispatch token on Anthropic and OpenAI Responses streams,
// whose event names drive the state machine. OpenAI-compatible blocks
// carry no event: line and return "".
func SSENamed(block []byte) (string, []byte) {
	var name string
	for _, line := range strings.Split(string(block), "\n") {
		line = strings.TrimRight(line, "\r")
		if n, ok := strings.CutPrefix(line, "event:"); ok && name == "" {
			name = strings.TrimSpace(n)
		}
	}
	return name, SSEData(block)
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
