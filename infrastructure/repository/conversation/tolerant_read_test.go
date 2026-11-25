package repoconversation

// T-34 — one corrupt record must not make a whole conversation unreadable.
// Reads degrade the bad call (or skip the bad line) and keep the rest; writes
// and repair stay strict so a bad record is never laundered into the file.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

const corruptSeedPrefix = `{"type":"message","data":{"id":"m_1","role":"assistant","content":"",`
const corruptSeedSuffix = `,"createdAt":"2026-01-01T00:00:00Z"}}`

// seedRoom writes one message line by hand: the tool-call record is the
// payload under test, and the message around it is valid.
func seedRoom(t *testing.T, repo *ConversationRepository, dir string, toolCalls string) {
	t.Helper()
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	good := `{"type":"message","data":{"id":"m_0","role":"user","content":"hi","createdAt":"2026-01-01T00:00:00Z"}}`
	line := corruptSeedPrefix + `"toolCalls":[` + toolCalls + `]` + corruptSeedSuffix
	active := filepath.Join(dir, "c1", activeFile)
	if err := os.WriteFile(active, []byte(good+"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadRoom(t *testing.T, repo *ConversationRepository) []entity.Message {
	t.Helper()
	conv, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return conv.Messages()
}

func unreadableCall(t *testing.T, msgs []entity.Message) entity.ToolCall {
	t.Helper()
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			if tc.ID() == "call_bad" {
				return tc
			}
		}
	}
	t.Fatalf("call_bad missing from %d message(s)", len(msgs))
	return entity.ToolCall{}
}

// (a) A call record without its provenance decodes to an unreadable
// placeholder: the read still succeeds and the neighbouring messages survive.
func TestLoadToleratesCallWithoutType(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	seedRoom(t, repo, dir, `{"id":"call_bad","name":"exec","input":"{}","callAt":"2026-01-01T00:00:00Z"}`)

	msgs := loadRoom(t, repo)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want both records", len(msgs))
	}
	call := unreadableCall(t, msgs)
	if call.Type().String() != "" || call.Outcome() != "" {
		t.Fatalf("unreadable call = type %q outcome %q, want no provenance and no verdict", call.Type(), call.Outcome())
	}
	if call.Name() != "exec" || call.Input() != "{}" {
		t.Fatalf("surviving fields lost: %+v", call)
	}
}

// (b) A verdict string this version does not know is as unreadable as a
// missing type: degrade the call, keep the conversation.
func TestLoadToleratesUnknownOutcome(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	seedRoom(t, repo, dir, `{"id":"call_bad","name":"exec","input":"{}","type":"origin","outcome":"weird","callAt":"2026-01-01T00:00:00Z"}`)

	msgs := loadRoom(t, repo)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want both records", len(msgs))
	}
	if call := unreadableCall(t, msgs); call.Outcome() != "" {
		t.Fatalf("outcome = %q, want none", call.Outcome())
	}
}

// (c) A line that is not even a line record is skipped; the readable records
// on both sides still load.
func TestLoadSkipsUndecodableLine(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	good := `{"type":"message","data":{"id":"m_0","role":"user","content":"hi","createdAt":"2026-01-01T00:00:00Z"}}`
	active := filepath.Join(dir, "c1", activeFile)
	if err := os.WriteFile(active, []byte(good+"\nnot-a-record\n"+good+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs := loadRoom(t, repo)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want the two readable records", len(msgs))
	}
}

// (d) The same payload on a write-side decode stays an error: tolerance is a
// read rule, never a licence to store a broken record.
func TestStrictDecodeStillRejects(t *testing.T) {
	record := messageRecord{
		ID:   "m_1",
		Role: "assistant",
		ToolCalls: []toolCallRecord{{
			ID:    "call_bad",
			Name:  "exec",
			Input: "{}",
		}},
	}
	if _, err := decodeMessage(record, strictDecode); err == nil || !strings.Contains(err.Error(), "toolCall") {
		t.Fatalf("strict decode = %v, want a toolCall error", err)
	}
	line := []byte(`{"type":"message","data":{"id":"m_1","role":{"bad":1}}}`)
	if _, err := messagesFromLines([][]byte{line}, strictDecode); err == nil {
		t.Fatal("strict decode accepted an undecodable line")
	}
}

// The degraded call keeps the output it still has, and the message's other
// calls are untouched.
func TestLoadDegradesOnlyTheBrokenCall(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	seedRoom(t, repo, dir,
		`{"id":"call_ok","name":"exec","input":"{}","type":"origin","outputSet":true,"output":"fine","outcome":"ok","callAt":"2026-01-01T00:00:00Z"},`+
			`{"id":"call_bad","name":"read_file","input":"{}","outputSet":true,"output":"error: gone","callAt":"2026-01-01T00:00:00Z"}`)

	msgs := loadRoom(t, repo)
	var ok, bad entity.ToolCall
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			switch tc.ID() {
			case "call_ok":
				ok = tc
			case "call_bad":
				bad = tc
			}
		}
	}
	if ok.Outcome() != enum.ToolOutcomeOK || ok.Output() != "fine" {
		t.Fatalf("healthy call = %q/%q, want ok/fine", ok.Outcome(), ok.Output())
	}
	if bad.Output() != "error: gone" || bad.Outcome() != "" {
		t.Fatalf("broken call = %q/%q, want its text with no verdict", bad.Output(), bad.Outcome())
	}
}

