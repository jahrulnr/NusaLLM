package repoconversation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

func msgIDs(t *testing.T, repo drepo.Conversation, id string) []string {
	t.Helper()
	all, err := repo.LoadAt(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(all))
	for i, m := range all {
		ids[i] = m.ID()
	}
	return ids
}

// Editing a message in the live tail rewrites its content and drops the
// suffix — the stream ends at the edited line.
func TestRewriteFromActiveTruncates(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3")
	ids := msgIDs(t, repo, "c1")

	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err != nil {
		t.Fatal(err)
	}

	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Content() != "u1" || all[1].Content() != "u2*" {
		t.Fatalf("history after rewrite: %+v", all)
	}
	info, err := repo.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.MessageCount != 2 || info.ActiveCount != 2 || info.SealedSegments != 0 {
		t.Fatalf("counters after rewrite: %+v", info)
	}
}

// Editing a compacted message promotes its era back into the live tail:
// earlier sealed segments merge verbatim, later segments and the old tail
// are discarded, and the stale summary is gone.
func TestRewriteFromSealedPromotesEra(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3", "u4", "u5")
	ids := msgIDs(t, repo, "c1")

	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := repo.Compact(t.Context(), "c1", sum1, 3); err != nil { // seg0=[u1,u2,u3], active=[S1,u4,u5]
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u6")
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := repo.Compact(t.Context(), "c1", sum2, 2); err != nil { // seg1=[S1,u4], active=[S2,u5,u6]
		t.Fatal(err)
	}

	// u4 sits in seg1 — the edit must unseal seg0 + seg1's prefix.
	if err := repo.RewriteFrom(t.Context(), "c1", ids[3], "u4*"); err != nil {
		t.Fatal(err)
	}

	live, err := repo.Load(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	// [u1,u2,u3,S1,u4*] — S1 stays: it was a real message in the stream and
	// still summarizes u1..u3, which are live again. S2 dies with the tail
	// it summarized.
	if len(live.Messages()) != 5 ||
		live.Messages()[0].Content() != "u1" || live.Messages()[3].Content() != "S1" ||
		live.Messages()[4].Content() != "u4*" {
		t.Fatalf("promoted history wrong: %+v", live.Messages())
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("sealed data must not linger: %d msgs", len(all))
	}
	info, err := repo.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 0 || info.MessageCount != 5 || info.ActiveCount != 5 {
		t.Fatalf("counters after promote: %+v", info)
	}
	// Repromotion must not leave numbered segments on disk — they would
	// duplicate the merged era if meta ever drifted.
	dir := filepath.Join(repo.(*ConversationRepository).path, "c1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != activeFile && e.Name() != metaFile && filepath.Ext(e.Name()) == ".jsonl" {
			t.Fatalf("stale segment after repromote: %s", e.Name())
		}
	}
}

func TestRewriteFromActiveRecountsSealedHistory(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3")
	ids := msgIDs(t, repo, "c1")
	sum, _ := entity.NewMessage(enum.RoleUser, "summary")
	if err := repo.Compact(t.Context(), "c1", sum, 2); err != nil {
		t.Fatal(err)
	}
	if err := repo.RewriteFrom(t.Context(), "c1", ids[2], "u3*"); err != nil {
		t.Fatal(err)
	}
	info, err := repo.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 1 || info.ActiveCount != 2 || info.MessageCount != 4 {
		t.Fatalf("metadata lost sealed history: %+v", info)
	}
}

func TestSurgeryStopsAtUncommittedSealedSegment(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3")
	ids := msgIDs(t, repo, "c1")
	sum, _ := entity.NewMessage(enum.RoleUser, "summary")
	dir := r.convDir("c1")
	blockedMeta := filepath.Join(dir, "meta.json.tmp")
	if err := os.Mkdir(blockedMeta, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := repo.Compact(t.Context(), "c1", sum, 2); err == nil {
		t.Fatal("expected metadata publish failure")
	}
	if err := os.Remove(blockedMeta); err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(dir, "0.jsonl")
	before, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Compact(t.Context(), "c1", sum, 1); err == nil {
		t.Fatal("compact overwrote an uncommitted sealed era")
	}
	if err := repo.RewriteFrom(t.Context(), "c1", ids[2], "u3*"); err == nil {
		t.Fatal("rewrite proceeded while an uncommitted era was present")
	}
	if _, err := repo.Load(t.Context(), "c1"); err == nil {
		t.Fatal("load hid the uncommitted era")
	}
	if _, err := repo.LoadAt(t.Context(), "c1", 0, 0); err == nil {
		t.Fatal("history read hid the uncommitted era")
	}
	if err := repo.Save(t.Context(), newConv(t, "c1")); err == nil {
		t.Fatal("save replaced inconsistent metadata")
	}
	after, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("uncommitted era changed")
	}
}

func TestRewriteFromNotFound(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	err := repo.RewriteFrom(t.Context(), "c1", "m_nope", "x")
	if apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("rewrite missing id → %v, want 404", err)
	}
	if err := repo.RewriteFrom(t.Context(), "nope", "m_x", "x"); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("rewrite missing room → %v, want 404", err)
	}
}

