package valueobject

import (
	"os"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func TestNewAttachment(t *testing.T) {
	t.Run("rejects empty uri", func(t *testing.T) {
		if _, err := NewAttachment("", "image/png"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("rejects invalid media type", func(t *testing.T) {
		if _, err := NewAttachment("https://x/y.png", "not a mime"); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("accepts data uri", func(t *testing.T) {
		a, err := NewAttachment("data:image/png;base64,aGk=", "image/png")
		if err != nil {
			t.Fatalf("NewAttachment: %v", err)
		}
		if a.URI() == "" || a.MediaType() != "image/png" {
			t.Fatalf("unexpected attachment: %+v", a)
		}
	})
}

func TestAttachmentModality(t *testing.T) {
	cases := map[string]enum.Modality{
		"image/png":       enum.ModalityImage,
		"audio/mpeg":      enum.ModalityAudio,
		"video/mp4":       enum.ModalityVideo,
		"text/plain":      enum.ModalityText,
		"application/pdf": enum.ModalityFile,
	}
	for mt, want := range cases {
		a, err := NewAttachment("https://x/f", mt)
		if err != nil {
			t.Fatalf("%s: %v", mt, err)
		}
		if got := a.Modality(); got != want {
			t.Fatalf("Modality(%s) = %s, want %s", mt, got, want)
		}
	}
}

// TestMediaFixtures pins the real sample files under test/media/ — adapters
// use them for end-to-end attachment serialization.
func TestMediaFixtures(t *testing.T) {
	fixtures := map[string]struct {
		mediaType string
		modality  enum.Modality
	}{
		"image.png": {"image/png", enum.ModalityImage},
		"video.mp4": {"video/mp4", enum.ModalityVideo},
		"audio.m4a": {"audio/mp4", enum.ModalityAudio},
	}
	for file, want := range fixtures {
		path := "../../../test/media/" + file
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", path)
		}
		a, err := NewAttachment("file://"+path, want.mediaType)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got := a.Modality(); got != want.modality {
			t.Fatalf("%s modality = %s, want %s", file, got, want.modality)
		}
	}
}