// Guard: the tolerant path still returns records in order.
func TestLoadToleratesKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	seedRoom(t, repo, dir, `{"id":"call_bad","name":"exec","input":"{}"}`)
	var contents []string
	for _, m := range loadRoom(t, repo) {
		contents = append(contents, m.Content())
	}
	if !slices.Equal(contents, []string{"hi", ""}) {
		t.Fatalf("contents = %q, want the records in order", contents)
	}
}

// T-34b — the same rule one level up: a message record that cannot be built
// is skipped (never faked), and a record with one broken field keeps what a
// reader can still use.

func TestLoadSkipsMessageWithUnknownRole(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "c1", activeFile)
	lines := `{"type":"message","data":{"id":"m_ok","role":"user","content":"still readable","createdAt":"2026-01-02T03:04:05Z"}}` + "\n" +
		`{"type":"message","data":{"id":"m_bad","role":"wizard","content":"not a role","createdAt":"2026-01-02T03:04:06Z"}}` + "\n" +
		`{"type":"message","data":{"id":"m_ok2","role":"assistant","content":"also readable","createdAt":"2026-01-02T03:04:07Z"}}` + "\n"
	if err := os.WriteFile(active, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs := loadRoom(t, repo)
	var ids, contents []string
	for _, m := range msgs {
		ids = append(ids, m.ID())
		contents = append(contents, m.Content())
	}
	if !slices.Equal(ids, []string{"m_ok", "m_ok2"}) {
		t.Fatalf("ids = %q, want the readable records in order", ids)
	}
	if !slices.Equal(contents, []string{"still readable", "also readable"}) {
		t.Fatalf("contents = %q", contents)
	}
}

// A broken field is dropped, not fatal: the role and the text still render.
func TestLoadSalvagesMessageWithBrokenFields(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "c1", activeFile)
	line := `{"type":"message","data":{"id":"m_1","role":"assistant","content":"kept","userType":"nonsense","finishReason":"weird",` +
		`"createdAt":"not-a-date","attachments":[{"uri":"","mediaType":"image/png"}]}}`
	if err := os.WriteFile(active, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs := loadRoom(t, repo)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want the salvaged record", len(msgs))
	}
	m := msgs[0]
	if m.ID() != "m_1" || m.Content() != "kept" || m.Role() != enum.RoleAssistant {
		t.Fatalf("salvaged message = %+v", m)
	}
	if m.UserType() != "" || m.FinishReason() != "" || len(m.Attachments()) != 0 {
		t.Fatalf("broken fields survived: userType=%q finish=%q attachments=%d", m.UserType(), m.FinishReason(), len(m.Attachments()))
	}
	// A bad date is metadata: the message loads with no timestamp, not
	// dropped.
	if !m.CreatedAt().IsZero() {
		t.Fatalf("createdAt = %v, want zero", m.CreatedAt())
	}
}

// Repair rewrites the file, so it must not soften: the same records that read
// tolerantly still fail a repair pass.
func TestRepairStaysStrictOnUnreadableMessage(t *testing.T) {
	line := []byte(`{"type":"message","data":{"id":"m_bad","role":"wizard","content":"x","createdAt":"2026-01-02T03:04:06Z"}}`)
	if _, err := messagesFromLines([][]byte{line}, strictDecode); err == nil {
		t.Fatal("strict decode accepted an unknown role")
	}
}

// T-34c — the patch stream and unknown line types take the same rule, and
// every degradation says which case it was: a log that calls everything
// "tool call" sends the next reader hunting the wrong record.

