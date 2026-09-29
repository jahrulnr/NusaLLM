// Package wire holds wire-neutral helpers shared by adapter families —
// URI resolution, base64 materialization. Wire-shaped part assembly stays
// inside each adapter; only the mechanics that never change per wire live
// here.
package wire

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// MaxAttachmentBytes returns the largest local media payload materialized by
// a tool or provider adapter for the given modality.
func MaxAttachmentBytes(modality enum.Modality) int64 {
	switch modality {
	case enum.ModalityImage:
		return 10 << 20
	case enum.ModalityAudio:
		return 50 << 20
	case enum.ModalityVideo:
		return 100 << 20
	default:
		return 20 << 20
	}
}

// MediaRoot binds a provider send to the workspace repository chosen at
// composition time. Empty Dir disables local file references.
type MediaRoot struct {
	Dir   string
	Store drepo.Workspace
}

// ResolveURI materializes an attachment reference into a sendable URL. Local
// file references require fileRoot and are reopened beneath it at send time;
// reads are bounded before allocation and a sha256 pin must still match.
func ResolveURI(a valueobject.Attachment, fileRoot MediaRoot) (string, error) {
	uri, mt := a.URI(), a.MediaType()
	if strings.HasPrefix(uri, "file://") {
		raw, err := readLocal(a, fileRoot)
		if err != nil {
			return "", err
		}
		return DataURI(mt, raw), nil
	}
	if strings.HasPrefix(uri, "data:") {
		marker := strings.Index(uri, ";base64,")
		if marker >= 0 && len(uri)-marker-len(";base64,") > base64.StdEncoding.EncodedLen(int(MaxAttachmentBytes(a.Modality()))) {
			return "", apperror.New(400, "attachment exceeds media limit", nil)
		}
		if a.SHA256() != "" {
			raw, err := decodeDataURI(uri)
			if err != nil {
				return "", err
			}
			if int64(len(raw)) > MaxAttachmentBytes(a.Modality()) {
				return "", apperror.New(400, "attachment exceeds media limit", nil)
			}
			if err := verifyPin(a, raw); err != nil {
				return "", err
			}
		}
		return uri, nil
	}
	if a.SHA256() != "" {
		return "", apperror.New(400, "pinned attachment must be a local reference",
			fmt.Errorf("cannot verify sha256 of remote uri"))
	}
	return uri, nil
}

func readLocal(a valueobject.Attachment, fileRoot MediaRoot) (raw []byte, err error) {
	if fileRoot.Dir == "" || fileRoot.Store == nil {
		return nil, apperror.New(400, "local attachment root is not configured", nil)
	}
	absRoot, err := filepath.Abs(fileRoot.Dir)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(strings.TrimPrefix(a.URI(), "file://"))
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(absRoot, absPath)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, apperror.New(400, "attachment outside workspace", err)
	}
	workspace, err := valueobject.NewWorkspace(relative)
	if err != nil {
		return nil, apperror.New(400, "attachment outside workspace", err)
	}
	root, err := fileRoot.Store.Open(context.Background(), absRoot)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); err == nil {
			err = closeErr
		}
	}()
	f, info, err := root.OpenRegular(workspace.Path())
	if err != nil {
		return nil, apperror.New(400, "attachment unreadable", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()
	limit := MaxAttachmentBytes(a.Modality())
	if info.Size() > limit {
		return nil, apperror.New(400, "attachment exceeds media limit", nil)
	}
	raw, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, apperror.New(400, "attachment unreadable", err)
	}
	if int64(len(raw)) > limit {
		return nil, apperror.New(400, "attachment exceeds media limit", nil)
	}
	if err := verifyPin(a, raw); err != nil {
		return nil, err
	}
	return raw, nil
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
func ResolveBase64(a valueobject.Attachment, fileRoot MediaRoot) (data, format string, err error) {
	uri, mt := a.URI(), a.MediaType()
	if !strings.HasPrefix(uri, "data:") && !strings.HasPrefix(uri, "file://") {
		return "", "", apperror.New(400, "attachment must be a data: or file:// URI",
			fmt.Errorf("unsupported uri scheme: %s", uri))
	}
	u, err := ResolveURI(a, fileRoot)
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
