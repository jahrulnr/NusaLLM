package enum

// Modality is a content kind a model accepts as input or emits as output,
// matching the gateway catalog's modality lists.
type Modality string

const (
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
	ModalityAudio Modality = "audio"
	ModalityVideo Modality = "video"
	ModalityFile  Modality = "file"
)

func (m Modality) String() string {
	return string(m)
}

func ParseModality(v string) (Modality, bool) {
	switch m := Modality(v); m {
	case ModalityText, ModalityImage, ModalityAudio, ModalityVideo, ModalityFile:
		return m, true
	}
	return "", false
}
