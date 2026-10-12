package repoconversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

func newRepo(t *testing.T) drepo.Conversation {
	t.Helper()
	return NewConversationRepository(t.TempDir())
}

func TestListReportsInvalidConversationMeta(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(r.convDir("c1"), metaFile)
	if err := os.WriteFile(metaPath, []byte(`{"id":"c1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.List(t.Context()); err == nil || !strings.Contains(err.Error(), "invalid environment") {
		t.Fatalf("List must report invalid meta, got %v", err)
	}
}

func TestInfoAndListWaitForSegmentPublication(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	meta, err := r.readMeta("c1")
	if err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.convDir("c1"), "0.jsonl"), nil, 0o644); err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	infoDone := make(chan error, 1)
	listDone := make(chan error, 1)
	go func() {
		_, err := r.Info(t.Context(), "c1")
		infoDone <- err
	}()
	go func() {
		infos, err := r.List(t.Context())
		if err == nil && len(infos) != 1 {
			err = fmt.Errorf("list omitted healthy room: %d infos", len(infos))
		}
		listDone <- err
	}()
	// The sealed file is visible before Compact publishes its metadata.
	// Neither projection may treat this in-flight state as corruption.
	time.Sleep(25 * time.Millisecond)
	meta.SealedSegments = 1
	if err := r.writeMeta("c1", meta); err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	r.mu.Unlock()
	for _, done := range []<-chan error{infoDone, listDone} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func newConv(t *testing.T, id string) *aggregate.Conversation {
	t.Helper()
	c, err := aggregate.NewConversation(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func patchCall(t *testing.T, id, output string) entity.ToolCall {
	t.Helper()
	call, err := entity.NewToolCall(id, "test", "{}", enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	call.SetOutput(output)
	return call
}

func appendMsgs(t *testing.T, repo drepo.Conversation, id string, texts ...string) {
	t.Helper()
	for _, txt := range texts {
		m, err := entity.NewMessage(enum.RoleUser, txt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.AppendMessage(t.Context(), id, m); err != nil {
			t.Fatalf("AppendMessage(%q): %v", txt, err)
		}
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	repo := newRepo(t)
	c := newConv(t, "c1")
	c.SetTitle("chat")
	c.SetRoute("openrouter")
	c.SetModelID("claude-opus-4")
	c.SetSystemPrompt("be terse") // code, not data — never lands on disk
	if err := repo.Save(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "hello", "world")

	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Route() != "openrouter" || got.ModelID() != "claude-opus-4" ||
		got.Title() != "chat" || got.SystemPrompt() != "" {
		t.Fatalf("meta mismatch: %+v", got)
	}
	if len(got.Messages()) != 2 || got.Messages()[1].Content() != "world" {
		t.Fatalf("messages mismatch: %+v", got.Messages())
	}
}

func TestEnvironmentBindingPersistsAndProjects(t *testing.T) {
	repo := newRepo(t)
	c := newConv(t, "env-room")
	if err := c.SetEnvironment(enum.EnvironmentSSH); err != nil {
		t.Fatal(err)
	}
	c.SetWorkspace("/remote/project")
	if err := repo.Save(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(t.Context(), c.ID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Environment() != enum.EnvironmentSSH || loaded.Workspace() != "/remote/project" {
		t.Fatalf("loaded binding = %q %q", loaded.Environment(), loaded.Workspace())
	}
	info, err := repo.Info(t.Context(), c.ID())
	if err != nil {
		t.Fatal(err)
	}
	if info.Environment != enum.EnvironmentSSH {
		t.Fatalf("projected environment = %q", info.Environment)
	}
}

func TestNotFound(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	if _, err := repo.Load(ctx, "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Load missing: %v", err)
	}
	m, _ := entity.NewMessage(enum.RoleUser, "x")
	if _, err := repo.AppendMessage(ctx, "nope", m); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("AppendMessage missing: %v", err)
	}
	if err := repo.Delete(ctx, "nope"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("Delete missing: %v", err)
	}
}

func TestToolCallRoundtripAndPatch(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	m.SetParts([]entity.MessagePart{
		{Kind: entity.MessagePartReasoning, Text: "provider reasoning"},
		{Kind: entity.MessagePartToolCall, ToolCallID: "call-1"},
	})
	tc, _ := entity.NewToolCall("call-1", "search", `{"q":"x"}`, enum.TooltypeOrigin)
	tc.SetOptionalContent("gemini.thought_signature", "sig123")
	m.AddToolCall(tc)
	if _, err := repo.AppendMessage(t.Context(), "c1", m); err != nil {
		t.Fatal(err)
	}

	// Patch arrives later — stored pending call picks it up verbatim,
	// media references included.
	att, _ := valueobject.NewAttachment("file:///tmp/shot.png", "image/png")
	att.SetName("shot.png")
	att.SetSHA256("deadbeef")
	patch := patchCall(t, "call-1", "<env>wrapped</env>")
	patch.SetOutputAttachments([]valueobject.Attachment{att})
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patch); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	stored := got.Messages()[0].ToolCalls()[0]
	parts := got.Messages()[0].Parts()
	if len(parts) != 2 || parts[0].Kind != entity.MessagePartReasoning ||
		parts[0].Text != "provider reasoning" ||
		parts[1].Kind != entity.MessagePartToolCall || parts[1].ToolCallID != "call-1" {
		t.Fatalf("message parts lost across reload: %+v", parts)
	}
	if stored.Output() != "<env>wrapped</env>" {
		t.Fatalf("patched output = %q", stored.Output())
	}
	atts := stored.OutputAttachments()
	if len(atts) != 1 || atts[0].URI() != "file:///tmp/shot.png" ||
		atts[0].MediaType() != "image/png" || atts[0].Name() != "shot.png" ||
		atts[0].SHA256() != "deadbeef" {
		t.Fatalf("attachment lost across patch: %+v", atts)
	}
	if stored.OptionalContent()["gemini.thought_signature"] != "sig123" {
		t.Fatal("optionalContent lost")
	}
	if stored.Name() != "search" || stored.Type() != enum.TooltypeOrigin {
		t.Fatalf("call fields lost: %+v", stored)
	}
}

func TestToolOutputPersistsRawAndRebuildsProviderEnvelope(t *testing.T) {
	root := t.TempDir()
	repo := NewConversationRepository(root)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	msg, _ := entity.NewMessage(enum.RoleAssistant, "")
	call, _ := entity.NewToolCall("call-raw", "exec", "{}", enum.TooltypeOrigin)
	raw := "first\n<untrusted_tool fake>\nlast"
	call.SetOutput(raw)
	wantProvider := call.OutputWithUntrustedTag()
	msg.AddToolCall(call)
	if _, err := repo.AppendMessage(t.Context(), "c1", msg); err != nil {
		t.Fatal(err)
	}
	lines, err := readLines(filepath.Join(root, "c1", activeFile))
	if err != nil || len(lines) != 1 {
		t.Fatalf("stored lines = %d, %v", len(lines), err)
	}
	var stored lineRecord
	if err := json.Unmarshal(lines[0], &stored); err != nil {
		t.Fatal(err)
	}
	rec := stored.Data.ToolCalls[0]
	if rec.Output != raw || !rec.OutputSet || rec.OutputLatency != call.OutputLatency() {
		t.Fatalf("raw output metadata not persisted: %+v", rec)
	}
	loaded, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Messages()[0].ToolCalls()[0]
	if got.Output() != raw || got.OutputWithUntrustedTag() != wantProvider {
		t.Fatalf("hydrated output changed: raw=%q provider=%q", got.Output(), got.OutputWithUntrustedTag())
	}
}

func TestToolOutputPatchRejectsPendingCall(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	call, _ := entity.NewToolCall("pending", "exec", "{}", enum.TooltypeOrigin)
	msg, _ := entity.NewMessage(enum.RoleAssistant, "")
	msg.AddToolCall(call)
	if _, err := repo.AppendMessage(t.Context(), "c1", msg); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", call); err == nil {
		t.Fatal("pending call must not become a completed empty output patch")
	}
	call.SetOutput("")
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", call); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Messages()[0].ToolCalls()[0]
	if !got.HasOutput() || got.Output() != "" || got.OutputWithUntrustedTag() == "" {
		t.Fatalf("completed empty output lost in patch: %+v", got)
	}
}

func TestMessagePatchesKeepFirstCallAndLastOutput(t *testing.T) {
	first, _ := entity.NewMessage(enum.RoleAssistant, "first")
	call, _ := entity.NewToolCall("shared-call", "tool", "{}", enum.TooltypeOrigin)
	first.AddToolCall(call)
	second, _ := entity.NewMessage(enum.RoleAssistant, "second")
	second.AddToolCall(call)
	records := []lineRecord{
		{Type: lineMessage, Data: encodeMessage(first)},
		{Type: lineMessage, Data: encodeMessage(second)},
		{Type: lineToolOutput, CallID: "dead-call", Output: "ignored"},
		{Type: lineToolOutput, CallID: "shared-call", Output: "early"},
		{Type: lineToolOutput, CallID: "shared-call", Output: "late"},
	}
	var lines [][]byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	msgs, err := messagesFromLines(lines)
	if err != nil {
		t.Fatal(err)
	}
	if got := msgs[0].ToolCalls()[0].Output(); got != "late" {
		t.Fatalf("first call output = %q", got)
	}
	if got := msgs[1].ToolCalls()[0].Output(); got != "" {
		t.Fatalf("second matching call should remain untouched, got %q", got)
	}
}

func TestAppendMessageReportsCommittedLineWhenMetaWriteFails(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "c1", "meta.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	message, _ := entity.NewMessage(enum.RoleUser, "committed before metadata failure")

	persisted, err := repo.AppendMessage(t.Context(), "c1", message)
	if err == nil || !persisted {
		t.Fatalf("AppendMessage = (%t, %v), want committed line plus metadata error", persisted, err)
	}
	loaded, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages()) != 1 || loaded.Messages()[0].Content() != message.Content() {
		t.Fatalf("committed line missing from history: %+v", loaded.Messages())
	}
}

func TestCompactRejectsCorruptLine(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "m1", "m2")
	r := repo.(*ConversationRepository)
	active := filepath.Join(r.path, "c1", activeFile)
	lines, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.Split(bytes.TrimSuffix(lines, []byte("\n")), []byte("\n"))
	if len(parts) < 2 {
		t.Fatal("need two message lines")
	}
	parts[0] = []byte("not-json")
	if err := os.WriteFile(active, append(bytes.Join(parts, []byte("\n")), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := entity.NewMessage(enum.RoleUser, "S")
	if err := repo.Compact(t.Context(), "c1", sum, 1); err == nil {
		t.Fatal("compact must fail on corrupt active line")
	}
}

func TestLoadAtRejectsMissingSealedSegment(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "m1", "m2", "m3")
	sum, _ := entity.NewMessage(enum.RoleUser, "S")
	if err := repo.Compact(t.Context(), "c1", sum, 2); err != nil {
		t.Fatal(err)
	}
	if err := repo.Compact(t.Context(), "c1", sum, 1); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.path, "c1", "0.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadAt(t.Context(), "c1", 0, 0); err == nil ||
		!strings.Contains(err.Error(), "sealed segment 0.jsonl missing") {
		t.Fatalf("LoadAt with missing segment → %v", err)
	}
	if err := repo.Compact(t.Context(), "c1", sum, 1); err == nil {
		t.Fatal("compact must not extend an archive with a missing segment")
	}
}

func TestCompactIgnoresPatchWithoutCall(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "m1", "m2", "m3")
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "ghost-call", "orphan")); err != nil {
		t.Fatal(err)
	}
	sum, _ := entity.NewMessage(enum.RoleUser, "S")
	if err := repo.Compact(t.Context(), "c1", sum, 1); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].Content() != "m1" || all[1].Content() != "S" {
		t.Fatalf("orphan patch changed history: %+v", all)
	}
}

func TestCompactSealAndGlobalLoadAt(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "m1", "m2", "m3", "m4", "m5")

	sum, _ := entity.NewMessage(enum.RoleUser, "SUMMARY")
	if err := repo.Compact(t.Context(), "c1", sum, 3); err != nil {
		t.Fatal(err)
	}

	// Live context = summary + tail only.
	live, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Messages()) != 3 || live.Messages()[0].Content() != "SUMMARY" {
		t.Fatalf("live context wrong: %+v", live.Messages())
	}

	// Global history still complete across the sealed boundary.
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 6 {
		t.Fatalf("global history = %d, want 6", len(all))
	}
	// Tail range across the boundary.
	tail, err := repo.LoadAt(t.Context(), "c1", -3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 3 || tail[0].Content() != "SUMMARY" || tail[2].Content() != "m5" {
		t.Fatalf("tail wrong: %+v", tail)
	}
	// Archived prefix reachable.
	head, err := repo.LoadAt(t.Context(), "c1", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if head[0].Content() != "m1" || head[1].Content() != "m2" {
		t.Fatalf("sealed head wrong: %+v", head)
	}

	info, err := repo.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.MessageCount != 6 || info.ActiveCount != 3 || info.SealedSegments != 1 {
		t.Fatalf("info counters wrong: %+v", info)
	}
}

// A call sealed before its output arrived still gets patched — the patch
// resolves globally.
func TestPatchReachesSealedCall(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("sealed-call", "tool", "{}", enum.TooltypeOrigin)
	m.AddToolCall(tc)
	if _, err := repo.AppendMessage(t.Context(), "c1", m); err != nil {
		t.Fatal(err)
	}
	sum, _ := entity.NewMessage(enum.RoleUser, "S")
	if err := repo.Compact(t.Context(), "c1", sum, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "sealed-call", "OUT")); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if all[0].ToolCalls()[0].Output() != "OUT" {
		t.Fatal("patch did not reach sealed call")
	}
}

// A patch targeting a call sealed by an earlier Compact must survive
// every later compaction.
func TestPatchSurvivesLaterCompact(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("sealed-call", "tool", "{}", enum.TooltypeOrigin)
	m.AddToolCall(tc)
	if _, err := repo.AppendMessage(t.Context(), "c1", m); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "sealed-call", "OUT")); err != nil {
		t.Fatal(err)
	}
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := repo.Compact(t.Context(), "c1", sum1, 1); err != nil { // seg0=[call], active=[S1,patch]
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2")
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := repo.Compact(t.Context(), "c1", sum2, 1); err != nil { // seg1=[S1], active=[S2,u1,u2,patch]
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if all[0].ToolCalls()[0].Output() != "OUT" {
		t.Fatal("later compact dropped the patch for a sealed call")
	}
}

func TestListOrdering(t *testing.T) {
	repo := newRepo(t)
	for _, id := range []string{"a", "b"} {
		if err := repo.Save(t.Context(), newConv(t, id)); err != nil {
			t.Fatal(err)
		}
	}
	infos, err := repo.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("List = %d, want 2", len(infos))
	}
	if infos[0].UpdatedAt.Before(infos[1].UpdatedAt) {
		t.Fatal("List not sorted newest-first")
	}
}

func TestDelete(t *testing.T) {
	root := t.TempDir()
	repo := NewConversationRepository(root)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(t.Context(), newConv(t, "c2")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2"} {
		outputDir := filepath.Join(root, id, "tool-output")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, "spill.log"), []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Delete(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "c1")); !os.IsNotExist(err) {
		t.Fatalf("deleted conversation and outputs remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "c2", "tool-output", "spill.log")); err != nil {
		t.Fatalf("another conversation's output was removed: %v", err)
	}
	if _, err := repo.Load(t.Context(), "c1"); apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatal("deleted conv still loads")
	}
}

// A truncated final line — the signature of a crash mid-append — is
// tolerated rather than failing the whole load.
func TestTruncatedTailTolerated(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "ok")
	f, err := os.OpenFile(filepath.Join(dir, "c1", activeFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","data":{"role":"us`); err != nil { // cut mid-line
		t.Fatal(err)
	}
	f.Close()

	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages()) != 1 {
		t.Fatalf("truncated tail should be dropped, got %d messages", len(got.Messages()))
	}
}