func TestLoadToleratesBrokenPatchLine(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	seedRoom(t, repo, dir, `{"id":"call_bad","name":"exec","input":"{}","type":"origin","callAt":"2026-01-01T00:00:00Z"}`)
	active := filepath.Join(dir, "c1", activeFile)
	// A late result whose verdict is unreadable and whose attachment cannot
	// be built: the call keeps its healthy state, the read stays 200.
	patch := `{"type":"tool_output","callId":"call_bad","output":"late output","outputSet":true,"outcome":"weird","attachments":[{"uri":"","mediaType":"image/png"}]}`
	f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(patch + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	lines, err := readLines(active)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	msgs, err := messagesFromLines(lines, tolerantRead(func(id, reason string) {
		notes = append(notes, id+"|"+reason)
	}))
	if err != nil {
		t.Fatalf("tolerant read failed: %v", err)
	}
	var call entity.ToolCall
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			call = tc
		}
	}
	if call.ID() != "call_bad" || call.Output() != "late output" {
		t.Fatalf("call = %q/%q, want the readable text of the patch", call.ID(), call.Output())
	}
	if call.Outcome() != "" {
		t.Fatalf("outcome = %q, want none (unreadable verdict dropped)", call.Outcome())
	}
	if !hasNote(notes, "tool_output verdict dropped") || !hasNote(notes, "tool_output attachment") {
		t.Fatalf("diagnostics = %q, want a verdict and an attachment note", notes)
	}
}

func TestLoadReportsUnknownLineType(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "c1", activeFile)
	good := `{"type":"message","data":{"id":"m_0","role":"user","content":"hi","createdAt":"2026-01-01T00:00:00Z"}}`
	future := `{"type":"annotation","callId":"x"}`
	if err := os.WriteFile(active, []byte(good+"\n"+future+"\n"+good+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := readLines(active)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	msgs, err := messagesFromLines(lines, tolerantRead(func(id, reason string) {
		notes = append(notes, id+"|"+reason)
	}))
	if err != nil {
		t.Fatalf("tolerant read failed: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want the two readable records", len(msgs))
	}
	if !hasNote(notes, `line type "annotation" ignored`) {
		t.Fatalf("diagnostics = %q, want the unknown line type reported", notes)
	}
}

// Each degradation names its own case, so one log line cannot be mistaken for
// another.
func TestDiagnosticsNameTheirCase(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	seedRoom(t, repo, dir, `{"id":"call_bad","name":"exec","input":"{}"}`)
	active := filepath.Join(dir, "c1", activeFile)
	f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	// The undecodable line sits in the middle: a torn *final* line is dropped
	// by the reader before it ever reaches the decoder.
	extra := `{"type":"message","data":{"id":"m_skip","role":"wizard","content":"x","createdAt":"2026-01-01T00:00:00Z"}}` + "\n" +
		`{"type":"message","data":{"id":"m_drop","role":"assistant","content":"kept","userType":"nonsense","createdAt":"2026-01-01T00:00:00Z"}}` + "\n" +
		`{"type":"annotation"}` + "\n" +
		`not-a-record` + "\n" +
		`{"type":"message","data":{"id":"m_tail","role":"user","content":"tail","createdAt":"2026-01-01T00:00:00Z"}}` + "\n"
	if _, err := f.WriteString(extra); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	lines, err := readLines(active)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	if _, err := messagesFromLines(lines, tolerantRead(func(id, reason string) {
		notes = append(notes, id+"|"+reason)
	})); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tool call unreadable:",
		"field dropped: userType",
		"message skipped:",
		`line type "annotation" ignored`,
		"line skipped:",
	} {
		if !hasNote(notes, want) {
			t.Fatalf("diagnostics %q miss %q", notes, want)
		}
	}
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// Repair still refuses the shapes a read now tolerates — it rewrites the
// file, so a softened repair would launder a bad record into the archive.
func TestRepairStaysStrictOnBrokenPatchAndUnknownType(t *testing.T) {
	owning := `{"type":"message","data":{"id":"m_1","role":"assistant","content":"","toolCalls":[{"id":"call_x","name":"exec","input":"{}","type":"origin","callAt":"2026-01-01T00:00:00Z"}],"createdAt":"2026-01-01T00:00:00Z"}}`
	cases := map[string][][]byte{
		// A patch only parses against the call it belongs to.
		"broken patch": {[]byte(owning), []byte(`{"type":"tool_output","callId":"call_x","outputSet":true,"outcome":"weird"}`)},
		"unknown line": {[]byte(`{"type":"annotation"}`)},
	}
	for label, lines := range cases {
		if _, err := messagesFromLines(lines, strictDecode); err == nil {
			t.Fatalf("%s: strict decode accepted the record", label)
		}
	}
}
