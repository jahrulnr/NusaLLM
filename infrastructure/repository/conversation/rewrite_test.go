package repoconversation

import (
	"os"
	"path/filepath"
	"strings"
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
	if err := compactAt(t, repo, "c1", sum1, 3); err != nil { // seg0=[u1,u2,u3], active=[S1,u4,u5]
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u6")
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, repo, "c1", sum2, 2); err != nil { // seg1=[S1,u4], active=[S2,u5,u6]
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
	if err := compactAt(t, repo, "c1", sum, 2); err != nil {
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
	unblock := blockAtomicWrites(t, metaFile)
	if err := compactAt(t, repo, "c1", sum, 2); err == nil {
		t.Fatal("expected metadata publish failure")
	}
	unblock()
	segment := filepath.Join(dir, "0.jsonl")
	before, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	if err := compactAt(t, repo, "c1", sum, 1); err == nil {
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

// The sealed-path commit stages a durable payload before any canonical
// name moves — a failed payload write must leave the archive untouched.
func TestRewriteFromSealedPayloadFailureKeepsArchive(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3")
	ids := msgIDs(t, repo, "c1")
	sum, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, repo, "c1", sum, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	segBefore, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	activeBefore, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	// The fault: the payload write must fail while the archive is still
	// untouched.
	blockAtomicWrites(t, rewritePayload)

	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err == nil {
		t.Fatal("expected RewriteFrom to fail on the payload write")
	}

	got, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil || string(got) != string(segBefore) {
		t.Fatalf("sealed segment changed across failed rewrite: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil || string(got) != string(activeBefore) {
		t.Fatalf("active.jsonl changed across failed rewrite: %v", err)
	}
	if _, err := repo.LoadAt(t.Context(), "c1", 0, 0); err != nil {
		t.Fatalf("history unreadable after failed payload write: %v", err)
	}
}

// A segment that cannot be parked must roll back the renames already
// done — the canonical archive stays readable, nothing is lost.
func TestRewriteFromSealedParkFailureRestoresSegments(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3", "u4", "u5")
	ids := msgIDs(t, repo, "c1")
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, repo, "c1", sum1, 3); err != nil { // seg0=[u1,u2,u3], active=[S1,u4,u5]
		t.Fatal(err)
	}
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, repo, "c1", sum2, 2); err != nil { // seg1=[S1,u4], active=[S2,u5]
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	segBefore, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// A non-empty directory under the parked name cannot be renamed over
	// or swept — parking 1.jsonl fails after 0.jsonl already moved.
	blocker := filepath.Join(dir, "1.jsonl"+rewriteParkSuffix)
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err == nil {
		t.Fatal("expected RewriteFrom to fail while parking")
	}

	got, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil || string(got) != string(segBefore) {
		t.Fatalf("rolled-back segment 0.jsonl changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.jsonl")); err != nil {
		t.Fatalf("unreached segment 1.jsonl lost: %v", err)
	}
	// The only parked name that should remain is the injected blocker
	// itself — the rename the failure was staged on.
	if _, err := os.Stat(filepath.Join(dir, "0.jsonl"+rewriteParkSuffix)); !os.IsNotExist(err) {
		t.Fatalf("parked copy of 0.jsonl left behind by rollback")
	}
	if _, err := os.Stat(filepath.Join(dir, rewritePayload)); !os.IsNotExist(err) {
		t.Fatalf("payload left behind by rollback: %v", err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 7 {
		t.Fatalf("history after failed parking: n=%d err=%v", len(all), err)
	}
}

// A failed publish rolls the parking back — the canonical stream and
// every sealed era survive untouched.
func TestRewriteFromSealedPublishFailureKeepsArchive(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3")
	ids := msgIDs(t, repo, "c1")
	sum, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, repo, "c1", sum, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	segBefore, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	activeBefore, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	unblock := blockAtomicWrites(t, activeFile)

	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err == nil {
		t.Fatal("expected RewriteFrom to fail on publish")
	}

	got, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil || string(got) != string(segBefore) {
		t.Fatalf("sealed segment changed across failed publish: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil || string(got) != string(activeBefore) {
		t.Fatalf("active.jsonl changed across failed publish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, rewritePayload)); !os.IsNotExist(err) {
		t.Fatalf("payload left behind by rollback: %v", err)
	}
	if _, err := repo.LoadAt(t.Context(), "c1", 0, 0); err != nil {
		t.Fatalf("history unreadable after failed publish: %v", err)
	}
	// The failure was clean: unblocking lets the edit land.
	unblock()
	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 2 || all[1].Content() != "u2*" {
		t.Fatalf("history after retry: %+v err=%v", all, err)
	}
}

// A crash between publish and metadata recount is the documented
// interrupted-edit window: reads fail with the repair verb, and the
// recovery command rolls the staged payload forward.
func TestRewriteFromSealedMetaFailureLeavesRecoverableWindow(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3")
	ids := msgIDs(t, repo, "c1")
	sum, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, repo, "c1", sum, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	unblock := blockAtomicWrites(t, metaFile)

	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err == nil {
		t.Fatal("expected RewriteFrom to fail on the metadata recount")
	}

	for _, check := range []func() error{
		func() error { _, err := repo.Load(t.Context(), "c1"); return err },
		func() error { _, err := repo.LoadAt(t.Context(), "c1", 0, 0); return err },
		func() error { _, err := repo.Info(t.Context(), "c1"); return err },
	} {
		err := check()
		if err == nil || !strings.Contains(err.Error(), "--finish-rewrite") {
			t.Fatalf("interrupted edit must name the repair verb, got %v", err)
		}
	}
	m, _ := entity.NewMessage(enum.RoleUser, "new")
	if _, err := repo.AppendMessage(t.Context(), "c1", m); err == nil {
		t.Fatal("append proceeded on an interrupted archive")
	}
	if err := compactAt(t, repo, "c1", sum, 1); err == nil {
		t.Fatal("compact proceeded on an interrupted archive")
	}

	unblock()
	if err := r.RepairRewrite(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Content() != "u1" || all[1].Content() != "u2*" {
		t.Fatalf("repaired history = %+v, want the edit's merged stream", all)
	}
	info, err := repo.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 0 || info.MessageCount != 2 || info.ActiveCount != 2 {
		t.Fatalf("counters after roll-forward: %+v", info)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "nothing to repair") {
		t.Fatalf("second repair must refuse cleanly: %v", err)
	}
}

// Leftovers from a crashed edit — a payload whose commit never landed and
// parked segment names — are debris once the archive validates again:
// invisible to readers, swept by the next sealed edit, never republished.
func TestRewriteFromSealedSweepsStaleLeftovers(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2", "u3")
	ids := msgIDs(t, repo, "c1")
	sum, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, repo, "c1", sum, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	payload := filepath.Join(dir, rewritePayload)
	if err := os.WriteFile(payload, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(dir, "9.jsonl"+rewriteParkSuffix)
	if err := os.WriteFile(parked, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadAt(t.Context(), "c1", 0, 0); err != nil {
		t.Fatalf("debris must be invisible to readers: %v", err)
	}

	if err := repo.RewriteFrom(t.Context(), "c1", ids[1], "u2*"); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[1].Content() != "u2*" {
		t.Fatalf("stale payload shaped history: %+v", all)
	}
	if _, err := os.Stat(payload); !os.IsNotExist(err) {
		t.Fatalf("stale payload survived the rewrite: %v", err)
	}
	if _, err := os.Stat(parked); !os.IsNotExist(err) {
		t.Fatalf("parked leftover survived the rewrite: %v", err)
	}
}

// The sweep also claims the payload's staged temp sibling — the name
// writeFileAtomic would reuse. A sealed edit that finds nothing still runs
// the sweep first, so the leftover must not survive it.
func TestRewriteFromSweepsPayloadTempSibling(t *testing.T) {
	repo := newRepo(t)
	r := repo.(*ConversationRepository)
	if err := repo.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, repo, "c1", "u1", "u2")
	sum, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, repo, "c1", sum, 1); err != nil {
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	staged := filepath.Join(dir, rewritePayload+".tmp")
	if err := os.WriteFile(staged, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := repo.RewriteFrom(t.Context(), "c1", "missing-id", "x")
	if err == nil || apperror.As(err).StatusCode() != apperror.StatusNotFound {
		t.Fatalf("RewriteFrom(unknown id) = %v, want 404", err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged payload temp sibling survived the sweep: %v", err)
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
	if err := compactAt(t, repo, "c1", sum, 1); err != nil { // seg0=[call], active=[S,u1]
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
