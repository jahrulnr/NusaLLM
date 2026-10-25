// Package repoconversation implements the Conversation repository port on
// the local filesystem: one directory per conversation holding meta.json,
// numbered sealed archive segments (0.jsonl, 1.jsonl, …) written by Compact,
// and active.jsonl — the live tail. Messages and tool-output patches share
// the same append-only line stream so writes never rewrite history. The
// exceptions are the surgery operations — Compact seals a prefix,
// RewriteFrom truncates/promotes for an edit — which rewrite segment
// files atomically rather than appending.
package repoconversation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

const (
	metaFile   = "meta.json"
	activeFile = "active.jsonl"
	// The sealed-edit commit protocol stages the merged stream in
	// rewritePayload before any canonical name moves, and parks numbered
	// segments under rewriteParkSuffix until the new tail is published.
	rewritePayload    = activeFile + ".rewrite"
	rewriteParkSuffix = ".rewrite-old"
	lockFileName      = ".lock"
	lineMessage       = "message"
	lineToolOutput    = "tool_output"
)

// ConversationRepository is a JSONL-backed Conversation store. A single
// process owns the directory; the mutex serializes writes. The store
// additionally holds an advisory file lock for the span of every
// operation, so a second instance or process pointing at the same root
// serializes instead of losing meta updates or tearing staged files.
type ConversationRepository struct {
	path string
	mu   sync.Mutex

	lockFile *os.File // opened lazily under r.mu; nil until the first op
}

// NewConversationRepository returns the concrete store so a caller picks
// the port it needs: runtime collaborators take drepo.Conversation, while
// offline tooling may take drepo.ConversationRepairer — a capability the
// Conversation port deliberately does not expose.
func NewConversationRepository(path string) *ConversationRepository {
	return &ConversationRepository{path: path}
}

