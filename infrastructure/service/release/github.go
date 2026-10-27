// Package release is the outbound release adapter: it asks the GitHub Releases
// API for the newest published release of the project and streams the files
// attached to it.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// maxBody caps the release document. The notes are the only large field, and
// a release body this size is already far past any changelog section.
const maxBody = 1 << 20

// GitHub reads releases and downloads their files over HTTPS. The client
// carries no global timeout: each call is bounded by the context its caller
// passes, because a binary download legitimately outlasts any fixed deadline.
type GitHub struct {
	client  *http.Client
	baseURL string
	repo    string
	agent   string
}

// NewGitHub binds the adapter to an API base (https://api.github.com in
// production), an owner/name repository, and the User-Agent GitHub requires.
func NewGitHub(client *http.Client, baseURL, repo, userAgent string) *GitHub {
	return &GitHub{client: client, baseURL: strings.TrimRight(baseURL, "/"), repo: repo, agent: userAgent}
}

// Latest asks for /repos/{repo}/releases/latest. GitHub's "latest" already
// skips drafts and prereleases, so whatever comes back is a published release.
func (g *GitHub) Latest(ctx context.Context) (valueobject.Release, error) {
	req, err := g.request(ctx, g.baseURL+"/repos/"+g.repo+"/releases/latest")
	if err != nil {
		return valueobject.Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := g.client.Do(req)
	if err != nil {
		return valueobject.Release{}, fmt.Errorf("release check: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return valueobject.Release{}, fmt.Errorf("release check: GitHub answered %s", res.Status)
	}
	var doc struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&doc); err != nil {
		return valueobject.Release{}, fmt.Errorf("release check: decode: %w", err)
	}
	version, err := valueobject.ParseVersion(doc.TagName)
	if err != nil {
		return valueobject.Release{}, fmt.Errorf("release check: %w", err)
	}
	assets := make([]valueobject.Asset, 0, len(doc.Assets))
	for _, a := range doc.Assets {
		asset, err := valueobject.NewAsset(a.Name, a.URL, a.Size)
		if err != nil {
			return valueobject.Release{}, fmt.Errorf("release check: %w", err)
		}
		assets = append(assets, asset)
	}
	return valueobject.NewRelease(version, doc.HTMLURL, doc.Body, doc.PublishedAt, assets)
}

// Fetch streams the file at url into dst and refuses anything larger than
// limit. A non-200 answer fails before any byte reaches dst.
func (g *GitHub) Fetch(ctx context.Context, url string, dst io.Writer, limit int64) (int64, error) {
	req, err := g.request(ctx, url)
	if err != nil {
		return 0, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download: %s answered %s", url, res.Status)
	}
	n, err := io.Copy(dst, io.LimitReader(res.Body, limit+1))
	if err != nil {
		return n, fmt.Errorf("download: %w", err)
	}
	if n > limit {
		return n, fmt.Errorf("download: file exceeds %d bytes", limit)
	}
	return n, nil
}

func (g *GitHub) request(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", g.agent)
	return req, nil
}