// A torn tail is the signature of a crash mid-append: the next write cuts
// it at the last line boundary rather than gluing a record onto partial
// bytes — the half-written record is unrecoverable, never merged.
func TestAppendAfterTornTailDropsPartialRecord(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	active := filepath.Join(dir, "c1", activeFile)
	f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","data:`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	appendMsgs(t, repo, "c1", "u2")

	raw, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if raw[len(raw)-1] != '\n' || strings.Count(string(raw), "\n") != 2 {
		t.Fatalf("torn bytes survived the append:\n%s", raw)
	}
	for i, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
		var record lineRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("line %d does not decode after append: %v", i, err)
		}
	}
	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages()) != 2 || got.Messages()[0].Content() != "u1" || got.Messages()[1].Content() != "u2" {
		t.Fatalf("history after torn-tail append: %+v", got.Messages())
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("global history after torn-tail append: n=%d err=%v", len(all), err)
	}
	// The clean path still appends: a newline-terminated file costs no
	// truncation and the third record lands as its own line.
	appendMsgs(t, repo, "c1", "u3")
	all, err = repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 3 || all[2].Content() != "u3" {
		t.Fatalf("append after repair: n=%d err=%v", len(all), err)
	}
	info, err := repo.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.MessageCount != 3 || info.ActiveCount != 3 {
		t.Fatalf("counters after torn-tail appends: %+v", info)
	}
}

// A file whose only bytes are one torn record truncates to empty rather
// than keeping partial history.
func TestAppendAfterTornTailTruncatesWholeFile(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "c1", activeFile)
	if err := os.WriteFile(active, []byte(`{"type":"message","data:`), 0o644); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	raw, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("torn-only file kept partial bytes:\n%s", raw)
	}
	if got := len(msgIDs(t, repo, "c1")); got != 1 {
		t.Fatalf("history = %d, want 1", got)
	}
}

