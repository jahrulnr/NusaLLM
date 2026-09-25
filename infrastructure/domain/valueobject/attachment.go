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
// in the domain.
type Attachment struct {
	uri       string // https://…, data:<mime>;base64,…, file://…, or adapter-resolved ref
	mediaType string // MIME type, e.g. "image/png", "audio/mpeg", "video/mp4"
	name      string // optional filename — required by providers for file inputs
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

func (a *Attachment) SetName(v string) { a.name = v }

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