// lockStore takes the store-level exclusive lock for the span of one
// operation — the read-modify-write sequences (append + meta bump,
// rewrite + recount) are only atomic across instances when the whole
// sequence holds one lock, and a second writer must never interleave
// into a staged file.
func (r *ConversationRepository) lockStore() (func(), error) {
	// The handle opens lazily under r.mu and retries every call until it
	// succeeds — a transient open failure (EMFILE, a brief FS fault) must
	// not brick the store for the process's lifetime the way a once-
	// cached error would.
	if r.lockFile == nil {
		if err := os.MkdirAll(r.path, 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filepath.Join(r.path, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		r.lockFile = f
	}
	lockFile := r.lockFile
	if err := fileLock(lockFile); err != nil {
		// A failed lock can mean a stale fd (the file was replaced
		// externally) — drop the handle so the next op reopens instead
		// of retrying a handle that can never lock the live file.
		_ = lockFile.Close()
		r.lockFile = nil
		return nil, err
	}
	return func() { _ = fileUnlock(lockFile) }, nil
}

func (r *ConversationRepository) convDir(id string) string {
	return filepath.Join(r.path, id)
}

// validID is the boundary guard: the id becomes a path segment, so the
// domain's own rule (valueobject.ValidID) is re-asserted against this root.
func validID(id string) error {
	if err := valueobject.ValidID(id); err != nil {
		return apperror.New(400, "invalid id", fmt.Errorf("id %q: %w", id, err))
	}
	return nil
}

// writeFileAtomic is a var so tests can inject write failures at a
// named target deterministically (the old fixed <path>.tmp staging name
// once served that role).
var writeFileAtomic = writeFileAtomicImpl

// writeFileAtomicImpl writes via tmp+rename — a crash mid-write leaves the
// old file or nothing, never a torn target. fsync before and after the
// rename matters as much as the rename: without it a crash can resurrect
// the name with no contents (a sealed segment that reads empty is silent
// history loss, not an error). The dir sync is best-effort — the bytes
// are already durable once the file syncs. The staging name is unique
// per writer: a fixed <path>.tmp would let a second writer truncate the
// first's staged bytes mid-write, after which the loser's rename fails
// and the winner can publish a torn file — the same fix repogateway
// applied to its store.
func writeFileAtomicImpl(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// --- meta -----------------------------------------------------------------

func (r *ConversationRepository) readMeta(id string) (metaRecord, error) {
	m, err := r.readMetaUnvalidated(id)
	if err != nil {
		return metaRecord{}, err
	}
	dir := r.convDir(id)
	if err := validateSealedArchive(dir, m.SealedSegments); err != nil {
		return metaRecord{}, err
	}
	// The tail file is the only place live messages exist — metadata
	// claiming tail content over a missing active.jsonl means history
	// vanished behind the metadata's back. Surface it: hydrating an
	// empty room would let a turn continue over lost context, and an
	// append would resurrect a divergent tail.
	if m.ActiveCount > 0 {
		if _, err := os.Stat(filepath.Join(dir, activeFile)); errors.Is(err, os.ErrNotExist) {
			return metaRecord{}, fmt.Errorf("conversation %s: active.jsonl missing while metadata records %d tail messages; stop the app and restore from backup", id, m.ActiveCount)
		}
	}
	return m, nil
}

// readMetaUnvalidated is used by readMeta before its archive check and by
// recountMeta while segment surgery has intentionally changed the archive
// but not yet published the new counters.
func (r *ConversationRepository) readMetaUnvalidated(id string) (metaRecord, error) {
	raw, err := os.ReadFile(filepath.Join(r.convDir(id), metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return metaRecord{}, apperror.NewNotFoundError("conversation "+id, err)
	}
	if err != nil {
		return metaRecord{}, err
	}
	var m metaRecord
	if err := json.Unmarshal(raw, &m); err != nil {
		return metaRecord{}, fmt.Errorf("meta corrupt: %w", err)
	}
	if m.ID != id {
		return metaRecord{}, fmt.Errorf("meta corrupt: id %q does not match directory %q", m.ID, id)
	}
	if _, ok := enum.ParseEnvironment(m.Environment); !ok {
		return metaRecord{}, fmt.Errorf("meta corrupt: invalid environment %q", m.Environment)
	}
	return m, nil
}

func (r *ConversationRepository) writeMeta(id string, m metaRecord) error {
	m.UpdatedAt = time.Now()
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(r.convDir(id), metaFile), raw, 0o644)
}

// --- line stream ------------------------------------------------------------

// readLines slurps every line of a jsonl file. A truncated final line is
// tolerated — it is the signature of a crash mid-append; earlier corruption
// is reported.
func readLines(path string) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(lines) > 0 {
		last := lines[len(lines)-1]
		var probe struct{ Type string }
		if json.Unmarshal(last, &probe) != nil || probe.Type == "" {
			lines = lines[:len(lines)-1] // truncated tail — drop
		}
	}
	return lines, nil
}

func appendLine(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := dropTornTail(f); err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// dropTornTail cuts a damaged final record before an append, mirroring the
// tolerance readLines applies on the read side exactly. readLines drops its
// final line only when that line fails the decode/type probe, whether or
// not it carries a terminator; a complete record that lost its terminator
// still decodes, so the reader keeps it. This writer therefore drops a
// final line that fails the same probe, and for one that decodes with a
// type it repairs the missing terminator instead of dropping the record —
// keeping the reader's record and leaving the append on a fresh line. A
// damaged tail left in place would turn interior on the next append and
// every later read of the room would fail from then on. The bytes of a
// dropped line are unrecoverable: they are discarded at the line boundary
// rather than glued onto the next record.
//
// The probe reads the true final line, with no cap: a long decodable line
// is exactly the tail readLines keeps, so a bounded probe that skipped it
// would drop a record the reader still had. Finding the line's start is a
// backward chunk scan that reads those bytes anyway, so the append path
// still reads the previous record once — the clean path costs one stat, a
// one-byte read, and one scan chunk.
func dropTornTail(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	if size == 0 {
		return nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return err
	}
	// end is where the backward search stops: one past the last byte the
	// final line's content may occupy. A terminated final line drops with
	// its terminator, so the search starts before it.
	end := size
	terminated := last[0] == '\n'
	if terminated {
		end = size - 1
	}
	line, start, err := finalLine(f, end)
	if err != nil {
		return err
	}
	if !tornLine(line) {
		if terminated {
			return nil // the final record is intact — append without touching it
		}
		// The final record decodes but lost its terminator: repair it in
		// place so the append lands on a fresh line instead of gluing onto
		// it. readLines keeps this record; only its terminator is missing.
		if _, err := f.Write([]byte{'\n'}); err != nil {
			return err
		}
		return nil
	}
	if err := f.Truncate(start); err != nil {
		return err
	}
	return f.Sync()
}

// finalLine returns the content of the file's final line — the bytes after
// the last '\n' in [0, end) — and the offset that content starts at (0 when
// the file holds no earlier newline). It walks backward from end in chunks
// so the tail is read once however long the final line is; a line longer
// than one chunk is accumulated across iterations.
func finalLine(f *os.File, end int64) ([]byte, int64, error) {
	const chunk = 32 << 10
	buf := make([]byte, chunk)
	var tail []byte
	for e := end; e > 0; e -= chunk {
		start := e - chunk
		if start < 0 {
			start = 0
		}
		window := buf[:e-start]
		if _, err := f.ReadAt(window, start); err != nil {
			return nil, 0, err
		}
		if i := bytes.LastIndexByte(window, '\n'); i >= 0 {
			line := make([]byte, 0, len(window)-i-1+len(tail))
			line = append(line, window[i+1:]...)
			line = append(line, tail...)
			return line, start + int64(i) + 1, nil
		}
		tail = append(append([]byte(nil), window...), tail...)
	}
	return tail, 0, nil
}

// tornLine reports whether a final line is one readLines drops — it fails
// to decode as a line record or carries no type. A blank line is not torn:
// readLines skips blank lines rather than dropping a record.
func tornLine(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return false
	}
	var probe struct{ Type string }
	return json.Unmarshal(trimmed, &probe) != nil || probe.Type == ""
}

// messagesFromLines decodes message lines and applies tool_output patches
// globally by callID — a patch may target a call sealed in an earlier
// segment.
func messagesFromLines(lines [][]byte) ([]entity.Message, error) {
	var msgs []entity.Message
	callMessage := make(map[string]int)
	type patch struct {
		callID, output, latency string
		atts                    []valueobject.Attachment
	}
	var patches []patch
	for _, l := range lines {
		var lr lineRecord
		if err := json.Unmarshal(l, &lr); err != nil {
			return nil, fmt.Errorf("corrupt line: %w", err)
		}
		switch lr.Type {
		case lineMessage:
			m, err := decodeMessage(lr.Data)
			if err != nil {
				return nil, err
			}
			for _, call := range m.ToolCalls() {
				if _, exists := callMessage[call.ID()]; !exists {
					callMessage[call.ID()] = len(msgs)
				}
			}
			msgs = append(msgs, m)
		case lineToolOutput:
			var atts []valueobject.Attachment
			for _, a := range lr.Attachments {
				att, err := valueobject.NewAttachment(a.URI, a.MediaType)
				if err != nil {
					return nil, fmt.Errorf("tool_output attachment: %w", err)
				}
				att.SetName(a.Name)
				att.SetSHA256(a.SHA256)
				atts = append(atts, att)
			}
			patches = append(patches, patch{lr.CallID, lr.Output, lr.OutputLatency, atts})
		}
	}
	for _, p := range patches {
		i, exists := callMessage[p.callID]
		if !exists { // patches for calls discarded by an edit stay inert
			continue
		}
		if err := msgs[i].SetStoredToolOutput(p.callID, p.output, p.latency); err != nil {
			return nil, err // index was built from these messages
		}
		if p.atts != nil {
			if err := msgs[i].SetToolOutputAttachments(p.callID, p.atts); err != nil {
				return nil, err
			}
		}
	}
	return msgs, nil
}

// sealedSegments lists the conversation's numbered archive files in
// order — the eras Compact wrote, oldest first.
func sealedSegments(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var segs []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		if _, err := strconv.Atoi(name); err == nil { // numbered segment only
			segs = append(segs, f)
		}
	}
	sort.Slice(segs, func(i, j int) bool {
		ai, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(segs[i]), ".jsonl"))
		aj, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(segs[j]), ".jsonl"))
		return ai < aj
	})
	return segs, nil
}