// A torn tail larger than the scan window still cuts at the last line
// boundary — the backward walk keeps going until it finds a newline.
func TestAppendAfterTornTailSpansScanChunks(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	active := filepath.Join(dir, "c1", activeFile)
	f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat("x", 40<<10)); err != nil { // >32KiB torn
		t.Fatal(err)
	}
	f.Close()

	appendMsgs(t, repo, "c1", "u2")

	raw, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "\n") != 2 {
		t.Fatalf("torn bytes beyond one chunk survived:\n%s", raw[:64])
	}
	if got := len(msgIDs(t, repo, "c1")); got != 2 {
		t.Fatalf("history = %d, want 2", got)
	}
}

// readLines tolerates a final line that fails to decode even when it is
// newline-terminated, so the writer must drop that tail too: left behind,
// the next append turns it interior and every later read of the room fails.
func TestAppendAfterUndecodableTerminatedTailDropsRecord(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	active := filepath.Join(dir, "c1", activeFile)
	f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"type\":\"message\",\"data\":\n"); err != nil { // terminated, undecodable
		t.Fatal(err)
	}
	f.Close()

	appendMsgs(t, repo, "c1", "u2")

	raw, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "\n") != 2 {
		t.Fatalf("undecodable terminated line survived the append:\n%s", raw)
	}
	for i, line := range bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n")) {
		var record lineRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("line %d does not decode after append: %v", i, err)
		}
	}
	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages()) != 2 || got.Messages()[0].Content() != "u1" || got.Messages()[1].Content() != "u2" {
		t.Fatalf("history after undecodable-tail append: %+v", got.Messages())
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 2 || all[1].Content() != "u2" {
		t.Fatalf("global history after undecodable-tail append: n=%d err=%v", len(all), err)
	}
	info, err := repo.Info(t.Context(), "c1")
	if err != nil || info.MessageCount != 2 || info.ActiveCount != 2 {
		t.Fatalf("counters after undecodable-tail append: %+v err=%v", info, err)
	}
}

