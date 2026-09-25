package repoconversation

import (
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
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
	if err := repo.AppendMessage(t.Context(), "c1", asst); err != nil {
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
	if err := repo.AppendMessage(t.Context(), "c1", asst); err != nil {
		t.Fatal(err)
	}
	ids := msgIDs(t, repo, "c1")

	if err := repo.RewriteFrom(t.Context(), "c1", ids[0], "u1*"); err != nil {
		t.Fatal(err)
	}
	// A patch for the dropped call applies to nothing — no error, no ghost.
	if err := repo.UpdateToolCallOutput(t.Context(), "c1", "call_x", "late"); err != nil {
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
