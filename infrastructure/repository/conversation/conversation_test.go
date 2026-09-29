package repoconversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