// A final line that is valid JSON but carries no type is torn by the same
// rule readLines applies — it is not a record — so it drops before the
// append instead of becoming interior corruption.
func TestAppendAfterTypelessTerminatedTailDropsRecord(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	active := filepath.Join(dir, "c1", activeFile)
	f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"data\":{\"id\":\"m1\",\"role\":\"user\"}}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	appendMsgs(t, repo, "c1", "u2")

	raw, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "\n") != 2 || strings.Contains(string(raw), "\"m1\"") {
		t.Fatalf("typeless terminated line survived the append:\n%s", raw)
	}
	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages()) != 2 || got.Messages()[1].Content() != "u2" {
		t.Fatalf("history after typeless-tail append: %+v", got.Messages())
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("global history after typeless-tail append: n=%d err=%v", len(all), err)
	}
}

// oldTornProbeCap is the probe window the writer used to apply before it
// was removed: the probe now reads the true final line. Kept to seed a
// final line longer than that window and prove the clean path neither
// rewrites it nor drops it.
const oldTornProbeCap = 64 << 10

// A clean file appends without a full-file rewrite even when its final line
// is longer than the probe window this writer used to bound: the record
// decodes, so it survives and the new record lands after it. Asserted
// through behavior — every pre-existing byte survives and the new record
// lands as the last line — not through syscall counting.
func TestAppendToCleanFileKeepsFinalLineBeyondOldProbeCap(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	big, err := entity.NewMessage(enum.RoleUser, strings.Repeat("x", oldTornProbeCap+(1<<10)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AppendMessage(t.Context(), "c1", big); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "c1", activeFile)
	if fi, err := os.Stat(active); err != nil || fi.Size() <= oldTornProbeCap {
		t.Fatalf("seed line must exceed the old probe cap: size=%v err=%v", fi, err)
	}
	before, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}

	appendMsgs(t, repo, "c1", "u2")

	after, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatal("clean append rewrote bytes it did not need to")
	}
	if strings.Count(string(after), "\n") != 2 {
		t.Fatalf("clean large file gained a stray line: %d lines", strings.Count(string(after), "\n"))
	}
	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages()) != 2 || got.Messages()[0].Content() != big.Content() || got.Messages()[1].Content() != "u2" {
		t.Fatalf("history after clean large append: n=%d", len(got.Messages()))
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("global history after clean large append: n=%d err=%v", len(all), err)
	}
}

