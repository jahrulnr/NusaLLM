// Package wire holds wire-neutral helpers shared by adapter families —
// URI resolution, base64 materialization. Wire-shaped part assembly stays
// inside each adapter; only the mechanics that never change per wire live
// here.
package wire

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// ResolveURI materializes an attachment reference into a sendable URL:
// data: and http(s) pass through; file:// reads bytes and builds a data
// URI. Existence is checked here — at send time, never in the domain.
// An attachment carrying a sha256 pin resolves only to the recorded
// bytes: a swapped or rewritten local file fails closed instead of
// silently sending different content than the producer checked. Pins
// cannot bind remote URIs — there is nothing to hash without fetching.
func ResolveURI(a valueobject.Attachment) (string, error) {
	uri, mt := a.URI(), a.MediaType()
	if strings.HasPrefix(uri, "file://") {
		raw, err := os.ReadFile(strings.TrimPrefix(uri, "file://"))
		if err != nil {
			return "", apperror.New(400, "attachment unreadable", err)
		}
		if err := verifyPin(a, raw); err != nil {
			return "", err
		}
		return DataURI(mt, raw), nil
	}
	if strings.HasPrefix(uri, "data:") && a.SHA256() != "" {
		raw, err := decodeDataURI(uri)
		if err != nil {
			return "", err
		}
		if err := verifyPin(a, raw); err != nil {
			return "", err
		}
		return uri, nil
	}
	if a.SHA256() != "" {
		return "", apperror.New(400, "pinned attachment must be a local reference",
			fmt.Errorf("cannot verify sha256 of remote uri"))
	}
	return uri, nil
}

// verifyPin enforces the optional content pin: resolved bytes must hash
// to the digest the producer recorded at check time.
func verifyPin(a valueobject.Attachment, raw []byte) error {
	if a.SHA256() == "" {
		return nil
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != a.SHA256() {
		return apperror.New(400, "attachment content changed",
			fmt.Errorf("sha256 mismatch: got %s, want %s", got, a.SHA256()))
	}
	return nil
}

// decodeDataURI extracts the base64 payload of a data: URI.
func decodeDataURI(uri string) ([]byte, error) {
	i := strings.Index(uri, ";base64,")
	if i < 0 {
		return nil, apperror.New(400, "invalid data URI",
			fmt.Errorf("missing ;base64, marker"))
	}
	raw, err := base64.StdEncoding.DecodeString(uri[i+len(";base64,"):])
	if err != nil {
		return nil, apperror.New(400, "invalid data URI", err)
	}
	return raw, nil
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
func ResolveBase64(a valueobject.Attachment) (data, format string, err error) {
	uri, mt := a.URI(), a.MediaType()
	if !strings.HasPrefix(uri, "data:") && !strings.HasPrefix(uri, "file://") {
		return "", "", apperror.New(400, "attachment must be a data: or file:// URI",
			fmt.Errorf("unsupported uri scheme: %s", uri))
	}
	u, err := ResolveURI(a)
	if err != nil {
		return "", "", err
	}
	data, ok := strings.CutPrefix(u, "data:"+mt+";base64,")
	if !ok {
		i := strings.Index(u, ";base64,")
		if i < 0 {
			return "", "", apperror.New(400, "invalid data URI",
				fmt.Errorf("missing ;base64, marker"))
		}
		data = u[i+len(";base64,"):] // tolerate a mismatched declared mime
	}
	return data, FormatName(mt), nil
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
