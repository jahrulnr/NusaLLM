// Package runtime resolves host interpreters and provisions standalone runtimes.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

const pythonProbe = `import sys, json, ssl, sqlite3, venv, zipfile, tarfile, csv, urllib.request, subprocess, hashlib, pathlib, asyncio
if not (3, 11) <= sys.version_info < (4, 0):
    raise RuntimeError("Python >=3.11 and <4 is required")
ssl.create_default_context()
sqlite3.connect(":memory:").execute("select 1").fetchone()
print(json.dumps({"executable": sys.executable}))`

type candidate struct {
	path string
	args []string
}
type artifact struct{ url, sha256 string }

type Python struct {
	store      drepo.PythonRuntime
	client     *http.Client
	candidates []candidate
	artifact   func(context.Context) (artifact, error)
}

// NewPython receives the runtime root so its own PATH launchers are excluded
// from host detection. Probing them would recursively bootstrap Python.
func NewPython(store drepo.PythonRuntime, root string) dservice.PythonRuntime {
	var candidates []candidate
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if strings.EqualFold(filepath.Clean(abs), filepath.Clean(filepath.Join(root, "bin"))) {
			continue
		}
		names := []string{"python3", "python"}
		if runtime.GOOS == "windows" {
			names = []string{"python3.exe", "python.exe", "py.exe"}
		}
		for _, name := range names {
			path := filepath.Join(abs, name)
			if seen[path] {
				continue
			}
			seen[path] = true
			item := candidate{path: path}
			if name == "py.exe" {
				item.args = []string{"-3"}
			}
			candidates = append(candidates, item)
		}
	}
	return &Python{store: store, candidates: candidates, client: &http.Client{Timeout: 10 * time.Minute}, artifact: standaloneArtifact}
}

func probe(ctx context.Context, item candidate) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append(append([]string{}, item.args...), "-c", pythonProbe)
	cmd := exec.CommandContext(ctx, item.path, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", item.path, err, stderr.String())
	}
	var info struct {
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", fmt.Errorf("%s: invalid Python probe: %w", item.path, err)
	}
	if !filepath.IsAbs(info.Executable) {
		return "", fmt.Errorf("%s: Python probe returned no absolute interpreter path", item.path)
	}
	return info.Executable, nil
}

func (p *Python) Ensure(ctx context.Context, diagnostics io.Writer) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.store == nil {
		return "", fmt.Errorf("Python runtime repository is required")
	}
	for _, item := range p.candidates {
		if executable, err := probe(ctx, item); err == nil {
			return executable, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	cached, err := p.store.Cached()
	if err != nil {
		return "", err
	}
	if cached != "" {
		if executable, err := probe(ctx, candidate{path: cached}); err == nil {
			return executable, nil
		}
	}
	asset, err := p.artifact(ctx)
	if err != nil {
		return "", err
	}
	if diagnostics != nil {
		fmt.Fprintln(diagnostics, "nusallm: no suitable Python found; downloading standalone CPython from", asset.url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.url, nil)
	if err != nil {
		return "", err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Python download: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Python download: HTTP %d from %s", res.StatusCode, asset.url)
	}
	hash := sha256.New()
	reader := io.TeeReader(res.Body, hash)
	install, err := p.store.Stage(ctx, reader)
	if err != nil {
		return "", fmt.Errorf("Python extraction: %w", err)
	}
	defer install.Close()
	// tar may stop before the gzip trailer/HTTP body is drained. Hash the
	// entire compressed asset, including any bytes buffered past tar EOF.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != asset.sha256 {
		return "", fmt.Errorf("Python download checksum mismatch: got %s, want %s", got, asset.sha256)
	}
	executable, err := probe(ctx, candidate{path: install.Executable()})
	if err != nil {
		return "", fmt.Errorf("downloaded Python failed validation: %w", err)
	}
	// Another launcher may have finished the same bootstrap while we were
	// downloading. Reuse its immutable installation and discard our stage.
	if cached, err := p.store.Cached(); err != nil {
		return "", err
	} else if cached != "" {
		if existing, err := probe(ctx, candidate{path: cached}); err == nil {
			return existing, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := install.Commit(); err != nil {
		return "", err
	}
	if diagnostics != nil {
		fmt.Fprintln(diagnostics, "nusallm: Python ready at", executable)
	}
	return executable, nil
}
