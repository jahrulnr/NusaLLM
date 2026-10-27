package valueobject

import (
	"errors"
	"time"
)

// Asset is one file published with a release: the name it carries, where it
// downloads from, and its size as the release reports it.
type Asset struct {
	name string
	url  string
	size int64
}

func NewAsset(name, url string, size int64) (Asset, error) {
	a := Asset{name: name, url: url, size: size}
	return a, a.validate()
}

func (a Asset) validate() error {
	if a.name == "" {
		return errors.New("asset name is required")
	}
	if a.url == "" {
		return errors.New("asset url is required")
	}
	if a.size < 0 {
		return errors.New("asset size is negative")
	}
	return nil
}

func (a Asset) Name() string { return a.name }
func (a Asset) URL() string  { return a.url }
func (a Asset) Size() int64  { return a.size }

// Release is one published NusaLLM release as the update check sees it: its
// version, the page a person can read it on, the notes the release carries
// (the CHANGELOG section it was cut from), when it went out, and the files
// attached to it.
type Release struct {
	version     Version
	url         string
	notes       string
	publishedAt time.Time
	assets      []Asset
}

func NewRelease(version Version, url, notes string, publishedAt time.Time, assets []Asset) (Release, error) {
	r := Release{version: version, url: url, notes: notes, publishedAt: publishedAt, assets: assets}
	return r, r.validate()
}

func (r Release) validate() error {
	if r.url == "" {
		return errors.New("release url is required")
	}
	return nil
}

func (r Release) Version() Version       { return r.version }
func (r Release) URL() string            { return r.url }
func (r Release) Notes() string          { return r.notes }
func (r Release) PublishedAt() time.Time { return r.publishedAt }

// Asset returns the file published under name, if the release has one.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.assets {
		if a.name == name {
			return a, true
		}
	}
	return Asset{}, false
}
