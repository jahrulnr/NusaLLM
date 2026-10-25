// Package repolog implements the Logger port as an append-only JSONL file
// with line-count rotation — the same segment pattern as the conversation
// store: a live active.jsonl plus numbered gzip files (0.jsonl.gz, 1.jsonl.gz,
// …). Single-process ownership; the mutex serializes writes.
package repolog

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/redact"
)

const activeFile = "active.jsonl"

// Options tunes rotation and scrubbing. MaxLines rotates active.jsonl once
// it holds that many records (0 = never rotate). MaxFiles caps sealed
// segments — the oldest are deleted past the cap (0 = unbounded). Redact
// transforms each entry before it is written — nil selects
// redact.Value, which masks credential-shaped keys and token patterns.
type Options struct {
	MaxLines int
	MaxFiles int
	Redact   func(kind string, entry any) any
}

// Logger is a JSONL-backed Logger store.
type Logger struct {
	dir      string
	maxLines int
	maxFiles int
	redact   func(string, any) any

	mu     sync.Mutex
	f      *os.File
	lines  int   // records in the open active file
	segs   []int // sealed segment numbers, ascending
	closed bool  // Close ran — no lazy reopen after an explicit close
}

// New opens the log dir, resuming the existing active file and segment
// numbering when present.
func New(dir string, opts Options) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &Logger{dir: dir, maxLines: opts.MaxLines, maxFiles: opts.MaxFiles}
	l.redact = opts.Redact
	if l.redact == nil {
		l.redact = func(_ string, e any) any { return redact.Value(e) }
	}
	if err := l.loadSegments(); err != nil {
		return nil, err
	}
	active := filepath.Join(dir, activeFile)
	lines, err := prepareActive(active)
	if err != nil {
		return nil, err
	}
	l.lines = lines
	f, err := os.OpenFile(filepath.Join(dir, activeFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l.f = f
	return l, nil
}

// loadSegments enumerates sealed <n>.jsonl.gz files in numeric order.
func (l *Logger) loadSegments() error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl.gz") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".jsonl.gz")
		if n, err := strconv.Atoi(name); err == nil && !e.IsDir() {
			l.segs = append(l.segs, n)
		}
	}
	sort.Ints(l.segs)
	return nil
}

// nextSeg returns the number the next sealed segment gets — max+1.
func (l *Logger) nextSeg() int {
	if len(l.segs) == 0 {
		return 0
	}
	return l.segs[len(l.segs)-1] + 1
}

// rotate seals active.jsonl as the next numbered segment, reopens a fresh
// active file, and prunes segments past maxFiles. The gzip is staged
// under a unique temp name and renamed into place, so a crash mid-write
// leaves an uncounted .tmp garbage file — never a valid-looking torn
// archive. On any failure the active handle is detached (reopen either
// reattaches or leaves l.f nil, which the next Append retries).
func (l *Logger) rotate() error {
	if err := l.f.Sync(); err != nil {
		return err
	}
	if err := l.f.Close(); err != nil {
		l.f = nil // closed-or-dying handle — never reuse it
		_ = l.reopen()
		return err
	}
	l.f = nil
	seg := l.nextSeg()
	activePath := filepath.Join(l.dir, activeFile)
	segPath := filepath.Join(l.dir, strconv.Itoa(seg)+".jsonl.gz")
	if err := sealSegment(segPath, activePath); err != nil {
		_ = l.reopen()
		return err
	}
	if err := os.Remove(activePath); err != nil {
		_ = os.Remove(segPath)
		_ = l.reopen()
		return err
	}
	if d, err := os.Open(l.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	l.segs = append(l.segs, seg)
	if l.maxFiles > 0 {
		for len(l.segs) > l.maxFiles {
			old := l.segs[0]
			if err := os.Remove(filepath.Join(l.dir, strconv.Itoa(old)+".jsonl.gz")); err != nil {
				_ = l.reopen()
				return apperror.NewInternalError("log rotate: prune segment", err)
			}
			l.segs = l.segs[1:]
		}
	}
	return l.reopen()
}

// sealSegment gzips activePath into a staged file and renames it to
// segPath — the rename is the commit point. A crash before it leaves a
// temp name loadSegments never counts; a crash after it leaves a
// complete archive plus the untouched active file (the next rotation
// re-seals those records — a tolerated duplicate window for diagnostics).
// The target must not already exist: a fresh rotation never reuses a
// name the segment list counts, and clobbering an uncounted one would
// silently replace a foreign file.
func sealSegment(segPath, activePath string) error {
	if _, err := os.Stat(segPath); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(segPath), filepath.Base(segPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	active, err := os.Open(activePath)
	if err != nil {
		tmp.Close()
		return err
	}
	zw := gzip.NewWriter(tmp)
	_, copyErr := io.Copy(zw, active)
	readErr := active.Close()
	zipErr := zw.Close()
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if err := errors.Join(copyErr, readErr, zipErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmpName, segPath)
}

// reopen attaches a fresh active file — also the recovery path after a
// failed rotate, where the renamed file may no longer exist. The record
// count is recomputed from the file, so a failed rotation that kept its
// records does not drift the threshold. On failure l.f is left nil —
// Append's lazy ensure retries the open once the environment heals.
func (l *Logger) reopen() error {
	path := filepath.Join(l.dir, activeFile)
	lines, err := prepareActive(path)
	if err != nil {
		l.f = nil
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		l.f = nil
		return err
	}
	l.f = f
	l.lines = lines
	return nil
}

// validLogLine matches conversation readLines: a complete jsonl row must
// decode as a log envelope with a non-empty kind tag.
func validLogLine(line []byte) bool {
	var probe struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal(bytes.TrimSpace(line), &probe) == nil && probe.Kind != ""
}

// prepareActive counts records in active.jsonl and truncates at the
// first torn or invalid line — keep is the byte offset where that line
// starts, so the corrupt record and everything after it are dropped and
// O_APPEND cannot glue the next write to partial bytes. ReadString keeps
// huge single-line records from hitting scanner caps. Missing or empty
// files yield 0.
func prepareActive(path string) (int, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() == 0 {
		return 0, nil
	}

	br := bufio.NewReader(f)
	var (
		lines int
		keep  int64 // byte length to retain
	)
	for {
		part, err := br.ReadString('\n')
		if err == nil {
			if !validLogLine([]byte(part[:len(part)-1])) {
				break // fail-stop: drop the bad line and its tail
			}
			keep += int64(len(part))
			lines++
			continue
		}
		if errors.Is(err, io.EOF) {
			break // partial tail without newline — keep excludes it
		}
		return 0, err
	}
	if keep < info.Size() {
		if err := f.Truncate(keep); err != nil {
			return 0, err
		}
		if err := f.Sync(); err != nil {
			return 0, err
		}
	}
	return lines, nil
}
