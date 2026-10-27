package release

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// lastRequest records what the fake server saw; the handler runs on the
// server goroutine, so reads go through the mutex.
type lastRequest struct {
	mu     sync.Mutex
	path   string
	header http.Header
}

func (l *lastRequest) get() (string, http.Header) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path, l.header
}

// newFake serves one fixed response for any request and records the last
// request it saw.
func newFake(t *testing.T, status int, body string) (*httptest.Server, *lastRequest) {
	t.Helper()
	seen := &lastRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		seen.path = r.URL.Path
		seen.header = r.Header.Clone()
		seen.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestLatestReadsTagNotesURLAndAssets(t *testing.T) {
	srv, seen := newFake(t, http.StatusOK, `{
		"tag_name": "0.10.4",
		"html_url": "https://github.com/jahrulnr/NusaLLM/releases/tag/0.10.4",
		"body": "## [0.10.4] - 2026-10-27\n\n### Fixed\n\n- thing",
		"published_at": "2026-10-27T17:00:00Z",
		"name": "ignored field",
		"assets": [
			{"name":"nusallm-linux-amd64","size":42,"browser_download_url":"https://dl.test/amd64"},
			{"name":"SHA256SUMS","size":7,"browser_download_url":"https://dl.test/sums"}
		]
	}`)
	g := NewGitHub(srv.Client(), srv.URL+"/", "jahrulnr/NusaLLM", "nusallm/0.0.0")

	rel, err := g.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest error: %v", err)
	}
	if rel.Version().String() != "0.10.4" {
		t.Fatalf("version = %s", rel.Version())
	}
	if rel.URL() != "https://github.com/jahrulnr/NusaLLM/releases/tag/0.10.4" {
		t.Fatalf("url = %s", rel.URL())
	}
	if !strings.HasPrefix(rel.Notes(), "## [0.10.4]") {
		t.Fatalf("notes = %q", rel.Notes())
	}
	if !rel.PublishedAt().Equal(time.Date(2026, 10, 27, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("publishedAt = %s", rel.PublishedAt())
	}
	bin, ok := rel.Asset("nusallm-linux-amd64")
	if !ok || bin.URL() != "https://dl.test/amd64" || bin.Size() != 42 {
		t.Fatalf("binary asset = %+v ok=%v", bin, ok)
	}
	if _, ok := rel.Asset("SHA256SUMS"); !ok {
		t.Fatal("SHA256SUMS asset missing")
	}
	path, header := seen.get()
	if path != "/repos/jahrulnr/NusaLLM/releases/latest" {
		t.Fatalf("path = %s", path)
	}
	if header.Get("Accept") != "application/vnd.github+json" || header.Get("User-Agent") != "nusallm/0.0.0" {
		t.Fatalf("headers = %v", header)
	}
}

func TestLatestRejectsNonOK(t *testing.T) {
	srv, _ := newFake(t, http.StatusNotFound, `{"message":"Not Found"}`)
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm")
	_, err := g.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want the 404 named", err)
	}
}

func TestLatestRejectsNonVersionTag(t *testing.T) {
	srv, _ := newFake(t, http.StatusOK, `{"tag_name":"v0.10.4","html_url":"https://x.test/r"}`)
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm")
	if _, err := g.Latest(context.Background()); err == nil {
		t.Fatal("a v-prefixed tag was accepted")
	}
}

func TestLatestRejectsMalformedAsset(t *testing.T) {
	srv, _ := newFake(t, http.StatusOK, `{"tag_name":"0.1.0","html_url":"https://x.test/r",
		"assets":[{"name":"nusallm-linux-amd64","size":1,"browser_download_url":""}]}`)
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm")
	if _, err := g.Latest(context.Background()); err == nil {
		t.Fatal("an asset without a download url was accepted")
	}
}

func TestLatestRejectsBrokenJSON(t *testing.T) {
	srv, _ := newFake(t, http.StatusOK, `{"tag_name":`)
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm")
	if _, err := g.Latest(context.Background()); err == nil {
		t.Fatal("truncated JSON was accepted")
	}
}

func TestLatestHonorsContext(t *testing.T) {
	srv, _ := newFake(t, http.StatusOK, `{"tag_name":"0.1.0","html_url":"https://x.test/r"}`)
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Latest(ctx); err == nil {
		t.Fatal("a canceled context still produced a release")
	}
}

func TestFetchStreamsTheFile(t *testing.T) {
	payload := strings.Repeat("x", 300_000)
	srv, seen := newFake(t, http.StatusOK, payload)
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm/0.0.0")
	var got bytes.Buffer
	n, err := g.Fetch(context.Background(), srv.URL+"/dl/bin", &got, 1<<20)
	if err != nil {
		t.Fatalf("Fetch error: %v", err)
	}
	if n != int64(len(payload)) || got.String() != payload {
		t.Fatalf("fetched %d bytes, content mismatch", n)
	}
	if path, header := seen.get(); path != "/dl/bin" || header.Get("User-Agent") != "nusallm/0.0.0" {
		t.Fatalf("request path=%s headers=%v", path, header)
	}
}

func TestFetchRefusesOversizedFile(t *testing.T) {
	srv, _ := newFake(t, http.StatusOK, strings.Repeat("x", 1000))
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm")
	if _, err := g.Fetch(context.Background(), srv.URL, &bytes.Buffer{}, 999); err == nil {
		t.Fatal("a file over the limit was accepted")
	}
}

func TestFetchFailsBeforeWritingOnNonOK(t *testing.T) {
	srv, _ := newFake(t, http.StatusNotFound, "not here")
	g := NewGitHub(srv.Client(), srv.URL, "jahrulnr/NusaLLM", "nusallm")
	var got bytes.Buffer
	if _, err := g.Fetch(context.Background(), srv.URL, &got, 1<<10); err == nil {
		t.Fatal("404 download succeeded")
	}
	if got.Len() != 0 {
		t.Fatalf("bytes written from an error page: %q", got.String())
	}
}
