package repolog

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(zr); err != nil {
			t.Fatal(err)
		}
		raw = buf.Bytes()
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

func TestAppendRejectsEmptyKindWithoutTruncatingLaterRecords(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(t.Context(), "first", "one"); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(t.Context(), "", "invalid"); err == nil {
		t.Fatal("empty kind must be rejected before append")
	}
	if err := l.Append(t.Context(), "last", "two"); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l, err = New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	lines := readLines(t, filepath.Join(dir, activeFile))
	if len(lines) != 2 || !strings.Contains(lines[1], `"kind":"last"`) {
		t.Fatalf("restart lost later record: %v", lines)
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

	// 5 records, cap 2 → two compressed segments (2 each), active has 1.
	if got := len(readLines(t, filepath.Join(dir, "0.jsonl.gz"))); got != 2 {
		t.Fatalf("seg0 = %d", got)
	}
	if got := len(readLines(t, filepath.Join(dir, "1.jsonl.gz"))); got != 2 {
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
	if _, err := os.Stat(filepath.Join(dir, "0.jsonl.gz")); !os.IsNotExist(err) {
		t.Fatal("seg0 should be pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "3.jsonl.gz")); err != nil {
		t.Fatal("seg3 should exist")
	}
}

func TestRotateKeepsTenFilesIncludingActive(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{MaxLines: 1, MaxFiles: 9})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := l.Append(t.Context(), "evt", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 {
		t.Fatalf("files = %d, want 9 archives and active", len(entries))
	}
	for i := 0; i < 12; i++ {
		path := filepath.Join(dir, strconv.Itoa(i)+".jsonl.gz")
		if i < 3 {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("archive %d should be pruned: %v", i, err)
			}
			continue
		}
		if got := len(readLines(t, path)); got != 1 {
			t.Fatalf("archive %d lines = %d", i, got)
		}
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

	if got := len(readLines(t, filepath.Join(dir, "0.jsonl.gz"))); got != 3 {
		t.Fatalf("seg0 = %d", got)
	}
	if got := len(readLines(t, filepath.Join(dir, activeFile))); got != 0 {
		t.Fatalf("active = %d", got)
	}
}

// RawMessage/struct entries must round-trip through JSON before scrubbing —
// otherwise upstream bodies quoting auth headers hit disk verbatim.
func TestRawMessageScrubbed(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	payload := json.RawMessage(`{"error":{"message":"bad key","key":"sk-live-abc123def456"}}`)
	if err := l.Append(t.Context(), "provider.error", payload); err != nil {
		t.Fatal(err)
	}
	l.Close()

	raw, _ := os.ReadFile(filepath.Join(dir, "active.jsonl"))
	if strings.Contains(string(raw), "sk-live-abc123def456") {
		t.Fatalf("credential leaked through RawMessage: %s", raw)
	}
	if !strings.Contains(string(raw), "bad key") {
		t.Fatal("benign content lost by scrub")
	}
}

// A record bigger than the old 8MB scanner cap must not brick reopen.
func TestResumeTruncatesTornTail(t *testing.T) {
	dir := t.TempDir()
	good := `{"ts":"2024-01-01T00:00:00Z","kind":"evt","data":1}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, activeFile), []byte(good+`{"ts":"2024`), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := l.Append(t.Context(), "evt", 2); err != nil {
		t.Fatal(err)
	}
	l.Close()

	raw, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), good) {
		t.Fatal("resumed good line missing")
	}
	lines := readLines(t, filepath.Join(dir, activeFile))
	if len(lines) != 2 {
		t.Fatalf("active lines = %d, want 2", len(lines))
	}
	for _, line := range lines {
		var rec struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Kind != "evt" {
			t.Fatalf("invalid line after torn resume: %q", line)
		}
	}
}

// A corrupt newline-terminated line mid-file must drop the whole tail —
// the valid records after it are not resurrected, and the next append
// must not glue onto the dead line's bytes.
func TestResumeDropsCorruptTail(t *testing.T) {
	dir := t.TempDir()
	good := `{"ts":"2024-01-01T00:00:00Z","kind":"evt","data":1}` + "\n"
	bad := `not-json` + "\n"
	tail := `{"ts":"2024-01-01T00:00:01Z","kind":"evt","data":2}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, activeFile), []byte(good+bad+tail), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.lines != 1 {
		t.Fatalf("resumed lines = %d, want 1 (valid prefix only)", l.lines)
	}
	if err := l.Append(t.Context(), "evt", 3); err != nil {
		t.Fatal(err)
	}
	l.Close()

	lines := readLines(t, filepath.Join(dir, activeFile))
	if len(lines) != 2 {
		t.Fatalf("active lines = %d, want 2 (valid prefix + append)", len(lines))
	}
	for _, line := range lines {
		var rec struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Kind != "evt" {
			t.Fatalf("invalid line after corrupt-tail resume: %q", line)
		}
	}
}

func TestResumeDropsInvalidFinalLine(t *testing.T) {
	dir := t.TempDir()
	good := `{"ts":"2024-01-01T00:00:00Z","kind":"evt","data":1}` + "\n"
	bad := `not-json` + "\n"
	if err := os.WriteFile(filepath.Join(dir, activeFile), []byte(good+bad), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := New(dir, Options{MaxLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	if err := l.Append(t.Context(), "evt", 2); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(t.Context(), "evt", 3); err != nil {
		t.Fatal(err)
	}
	l.Close()

	if got := len(readLines(t, filepath.Join(dir, "0.jsonl.gz"))); got != 3 {
		t.Fatalf("seg0 = %d, want 3 (1 resumed + 2 appended)", got)
	}
}

func TestHugeRecordReopens(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{MaxLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 10<<20) // 10MB single line
	if err := l.Append(t.Context(), "big", big); err != nil {
		t.Fatal(err)
	}
	l.Close()
	l2, err := New(dir, Options{MaxLines: 2})
	if err != nil {
		t.Fatalf("reopen after huge record: %v", err)
	}
	l2.Close()
}

// A failed rotate() leaves l.f pointing at the file it just closed, but
// the logger is never permanently dead — the next Append retries reopen,
// so a transient unwritable window heals without a restart.
func TestAppendRecoversAfterFailedReopen(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{MaxLines: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Force the reopen path to fail: remove active.jsonl and make the dir
	// read-only. The first Append still "writes" — into the unlinked
	// inode the open O_APPEND handle still points at — then rotate fails
	// to create the segment and fails to recreate active.
	if err := os.Remove(filepath.Join(dir, activeFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err = l.Append(t.Context(), "evt", 1)
	if err == nil {
		t.Fatal("expected rotate/reopen failure")
	}

	// Heal the environment — a dead logger keeps reporting a closed
	// handle; a live one retries reopen and recovers.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err = l.Append(t.Context(), "evt", 2)
	if err != nil {
		t.Fatalf("logger stayed dead after environment healed: %v", err)
	}
	l.Close()
}

// When rotate() fails, Append has already written and synced the record
// — the call returns an error yet the record persisted, breaking the
// usual "error means not persisted" contract. reopen() recounts the
// file, so the rotation threshold does not drift while the records are
// still there.
func TestFailedRotateKeepsPersistedRecord(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{MaxLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Block the next segment number after New() counted segments, so
	// rotate fails at O_EXCL while reopen() succeeds.
	blocker := filepath.Join(dir, "0.jsonl.gz")
	if err := os.WriteFile(blocker, []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := l.Append(t.Context(), "evt", "first"); err == nil {
		t.Fatal("expected rotate error")
	}
	// The record landed anyway.
	got := readLines(t, filepath.Join(dir, activeFile))
	if len(got) != 1 || !strings.Contains(got[0], "first") {
		t.Fatalf("record lost despite error path: %v", got)
	}
	if l.lines != 1 {
		t.Fatalf("l.lines = %d after failed rotate, want 1 (recounted)", l.lines)
	}
	if err := l.Append(t.Context(), "evt", "second"); err == nil {
		t.Fatal("expected rotate error again")
	}
	got = readLines(t, filepath.Join(dir, activeFile))
	if len(got) != 2 {
		t.Fatalf("active should hold 2 records during the failure window: %v", got)
	}
}

// Append after Close returns an error rather than panicking.
func TestAppendAfterClose(t *testing.T) {
	l, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	err = l.Append(t.Context(), "evt", 1)
	if err == nil {
		t.Fatal("append after close should error")
	}
	t.Logf("append after close error: %v", err)
}

// A crash between "segment gzip written" and "active.jsonl removed"
// (the window inside rotate()) restarts with both copies on disk: the
// sealed archive AND the still-live active. The next rotation seals the
// same records a second time. That duplication is the chosen failure
// mode — copy-then-delete guarantees no record is lost at the boundary,
// and forensic archives tolerate a repeated line; the alternative order
// (delete-then-seal) would lose the records outright.
func TestRotationCrashWindowDuplicates(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, Options{MaxLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(t.Context(), "evt", 1); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(t.Context(), "evt", 2); err != nil {
		t.Fatal(err)
	}
	l.Close()

	// Replay the crash window by hand: a complete sealed segment exists
	// AND active.jsonl was never removed.
	activePath := filepath.Join(dir, activeFile)
	raw, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	seg0, err := os.Create(filepath.Join(dir, "0.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(seg0)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := seg0.Close(); err != nil {
		t.Fatal(err)
	}
	// active.jsonl deliberately left in place.

	l, err = New(dir, Options{MaxLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append(t.Context(), "evt", 3); err != nil {
		t.Fatal(err)
	}
	l.Close()

	seg0Lines := readLines(t, filepath.Join(dir, "0.jsonl.gz"))
	seg1Lines := readLines(t, filepath.Join(dir, "1.jsonl.gz"))
	// seg0 holds records 1,2 (the pre-crash seal); the resume re-seals
	// them together with record 3 into seg1 — duplication across archives.
	if len(seg0Lines) != 2 || len(seg1Lines) != 3 {
		t.Fatalf("segments: seg0=%d seg1=%d — records re-sealed", len(seg0Lines), len(seg1Lines))
	}
}
