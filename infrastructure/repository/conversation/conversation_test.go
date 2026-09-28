package repoconversation

import (
	"os"
	"path/filepath"
	"testing"

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

func newConv(t *testing.T, id string) *aggregate.Conversation {
	t.Helper()
	c, err := aggregate.NewConversation(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
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
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", "call-1", "<env>wrapped</env>",
		[]valueobject.Attachment{att}); err != nil {
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
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", "sealed-call", "OUT", nil); err != nil {
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
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(t.Context(), "c1"); err != nil {
		t.Fatal(err)
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