// Assistant turns are off-limits — only user input may be rewritten.
func TestRewriteFromRejectsNonUser(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	asst, _ := entity.NewMessage(enum.RoleAssistant, "reply")
	if _, err := repo.AppendMessage(t.Context(), "c1", asst); err != nil {
		t.Fatal(err)
	}
	ids := msgIDs(t, repo, "c1")
	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "hijack"); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("assistant rewrite → %v, want 400", err)
	}
}

// A tool call dropped by a rewrite stays dropped — a patch landing later
// must not resurrect it.
func TestRewriteFromDropsPendingCall(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	asst, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("call_x", "tool", "{}", enum.TooltypeOrigin)
	asst.AddToolCall(tc)
	if _, err := repo.AppendMessage(t.Context(), "c1", asst); err != nil {
		t.Fatal(err)
	}
	ids := msgIDs(t, repo, "c1")

	if err := repo.RewriteFrom(t.Context(), "c1", ids[0], "u1*"); err != nil {
		t.Fatal(err)
	}
	// A patch for the dropped call applies to nothing — no error, no ghost.
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "call_x", "late")); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Content() != "u1*" {
		t.Fatalf("dropped call must stay dropped: %+v", all)
	}
}

// A patch written after the edit target still belongs to a retained
// call — patches resolve globally, so the cut must carry them along.
func TestRewriteFromIgnoresPatchWithoutCall(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2")
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "ghost-call", "orphan")); err != nil {
		t.Fatal(err)
	}
	ids := msgIDs(t, repo, "c1")

	if err := repo.RewriteFrom(t.Context(), "c1", ids[0], "u1*"); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Content() != "u1*" {
		t.Fatalf("orphan patch changed rewritten history: %+v", all)
	}
}

func TestRewriteFromKeepsPatchesForRetainedCalls(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	asst, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("call_keep", "tool", "{}", enum.TooltypeOrigin)
	asst.AddToolCall(tc)
	if _, err := repo.AppendMessage(t.Context(), "c1", asst); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u2")
	tail, _ := entity.NewMessage(enum.RoleAssistant, "tail")
	if _, err := repo.AppendMessage(t.Context(), "c1", tail); err != nil {
		t.Fatal(err)
	}
	// The output lands at the file tail — after the edit target.
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "call_keep", "kept result")); err != nil {
		t.Fatal(err)
	}
	ids := msgIDs(t, repo, "c1")

	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[1].Content() != "u2*" {
		t.Fatalf("history after rewrite: %+v", all)
	}
	got := all[0].ToolCalls()
	if len(got) != 1 || got[0].Output() != "kept result" {
		t.Fatalf("patch past the cut must still resolve: %+v", got)
	}
}

// Editing a message in the live tail leaves the sealed segments alone —
// a patch for a call compacted before its output arrived must survive.
func TestRewriteFromKeepsPatchForSealedCall(t *testing.T) {
	repo := newRepo(t)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	asst, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("call_sealed", "tool", "{}", enum.TooltypeOrigin)
	asst.AddToolCall(tc)
	if _, err := repo.AppendMessage(t.Context(), "c1", asst); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1")
	sum, _ := entity.NewMessage(enum.RoleUser, "S")
	if err := repo.Compact(t.Context(), "c1", sum, 1); err != nil { // seg0=[call], active=[S,u1]
		t.Fatal(err)
	}
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "call_sealed", "sealed result")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u2")
	ids := msgIDs(t, repo, "c1")

	if err := repo.RewriteFrom(t.Context(), "c1", ids[2], "u1*"); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[2].Content() != "u1*" {
		t.Fatalf("history after rewrite: %+v", all)
	}
	got := all[0].ToolCalls()
	if len(got) != 1 || got[0].Output() != "sealed result" {
		t.Fatalf("rewrite dropped the patch for a sealed call: %+v", got)
	}
}
