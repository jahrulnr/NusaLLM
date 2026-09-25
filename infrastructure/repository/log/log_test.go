package repolog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func TestAppendWritesEnvelope(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := l.Append(t.Context(), "provider.response", map[string]any{"status": 200, "body": "raw"}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	lines := readLines(t, filepath.Join(dir, activeFile))
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	var rec struct {
		Ts   string         `json:"ts"`
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Ts == "" || rec.Kind != "provider.response" || rec.Data["status"] != float64(200) {
		t.Fatalf("record mismatch: %s", lines[0])
	}
}

func TestRotatePerLines(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{MaxLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	for i := 0; i < 5; i++ {
		if err := l.Append(t.Context(), "evt", i); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()

	// 5 records, cap 2 → segments 0.jsonl+1.jsonl (2 each), active has 1.
	if got := len(readLines(t, filepath.Join(dir, "0.jsonl"))); got != 2 {
		t.Fatalf("seg0 = %d", got)
	}
	if got := len(readLines(t, filepath.Join(dir, "1.jsonl"))); got != 2 {
		t.Fatalf("seg1 = %d", got)
	}
	if got := len(readLines(t, filepath.Join(dir, activeFile))); got != 1 {
		t.Fatalf("active = %d", got)
	}
}

func TestRotatePrunesMaxFiles(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{MaxLines: 1, MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	for i := 0; i < 4; i++ {
		if err := l.Append(t.Context(), "evt", i); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()

	// cap 2 → segments 0,1 pruned; 2,3 remain.
	if _, err := os.Stat(filepath.Join(dir, "0.jsonl")); !os.IsNotExist(err) {
		t.Fatal("seg0 should be pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "3.jsonl")); err != nil {
		t.Fatal("seg3 should exist")
	}
}

// Entries pass through the redactor before hitting disk — default is
// redact.Value, Options.Redact overrides.
func TestRedaction(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Append(t.Context(), "provider.response", map[string]any{"api_key": "sk-live-123456789", "ok": 1})
	l.Close()

	raw, _ := os.ReadFile(filepath.Join(dir, activeFile))
	if strings.Contains(string(raw), "sk-live-123456789") {
		t.Fatal("secret leaked into log")
	}
	if !strings.Contains(string(raw), "***") {
		t.Fatal("expected mask in log")
	}
}

func TestCustomRedactor(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{Redact: func(kind string, e any) any { return "fixed" }})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Append(t.Context(), "x", map[string]any{"a": 1})
	l.Close()

	raw, _ := os.ReadFile(filepath.Join(dir, activeFile))
	if !strings.Contains(string(raw), `"data":"fixed"`) {
		t.Fatalf("custom redactor not applied: %s", raw)
	}
}

func TestReopenResumes(t *testing.T) {
	dir := t.TempDir()
	l, _ := New(dir, Options{MaxLines: 3})
	l.Append(t.Context(), "a", 1)
	l.Append(t.Context(), "b", 2)
	l.Close()

	// Reopen: 2 existing lines + 1 append hits cap → rotate.
	l, err := New(dir, Options{MaxLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append(t.Context(), "c", 3); err != nil {
		t.Fatal(err)
	}
	l.Close()

	if got := len(readLines(t, filepath.Join(dir, "0.jsonl"))); got != 3 {
		t.Fatalf("seg0 = %d", got)
	}
	if got := len(readLines(t, filepath.Join(dir, activeFile))); got != 0 {
		t.Fatalf("active = %d", got)
	}
}