// Only the torn tail drops. An undecodable interior line stays put and
// keeps failing the read loudly — never silently removed to make the load
// succeed.
func TestAppendKeepsInteriorCorruption(t *testing.T) {
	dir := t.TempDir()
	repo := NewConversationRepository(dir)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	active := filepath.Join(dir, "c1", activeFile)
	raw, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	const corrupt = "not-a-record\n"
	if err := os.WriteFile(active, append(raw, []byte(corrupt+"{\"data\":{}}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	appendMsgs(t, repo, "c1", "u2")

	after, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte(corrupt)) {
		t.Fatalf("interior corruption was silently dropped:\n%s", after)
	}
	if _, err := repo.Load(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "corrupt line") {
		t.Fatalf("Load past interior corruption → %v, want a corrupt-line error", err)
	}
	if _, err := repo.LoadAt(t.Context(), "c1", 0, 0); err == nil || !strings.Contains(err.Error(), "corrupt line") {
		t.Fatalf("LoadAt past interior corruption → %v, want a corrupt-line error", err)
	}
}

// convID becomes a directory segment — traversal ids must be rejected.
func TestInvalidIDRejected(t *testing.T) {
	repo := NewConversationRepository(t.TempDir())
	if _, err := repo.Load(t.Context(), "../escape"); err == nil ||
		apperror.As(err).StatusCode() != 400 {
		t.Fatalf("traversal id → %v, want 400", err)
	}
}

func TestDeleteRejectsTraversalWithoutRemovingSibling(t *testing.T) {
	root := t.TempDir()
	repo := NewConversationRepository(filepath.Join(root, "conversations"))
	if err := repo.Save(t.Context(), newConv(t, "c2")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "c2")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(t.Context(), "../c2"); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("traversal delete = %v, want 400", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "sentinel")); err != nil {
		t.Fatalf("directory outside conversation root was removed: %v", err)
	}
	if _, err := repo.Load(t.Context(), "c2"); err != nil {
		t.Fatalf("sibling room was removed: %v", err)
	}
}

func TestCompactIgnoresToolOutputDirectory(t *testing.T) {
	root := t.TempDir()
	repo := NewConversationRepository(root)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "old", "new")
	outputDir := filepath.Join(root, "c1", "tool-output")
	if err := os.Mkdir(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	spill := filepath.Join(outputDir, "0.jsonl")
	if err := os.WriteFile(spill, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := entity.NewMessage(enum.RoleAssistant, "summary")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Compact(t.Context(), "c1", summary, 1); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages()) != 2 || got.Messages()[0].Content() != "summary" || got.Messages()[1].Content() != "new" {
		t.Fatalf("compacted history includes tool-output or lost messages: %+v", got.Messages())
	}
	if data, err := os.ReadFile(spill); err != nil || string(data) != "keep me" {
		t.Fatalf("tool output was changed by compact: %q, %v", data, err)
	}
}

// Message ids survive a roundtrip — edit features address by id, not index.
func TestMessageIDRoundtrip(t *testing.T) {
	repo := NewConversationRepository(t.TempDir())
	conv, _ := aggregate.NewConversation("ids")
	if err := repo.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	m, _ := entity.NewMessage(enum.RoleUser, "remember me")
	if _, err := repo.AppendMessage(t.Context(), "ids", m); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Load(t.Context(), "ids")
	if err != nil {
		t.Fatal(err)
	}
	if got.Last().ID() != m.ID() || m.ID() == "" {
		t.Fatalf("id lost: %q vs %q", got.Last().ID(), m.ID())
	}
}

// messageLine marshals a stored message line — the jsonl envelope the writer
// produces — so the tail-shape tests can seed active.jsonl by hand.
func messageLine(t *testing.T, content string) []byte {
	t.Helper()
	m, err := entity.NewMessage(enum.RoleUser, content)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(lineRecord{Type: lineMessage, Data: encodeMessage(m)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func messageContents(msgs []entity.Message) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Content())
	}
	return out
}

// TestAppendMirrorsReaderFinalLineTolerance drives the append path over the
// final-line shape matrix. Each case seeds active.jsonl with raw bytes,
// appends one record, and pins what the writer left behind: Load and LoadAt
// must stay readable and return exactly the surviving records. The writer
// must mirror readLines — dropping only a final line that fails the same
// decode/type probe, repairing (not dropping) a complete record whose
// terminator was lost, and leaving interior corruption for the reader to
// report. A final line the reader would drop and the writer leaves in place
// turns interior on the next append, after which the room reads corrupt.
func TestAppendMirrorsReaderFinalLineTolerance(t *testing.T) {
	u1 := messageLine(t, "u1")
	u2 := messageLine(t, "u2")
	longValid := messageLine(t, strings.Repeat("L", oldTornProbeCap+(1<<10)))
	longUndecodable := bytes.Repeat([]byte("x"), oldTornProbeCap+(1<<10))

	// line joins raw fragments, newline-terminating each.
	line := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
			out = append(out, '\n')
		}
		return out
	}

	cases := []struct {
		name       string
		seed       []byte
		want       []string
		wantErr    bool
		rawHasLine []byte // if set, the raw file must still contain this exact run
	}{
		{
			name: "terminated valid",
			seed: line(u1, u2),
			want: []string{"u1", "u2", "new"},
		},
		{
			name: "terminated undecodable",
			seed: line(u1, []byte("not-a-record")),
			want: []string{"u1", "new"},
		},
		{
			name: "terminated typeless",
			seed: line(u1, []byte(`{"data":{"role":"user"}}`)),
			want: []string{"u1", "new"},
		},
		{
			name: "terminated blank",
			seed: line(u1, nil),
			want: []string{"u1", "new"},
		},
		{
			name: "terminated whitespace-only",
			seed: line(u1, []byte("  \t ")),
			want: []string{"u1", "new"},
		},
		{
			name:       "unterminated complete decoded",
			seed:       append(line(u1), u2...), // u2 without its terminator
			want:       []string{"u1", "u2", "new"},
			rawHasLine: append(append([]byte(nil), u2...), '\n'),
		},
		{
			name: "unterminated partial undecodable",
			seed: append(line(u1), []byte(`{"type":"message","data":{"role":"us`)...),
			want: []string{"u1", "new"},
		},
		{
			name: "unterminated whitespace-only",
			seed: append(line(u1), []byte("   ")...),
			want: []string{"u1", "new"},
		},
		{
			name: "whole file one unterminated valid record",
			seed: u1,
			want: []string{"u1", "new"},
		},
		{
			name: "whole file one unterminated partial record",
			seed: []byte(`{"type":"message","data":{"role":`),
			want: []string{"new"},
		},
		{
			name: "final line past old cap, terminated undecodable",
			seed: line(u1, longUndecodable),
			want: []string{"u1", "new"},
		},
		{
			name: "final line past old cap, unterminated valid",
			seed: append(line(u1), longValid...),
			want: []string{"u1", strings.Repeat("L", oldTornProbeCap+(1<<10)), "new"},
		},
		{
			name:    "interior corruption stays reported",
			seed:    line(u1, []byte("not-a-record"), u2),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			repo := NewConversationRepository(dir)
			if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
				t.Fatal(err)
			}
			active := filepath.Join(dir, "c1", activeFile)
			if err := os.WriteFile(active, tc.seed, 0o644); err != nil {
				t.Fatal(err)
			}

			appendMsgs(t, repo, "c1", "new")

			if tc.wantErr {
				if _, err := repo.Load(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "corrupt line") {
					t.Fatalf("Load → %v, want a corrupt-line error", err)
				}
				if _, err := repo.LoadAt(t.Context(), "c1", 0, 0); err == nil || !strings.Contains(err.Error(), "corrupt line") {
					t.Fatalf("LoadAt → %v, want a corrupt-line error", err)
				}
				return
			}

			got, err := repo.Load(t.Context(), "c1")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if contents := messageContents(got.Messages()); !slices.Equal(contents, tc.want) {
				t.Fatalf("Load records = %q, want %q", contents, tc.want)
			}
			all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
			if err != nil {
				t.Fatalf("LoadAt: %v", err)
			}
			if contents := messageContents(all); !slices.Equal(contents, tc.want) {
				t.Fatalf("LoadAt records = %q, want %q", contents, tc.want)
			}
			if len(tc.rawHasLine) > 0 {
				raw, err := os.ReadFile(active)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(raw, tc.rawHasLine) {
					t.Fatalf("writer dropped a decodable record instead of repairing its terminator:\n%s", raw)
				}
			}
		})
	}
}
