//go:build !windows

package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	reporuntime "github.com/jahrulnr/nusallm/infrastructure/repository/runtime"
)

const fakeInterpreter = "#!/bin/sh\nprintf '{\"executable\":\"%s\"}\\n' \"$0\"\n"

func writeInterpreter(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func pythonArchive(t *testing.T, body string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tarw := tar.NewWriter(gz)
	if err := tarw.WriteHeader(&tar.Header{Name: "python/bin/python3", Mode: 0755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tarw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestHostPythonReusedWithoutDownload(t *testing.T) {
	root := t.TempDir()
	p := NewPython(reporuntime.NewLocal(root), root).(*Python)
	valid := writeInterpreter(t, fakeInterpreter)
	p.candidates = []candidate{{path: writeInterpreter(t, "#!/bin/sh\necho 'old or incomplete python' >&2\nexit 1\n")}, {path: valid}}
	p.artifact = func(context.Context) (artifact, error) {
		t.Fatal("download attempted with suitable host Python")
		return artifact{}, nil
	}
	got, err := p.Ensure(t.Context(), io.Discard)
	if err != nil || got != valid {
		t.Fatalf("ensure=%q %v", got, err)
	}
	if current, _ := p.store.Cached(); current != "" {
		t.Fatal("host reuse created a managed installation")
	}
}

func TestDownloadValidatePublishAndOfflineCacheReuse(t *testing.T) {
	raw := pythonArchive(t, fakeInterpreter)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(raw) }))
	defer server.Close()
	root := t.TempDir()
	p := NewPython(reporuntime.NewLocal(root), root).(*Python)
	p.candidates = nil
	p.artifact = func(context.Context) (artifact, error) {
		return artifact{url: server.URL, sha256: fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
	}
	var diagnostics bytes.Buffer
	got, err := p.Ensure(t.Context(), &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || !strings.Contains(diagnostics.String(), "Python ready") {
		t.Fatalf("requests=%d diagnostics=%q", requests.Load(), diagnostics.String())
	}
	server.Close()
	for n := 0; n < 2; n++ {
		cached, err := p.Ensure(t.Context(), io.Discard)
		if err != nil || cached != got {
			t.Fatalf("offline reuse=%q %v", cached, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("cached runtime downloaded again: %d", requests.Load())
	}
}

func TestFailedDownloadNeverPublishesRuntime(t *testing.T) {
	for _, failure := range []string{"http", "archive", "checksum", "interpreter"} {
		t.Run(failure, func(t *testing.T) {
			raw := pythonArchive(t, fakeInterpreter)
			if failure == "archive" {
				raw = []byte("broken gzip")
			}
			if failure == "interpreter" {
				raw = pythonArchive(t, "#!/bin/sh\nexit 1\n")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failure == "http" {
					w.WriteHeader(503)
					return
				}
				w.Write(raw)
			}))
			defer server.Close()
			root := t.TempDir()
			p := NewPython(reporuntime.NewLocal(root), root).(*Python)
			p.candidates = nil
			digest := fmt.Sprintf("%x", sha256.Sum256(raw))
			if failure == "checksum" {
				digest = "wrong"
			}
			p.artifact = func(context.Context) (artifact, error) { return artifact{url: server.URL, sha256: digest}, nil }
			if _, err := p.Ensure(t.Context(), io.Discard); err == nil {
				t.Fatal("broken download succeeded")
			}
			if current, err := p.store.Cached(); err != nil || current != "" {
				t.Fatalf("failed runtime published: %q %v", current, err)
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatalf("failed staging leaked: %v %v", files, err)
			}
		})
	}
}

func TestLauncherExcludedAndCancellationStopsBootstrap(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", filepath.Join(root, "bin"))
	p := NewPython(reporuntime.NewLocal(root), root).(*Python)
	if len(p.candidates) != 0 {
		t.Fatal("launcher would recursively probe itself")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Ensure(ctx, io.Discard); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
}