// validateSealedArchive checks that the files and metadata describe the
// same eras before an operation rewrites or deletes any of them. A failed
// metadata write after Compact's active rewrite leaves an extra segment;
// continuing would overwrite it or discard patches for calls inside it.
func validateSealedArchive(dir string, committed int) error {
	segs, err := sealedSegments(dir)
	if err != nil {
		return err
	}
	id := filepath.Base(dir)
	if err := interruptedEdit(dir, id, segs, committed); err != nil {
		return err
	}
	return sealedArchiveMismatch(id, segs, committed)
}

// interruptedEdit reports whether the archive shows the sealed edit's
// crash window — a staged payload plus a layout only this protocol can
// leave behind. The edit only ever removes numbered files, so more
// segments than committed is never its remnant. While a payload exists
// the protocol's layout is narrow: it parks numbered segments in
// ascending order and never skips one, so the parked set is exactly the
// contiguous prefix 0.jsonl.rewrite-old … j.jsonl.rewrite-old, and every
// numbered segment still present is the suffix the parking left behind —
// an index greater than every parked index. A payload already equal to
// the live tail is the escape hatch: the publish landed, so the window is
// still edit-shaped however the leftovers are mixed, and republishing it
// is a no-op. Anything else — a Compact orphan, a missing segment, manual
// damage, a gapped or otherwise wrong-side parked set — gets the
// mismatch's own guidance rather than rolling a possibly stale payload
// forward.
func interruptedEdit(dir, id string, segs []string, committed int) error {
	if committed < 0 || len(segs) >= committed {
		return nil
	}
	payload, err := os.ReadFile(filepath.Join(dir, rewritePayload))
	if err != nil {
		return nil
	}
	// Publish already landed (payload == live tail): republishing is a
	// no-op, so the window stays edit-shaped whatever the leftover mix is.
	if active, err := os.ReadFile(filepath.Join(dir, activeFile)); err == nil && bytes.Equal(active, payload) {
		return interruptedEditErr(id)
	}
	parked, err := filepath.Glob(filepath.Join(dir, "*.jsonl"+rewriteParkSuffix))
	if err != nil || len(parked) == 0 {
		return nil
	}
	maxParked, ok := parkedPrefix(parked)
	if !ok {
		return nil
	}
	for _, seg := range segs {
		n, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(seg), ".jsonl"))
		if n <= maxParked {
			return nil
		}
	}
	return interruptedEditErr(id)
}

