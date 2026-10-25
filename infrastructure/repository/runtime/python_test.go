package reporuntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func archive(t *testing.T, name, body string) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tarw := tar.NewWriter(gz)
	if err := tarw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body))}); err != nil {
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
	return data.Bytes()
}

func TestStagedPythonOnlyPublishedOnCommit(t *testing.T) {
	store := NewLocal(t.TempDir())
	if current, err := store.Cached(); err != nil || current != "" {
		t.Fatalf("empty cache=%q %v", current, err)
	}
	install, err := store.Stage(t.Context(), bytes.NewReader(archive(t, filepath.ToSlash(pythonExecutable("")), "interpreter")))
	if err != nil {
		t.Fatal(err)
	}
	defer install.Close()
	if current, _ := store.Cached(); current != "" {
		t.Fatal("staging exposed unvalidated interpreter")
	}
	if err := install.Commit(); err != nil {
		t.Fatal(err)
	}
	current, err := store.Cached()
	if err != nil || current != install.Executable() {
		t.Fatalf("cache=%q %v", current, err)
	}
	if err := install.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatal("committed installation was discarded")
	}
}

func TestDiscardMalformedAndCanceledInstallations(t *testing.T) {
	root := t.TempDir()
	store := NewLocal(root)
	install, err := store.Stage(t.Context(), bytes.NewReader(archive(t, filepath.ToSlash(pythonExecutable("")), "interpreter")))
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(install.Executable()); !os.IsNotExist(err) {
		t.Fatalf("staging leaked: %v", err)
	}
	if _, err := store.Stage(t.Context(), bytes.NewReader([]byte("not gzip"))); err == nil {
		t.Fatal("malformed archive accepted")
	}
	if _, err := store.Stage(t.Context(), bytes.NewReader(archive(t, "../outside", "bad"))); err == nil {
		t.Fatal("archive escaped installation root")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Stage(ctx, bytes.NewReader(archive(t, "python/bin/python3", "unused"))); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed staging left files: %v %v", entries, err)
	}
}

func TestPreparePythonLaunchers(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(t.TempDir(), "application with ' quote")
	if err := PrepareLaunchers(root, executable); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python", "python3"} {
		want := []byte(`python "$@"`)
		if runtime.GOOS == "windows" {
			name += ".cmd"
			want = []byte(`python %*`)
		}
		raw, err := os.ReadFile(filepath.Join(root, "bin", name))
		if err != nil || !bytes.Contains(raw, want) {
			t.Fatalf("launcher=%q %v", raw, err)
		}
	}
}
