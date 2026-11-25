package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// ResolveAttachment reads the requested file, detects its media type,
// enforces the modality size limit, and pins the bytes by hash so the later
// wire-time read resolves only what the user selected.
//
// The argument is resolved by the workspace port, exactly like every other
// path argument: a leading ~ is the owner's home, a file: URL names the file
// it says, and confinement stays the port's decision. Reading it here would
// be a second, weaker rule — the defect this replaces turned ~ into a folder
// named "~" inside the workspace.
func (w *Root) ResolveAttachment(ctx context.Context, requested string) (attachment valueobject.Attachment, err error) {
	placement, err := w.store.LocateRead(ctx, w.dir, requested)
	if err != nil {
		return valueobject.Attachment{}, err
	}
	defer func() {
		if closeErr := placement.Close(); err == nil {
			err = closeErr
		}
	}()
	att, _, err := PinAttachment(ctx, placement, requested)
	return att, err
}

// PinAttachment turns one placed file into an attachment: it sniffs the
// bytes (the extension is caller-supplied and proves nothing), refuses text,
// enforces the modality cap, and hashes the payload through the same handle
// it read so the later wire-time resolution verifies exactly this content.
//
// It is the only implementation of that rule. /attach and the read_media tool
// both resolve placement through the workspace port and then come here, so a
// path cannot be confined twice as well as pinned twice.
// The byte count comes back with it so a caller can describe what it loaded
// without opening the placement a second time.
func PinAttachment(ctx context.Context, placement drepo.Placement, requested string) (attachment valueobject.Attachment, size int64, err error) {
	f, info, err := placement.Open(ctx)
	if err != nil {
		return valueobject.Attachment{}, 0, fmt.Errorf("%q is not a readable regular file: %w", requested, err)
	}
	size = info.Size()
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	// Sniff the first 512 bytes — DetectContentType's window.
	prefix := make([]byte, 512)
	n, err := io.ReadFull(f, prefix)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return valueobject.Attachment{}, 0, err
	}
	prefix = prefix[:n]
	mediaType := http.DetectContentType(prefix)
	att, err := valueobject.NewAttachment(placement.URI(), mediaType)
	if err != nil {
		return valueobject.Attachment{}, 0, err
	}
	modality := att.Modality()
	if modality == enum.ModalityText {
		// Sniffed text (a .csv IS text) belongs to read_file.
		return valueobject.Attachment{}, 0, fmt.Errorf("%q is text (%s) — use read_file instead", requested, mediaType)
	}

	limit := valueobject.MaxAttachmentBytes(modality)
	if size > limit {
		return valueobject.Attachment{}, 0, fmt.Errorf("%q exceeds the %d MiB limit for %s media", requested, limit>>20, modality)
	}

	h := sha256.New()
	_, _ = h.Write(prefix)
	rest, err := io.Copy(h, io.LimitReader(f, limit+1-int64(len(prefix))))
	if err != nil {
		return valueobject.Attachment{}, 0, err
	}
	if int64(n)+rest > limit {
		return valueobject.Attachment{}, 0, fmt.Errorf("%q exceeds the %d MiB limit for %s media", requested, limit>>20, modality)
	}
	att.SetSHA256(hex.EncodeToString(h.Sum(nil)))
	att.SetName(filepath.Base(info.Name()))
	return att, size, nil
}
