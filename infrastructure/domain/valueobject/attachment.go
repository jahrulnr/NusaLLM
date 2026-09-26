package valueobject

import (
	"errors"
	"mime"
	"strings"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
)

// Attachment is a non-text content reference carried by a message. The
// domain stores only the URI — https URLs and base64 data: URIs pass
// through as-is; local paths and provider-specific references (uploaded
// file ids) are resolved by adapters at send time. Raw bytes never live
// in the domain. An optional sha256 pin binds the reference to the exact
// bytes the producer checked, so a swapped or rewritten file fails at
// resolve time instead of silently sending different content.
type Attachment struct {
	uri       string // https://…, data:<mime>;base64,…, file://…, or adapter-resolved ref
	mediaType string // MIME type, e.g. "image/png", "audio/mpeg", "video/mp4"
	name      string // optional filename — required by providers for file inputs
	sha256    string // optional lowercase-hex content pin, verified at resolve time
}

func NewAttachment(uri, mediaType string) (Attachment, error) {
	a := Attachment{uri: uri, mediaType: mediaType}
	return a, a.validate()
}

func (a Attachment) validate() error {
	if a.uri == "" {
		return errors.New("uri is required")
	}
	if _, _, err := mime.ParseMediaType(a.mediaType); err != nil {
		return errors.New("mediaType must be a valid MIME type")
	}
	return nil
}

func (a Attachment) URI() string       { return a.uri }
func (a Attachment) MediaType() string { return a.mediaType }
func (a Attachment) Name() string      { return a.name }
func (a Attachment) SHA256() string    { return a.sha256 }

func (a *Attachment) SetName(v string)   { a.name = v }
func (a *Attachment) SetSHA256(v string) { a.sha256 = v }

// Modality derives the coarse modality from the media type's top-level
// type; anything unrecognized maps to ModalityFile.
func (a Attachment) Modality() enum.Modality {
	switch top, _, _ := strings.Cut(a.mediaType, "/"); top {
	case "image":
		return enum.ModalityImage
	case "audio":
		return enum.ModalityAudio
	case "video":
		return enum.ModalityVideo
	case "text":
		return enum.ModalityText
	default:
		return enum.ModalityFile
	}
}
