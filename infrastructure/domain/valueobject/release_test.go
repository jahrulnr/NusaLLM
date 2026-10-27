package valueobject

import (
	"testing"
	"time"
)

func TestNewReleaseRequiresURL(t *testing.T) {
	v, _ := ParseVersion("0.1.0")
	if _, err := NewRelease(v, "", "notes", time.Time{}, nil); err == nil {
		t.Fatal("NewRelease with empty url succeeded")
	}
}

func TestNewReleaseKeepsFieldsAndAssets(t *testing.T) {
	v, _ := ParseVersion("0.1.0")
	at := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	bin, err := NewAsset("nusallm-linux-amd64", "https://example.test/bin", 42)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRelease(v, "https://example.test/r/0.1.0", "### Added", at, []Asset{bin})
	if err != nil {
		t.Fatal(err)
	}
	if r.Version().String() != "0.1.0" || r.URL() != "https://example.test/r/0.1.0" ||
		r.Notes() != "### Added" || !r.PublishedAt().Equal(at) {
		t.Fatalf("fields lost: %+v", r)
	}
	got, ok := r.Asset("nusallm-linux-amd64")
	if !ok || got.URL() != "https://example.test/bin" || got.Size() != 42 {
		t.Fatalf("asset lost: %+v ok=%v", got, ok)
	}
	if _, ok := r.Asset("missing"); ok {
		t.Fatal("unknown asset reported as present")
	}
}

func TestNewAssetValidates(t *testing.T) {
	if _, err := NewAsset("", "https://x.test/a", 1); err == nil {
		t.Fatal("nameless asset accepted")
	}
	if _, err := NewAsset("a", "", 1); err == nil {
		t.Fatal("urlless asset accepted")
	}
	if _, err := NewAsset("a", "https://x.test/a", -1); err == nil {
		t.Fatal("negative size accepted")
	}
}
