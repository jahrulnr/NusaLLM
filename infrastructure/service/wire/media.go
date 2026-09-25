// Package encoder holds wire-neutral helpers shared by adapter families —
// URI resolution, base64 materialization. Wire-shaped part assembly stays
// inside each adapter; only the mechanics that never change per wire live
// here.
package wire

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// ResolveURI materializes an attachment reference into a sendable URL:
// data: and http(s) pass through; file:// reads bytes and builds a data
// URI. Existence is checked here — at send time, never in the domain.
func ResolveURI(uri, mediaType string) (string, error) {
	if strings.HasPrefix(uri, "file://") {
		raw, err := os.ReadFile(strings.TrimPrefix(uri, "file://"))
		if err != nil {
			return "", apperror.New(400, "attachment unreadable", err)
		}
		return DataURI(mediaType, raw), nil
	}
	return uri, nil // data:/https:// pass through
}

// DataURI assembles data:<mime>;base64,<raw> — the inline-payload form
// most chat wires accept for media.
func DataURI(mediaType string, raw []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

// ResolveBase64 splits a data: URI (or resolves file:// into one) into raw
// base64 payload + format name — what wires like input_audio expect
// instead of a URL. Audio over plain http(s) is rejected: nothing to
// embed without fetching, and adapters do not fetch arbitrary URLs.
func ResolveBase64(uri, mediaType string) (data, format string, err error) {
	if !strings.HasPrefix(uri, "data:") && !strings.HasPrefix(uri, "file://") {
		return "", "", apperror.New(400, "attachment must be a data: or file:// URI",
			fmt.Errorf("unsupported uri scheme: %s", uri))
	}
	u, err := ResolveURI(uri, mediaType)
	if err != nil {
		return "", "", err
	}
	data, ok := strings.CutPrefix(u, "data:"+mediaType+";base64,")
	if !ok {
		i := strings.Index(u, ";base64,")
		if i < 0 {
			return "", "", apperror.New(400, "invalid data URI",
				fmt.Errorf("missing ;base64, marker"))
		}
		data = u[i+len(";base64,"):] // tolerate a mismatched declared mime
	}
	return data, FormatName(mediaType), nil
}

// FormatName maps a MIME subtype to the format token wires expect:
// audio/mpeg → mp3, audio/x-wav → wav; anything else stays verbatim.
func FormatName(mediaType string) string {
	sub := mediaType[strings.IndexByte(mediaType, '/')+1:]
	switch sub {
	case "mpeg":
		return "mp3"
	case "x-wav", "wave":
		return "wav"
	default:
		return sub
	}
}
