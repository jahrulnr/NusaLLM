// Package repolog implements the Logger port as an append-only JSONL file
// with line-count rotation — the same segment pattern as the conversation
// store: a live active.jsonl plus numbered sealed files (0.jsonl, 1.jsonl,
// …). Single-process ownership; the mutex serializes writes.
package repolog

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	"github.com/jahrulnr/llm-base/infrastructure/redact"
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

	mu    sync.Mutex
	f     *os.File
	lines int   // records in the open active file
	segs  []int // sealed segment numbers, ascending
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
	lines, err := countLines(filepath.Join(dir, activeFile))
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

// loadSegments enumerates sealed <n>.jsonl files in numeric order.
func (l *Logger) loadSegments() error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".jsonl")
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
// active file, and prunes segments past maxFiles.
func (l *Logger) rotate() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	seg := l.nextSeg()
	if err := os.Rename(filepath.Join(l.dir, activeFile),
		filepath.Join(l.dir, strconv.Itoa(seg)+".jsonl")); err != nil {
		return err
	}
	l.segs = append(l.segs, seg)
	if l.maxFiles > 0 {
		for len(l.segs) > l.maxFiles {
			old := l.segs[0]
			l.segs = l.segs[1:]
			if err := os.Remove(filepath.Join(l.dir, strconv.Itoa(old)+".jsonl")); err != nil {
				return apperror.NewInternalError("log rotate: prune segment", err)
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(l.dir, activeFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	l.lines = 0
	return nil
}

// countLines scans a file for newlines — 0 for a missing file.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024) // raw payloads can be large
	for sc.Scan() {
		n++
	}
	return n, sc.Err()
}
