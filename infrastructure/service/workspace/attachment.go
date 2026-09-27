package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// ResolveAttachment reads the requested file inside the workspace,
// detects its media type, enforces the modality size limit, and pins the
// bytes by hash so the later wire-time read resolves only what the user
// selected.
func (w *Root) ResolveAttachment(requested string) (attachment valueobject.Attachment, err error) {
	workspace, err := valueobject.NewWorkspace(requested)
	if err != nil {
		return valueobject.Attachment{}, err
	}
	f, info, err := wire.OpenRegularFile(w.root, workspace.Path())
	if err != nil {
		return valueobject.Attachment{}, fmt.Errorf("%q is not a readable regular file: %w", requested, err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	prefix := make([]byte, 512)
	n, err := io.ReadFull(f, prefix)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return valueobject.Attachment{}, err
	}
	prefix = prefix[:n]
	mediaType := http.DetectContentType(prefix)
	att, err := valueobject.NewAttachment("file://"+filepath.Join(w.absPath, workspace.Path()), mediaType)
	if err != nil {
		return valueobject.Attachment{}, err
	}
	modality := att.Modality()
	if modality == enum.ModalityText {
		return valueobject.Attachment{}, fmt.Errorf("%q is text (%s) — use read_file instead", requested, mediaType)
	}

	limit := wire.MaxAttachmentBytes(modality)
	if info.Size() > limit {
		return valueobject.Attachment{}, fmt.Errorf("%q exceeds the %d MiB limit for %s media", requested, limit>>20, modality)
	}

	h := sha256.New()
	_, _ = h.Write(prefix)
	rest, err := io.Copy(h, io.LimitReader(f, limit+1-int64(len(prefix))))
	if err != nil {
		return valueobject.Attachment{}, err
	}
	if int64(n)+rest > limit {
		return valueobject.Attachment{}, fmt.Errorf("%q exceeds the %d MiB limit for %s media", requested, limit>>20, modality)
	}
	att.SetSHA256(hex.EncodeToString(h.Sum(nil)))
	att.SetName(filepath.Base(workspace.Path()))
	return att, nil
}