// parkedPrefix reports whether the parked segment names form the
// contiguous ascending prefix 0,1,…,j the edit protocol leaves — it parks
// numbered segments in order and never skips one — and returns the
// highest parked index. An unparseable name or a gap is not this
// protocol's remnant.
func parkedPrefix(parked []string) (maxIndex int, ok bool) {
	idx := make([]int, 0, len(parked))
	for _, p := range parked {
		n, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(p), ".jsonl"+rewriteParkSuffix))
		if err != nil {
			return 0, false
		}
		idx = append(idx, n)
	}
	sort.Ints(idx)
	for i, n := range idx {
		if n != i {
			return 0, false
		}
	}
	return len(idx) - 1, true
}

func interruptedEditErr(id string) error {
	return fmt.Errorf("conversation %s: interrupted edit in sealed history; stop the app and run `nusallm conversation repair %s --finish-rewrite` to roll it forward", id, id)
}

// sealedArchiveMismatch reports how the numbered segments disagree with
// the committed count — nil means files and metadata describe the same
// eras. The caller supplies the globbed segments so the same listing can
// feed surgery after the check.
func sealedArchiveMismatch(id string, segs []string, committed int) error {
	if committed < 0 {
		return fmt.Errorf("conversation %s: invalid metadata sealedSegments=%d; stop the app, inspect meta.json, and restore from backup", id, committed)
	}
	for i := 0; i < committed; i++ {
		if i >= len(segs) {
			return fmt.Errorf("conversation %s: sealed segment %d.jsonl missing; restore it from backup", id, i)
		}
		seg := segs[i]
		n, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(seg), ".jsonl"))
		if n != i {
			return fmt.Errorf("conversation %s: sealed segment %d.jsonl missing; restore it from backup", id, i)
		}
	}
	if len(segs) != committed {
		if len(segs) == committed+1 && filepath.Base(segs[committed]) == fmt.Sprintf("%d.jsonl", committed) {
			return fmt.Errorf("conversation %s: one uncommitted sealed segment; stop the app and see docs/persistence.md for offline repair; if neither action applies, restore from backup", id)
		}
		return fmt.Errorf("conversation %s: %d sealed segment files, metadata records %d; stop the app, inspect metadata and segments, and restore from backup", id, len(segs), committed)
	}
	return nil
}

// loadMessages reads the live tail, optionally prefixed by the first
// sealedCount archive segments in numeric order — the global logical
// history. The caller supplies the count (usually meta's) so surgery can
// recount against the files it just wrote rather than stale meta. Only
// committed segments qualify: Compact writes the file before bumping the
// count, so a crash mid-write leaves an orphan a glob would count —
// replaying a prefix the intact active copy still holds.
func (r *ConversationRepository) loadMessages(convID string, sealedCount int) ([]entity.Message, error) {
	dir := r.convDir(convID)
	var files []string
	if sealedCount > 0 {
		segs, err := sealedSegments(dir)
		if err != nil {
			return nil, err
		}
		present := make(map[int]struct{}, sealedCount)
		for _, f := range segs {
			n, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".jsonl"))
			if n < sealedCount { // committed segments only
				files = append(files, f)
				present[n] = struct{}{}
			}
		}
		for i := 0; i < sealedCount; i++ {
			if _, ok := present[i]; !ok {
				return nil, fmt.Errorf("conversation %s: sealed segment %d.jsonl missing (sealedSegments=%d)",
					convID, i, sealedCount)
			}
		}
	}
	files = append(files, filepath.Join(dir, activeFile))

	var lines [][]byte
	for _, f := range files {
		l, err := readLines(f)
		if err != nil {
			return nil, err
		}
		lines = append(lines, l...)
	}
	return messagesFromLines(lines)
}
