//go:build !windows

package admin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pythonFixture struct {
	path string
	err  error
}

func (p pythonFixture) Ensure(_ context.Context, diagnostics io.Writer) (string, error) {
	io.WriteString(diagnostics, "runtime diagnostic\n")
	return p.path, p.err
}

func TestPythonPreservesArgumentsStreamsAndExitCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "python fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nread input\nprintf 'arg=%s input=%s' \"$1\" \"$input\"\necho child-diagnostic >&2\nexit 7\n"), 0755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Python(t.Context(), []string{"space argument"}, strings.NewReader("stdin value\n"), &stdout, &stderr, pythonFixture{path: path})
	if code != 7 || stdout.String() != "arg=space argument input=stdin value" || !strings.Contains(stderr.String(), "child-diagnostic") || strings.Contains(stdout.String(), "runtime diagnostic") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := Python(t.Context(), nil, nil, io.Discard, &stderr, pythonFixture{err: errors.New("offline")}); code != 1 || !strings.Contains(stderr.String(), "offline") {
		t.Fatalf("failure=%d %q", code, stderr.String())
	}
}
