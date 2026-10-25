package repoconversation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

func TestRepairOrphanDuplicate(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one")
	if err := r.UpdateToolCallOutput(t.Context(), "c1", patchCall(t, "ghost", "ignored")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "two")
	dir := r.convDir("c1")
	active, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	var messages [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(active), []byte("\n")) {
		if bytes.Contains(line, []byte(`"type":"message"`)) {
			messages = append(messages, line)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), joinLines(messages), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil {
		t.Fatal("must not commit a duplicate prefix")
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "0.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("duplicate segment remains: %v", err)
	}
	if got := len(msgIDs(t, r, "c1")); got != 2 {
		t.Fatalf("messages = %d", got)
	}
}

func TestRepairOrphanRejectsPartialMessageOverlap(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one")
	dir := r.convDir("c1")
	active, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	ghost, _ := entity.NewMessage(enum.RoleUser, "ghost")
	ghostLine, err := json.Marshal(lineRecord{Type: lineMessage, Data: encodeMessage(ghost)})
	if err != nil {
		t.Fatal(err)
	}
	orphan := append(append([]byte(nil), active...), append(ghostLine, '\n')...)
	segment := filepath.Join(dir, "0.jsonl")
	if err := os.WriteFile(segment, orphan, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil || !strings.Contains(err.Error(), "partial overlap requires manual reconciliation — stop the app, back up the room, then inspect its files or restore from backup") {
		t.Fatalf("partially overlapping orphan needs manual recovery guidance: %v", err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate); err == nil || !strings.Contains(err.Error(), "partial overlap requires manual reconciliation — stop the app, back up the room, then inspect its files or restore from backup") {
		t.Fatalf("partially overlapping orphan must not be discarded: %v", err)
	}
	if got, err := os.ReadFile(segment); err != nil || !bytes.Equal(got, orphan) {
		t.Fatalf("failed repair changed orphan: %v", err)
	}
}

func TestRepairOrphanRejectsOverlapWithCommittedSegment(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one", "two")
	summary, _ := entity.NewMessage(enum.RoleUser, "summary")
	if err := compactAt(t, r, "c1", summary, 1); err != nil {
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	oldSegment, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), oldSegment, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("old-era duplicate must be rejected: %v", err)
	}
}

func TestRepairOrphanRejectsMalformedSegments(t *testing.T) {
	tests := []struct {
		name, want string
		mutate     func(t *testing.T, dir string, active []byte)
	}{
		{"two extras", "exactly one extra", func(t *testing.T, dir string, active []byte) {
			for _, name := range []string{"0.jsonl", "1.jsonl"} {
				if err := os.WriteFile(filepath.Join(dir, name), active, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"gap", "segment 0 missing", func(t *testing.T, dir string, active []byte) {
			if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), active, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty", "empty or incomplete", func(t *testing.T, dir string, _ []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"no newline", "empty or incomplete", func(t *testing.T, dir string, active []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), bytes.TrimSuffix(active, []byte("\n")), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-json", "no valid message", func(t *testing.T, dir string, _ []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), []byte("corrupt\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-message", "non-message line", func(t *testing.T, dir string, _ []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), []byte("{\"type\":\"tool_output\"}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"noncanonical bytes", "incomplete lines", func(t *testing.T, dir string, active []byte) {
			if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), append([]byte(" "), active...), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t).(*ConversationRepository)
			if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
				t.Fatal(err)
			}
			appendMsgs(t, r, "c1", "one")
			dir := r.convDir("c1")
			active, err := os.ReadFile(filepath.Join(dir, activeFile))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, dir, active)
			if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("repair error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestArchiveMismatchGuidance(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, -1); err == nil || !strings.Contains(err.Error(), "conversation c1") || !strings.Contains(err.Error(), "metadata") || strings.Contains(err.Error(), "repair <id>") {
		t.Fatalf("negative metadata count guidance = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, 0); err == nil || !strings.Contains(err.Error(), "conversation c1") || !strings.Contains(err.Error(), "offline repair") {
		t.Fatalf("single orphan guidance = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, 0); err == nil || !strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "offline repair") {
		t.Fatalf("multiple orphan guidance = %v", err)
	}
}

func TestArchiveGapDoesNotSuggestAutomaticRepair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSealedArchive(dir, 0); err == nil || !strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "offline repair") {
		t.Fatalf("gap guidance = %v", err)
	}
}

// The mid-parking crash window built by hand — payload staged, one
// segment parked, one still numbered, stale meta counting both — rolls
// forward: the payload becomes the live tail, every leftover goes,
// counters resync.
func TestRepairRewriteFinishesInterruptedEdit(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2", "u3", "u4")
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum1, 2); err != nil { // seg0=[u1,u2], active=[S1,u3,u4]
		t.Fatal(err)
	}
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, r, "c1", sum2, 2); err != nil { // seg1=[S1,u3], active=[S2,u4]
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	seg0Lines, err := readLines(filepath.Join(dir, "0.jsonl"))
	if err != nil || len(seg0Lines) != 2 {
		t.Fatalf("sealed segment 0: %d lines, %v", len(seg0Lines), err)
	}
	seg1Lines, err := readLines(filepath.Join(dir, "1.jsonl"))
	if err != nil || len(seg1Lines) != 2 {
		t.Fatalf("sealed segment 1: %d lines, %v", len(seg1Lines), err)
	}
	// The edit targets u3 in seg1: merged = seg0 verbatim + seg1's cut.
	var edited lineRecord
	if err := json.Unmarshal(seg1Lines[1], &edited); err != nil {
		t.Fatal(err)
	}
	edited.Data.Content = "u3*"
	patched, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	merged := append(append(append([][]byte{}, seg0Lines...), seg1Lines[0]), patched)
	if err := os.WriteFile(filepath.Join(dir, rewritePayload), joinLines(merged), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "0.jsonl"), filepath.Join(dir, "0.jsonl"+rewriteParkSuffix)); err != nil {
		t.Fatal(err)
	}

	err = func() error { _, err := r.Load(t.Context(), "c1"); return err }()
	if err == nil || !strings.Contains(err.Error(), "--finish-rewrite") {
		t.Fatalf("interrupted edit must name the repair verb, got %v", err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	all, err := r.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].Content() != "u1" || all[1].Content() != "u2" ||
		all[2].Content() != "S1" || all[3].Content() != "u3*" {
		t.Fatalf("repaired history = %+v, want the staged merged stream", all)
	}
	info, err := r.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 0 || info.MessageCount != 4 || info.ActiveCount != 4 {
		t.Fatalf("counters after roll-forward: %+v", info)
	}
	for _, name := range []string{rewritePayload, "0.jsonl" + rewriteParkSuffix, "1.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("leftover %s survived repair", name)
		}
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "nothing to repair") {
		t.Fatalf("second repair must refuse cleanly: %v", err)
	}
}

// The repair's own late crash — payload published and every leftover
// deleted while the meta still counts the old segments — is still the
// window: the payload already equals the live tail, so recounting and
// dropping it finishes the job.
func TestRepairRewriteFinishesPublishedWindow(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2")
	sum, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum, 1); err != nil { // seg0=[u1], active=[S1,u2], meta=1
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	segLines, err := readLines(filepath.Join(dir, "0.jsonl"))
	if err != nil || len(segLines) != 1 {
		t.Fatalf("sealed segment: %d lines, %v", len(segLines), err)
	}
	var edited lineRecord
	if err := json.Unmarshal(segLines[0], &edited); err != nil {
		t.Fatal(err)
	}
	edited.Data.Content = "u1*"
	patched, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	merged := joinLines([][]byte{patched})
	// Publish landed and leftovers are gone; only the meta lags.
	if err := os.WriteFile(filepath.Join(dir, activeFile), merged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rewritePayload), merged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "0.jsonl")); err != nil {
		t.Fatal(err)
	}

	err = func() error { _, err := r.Load(t.Context(), "c1"); return err }()
	if err == nil || !strings.Contains(err.Error(), "--finish-rewrite") {
		t.Fatalf("published window must name the repair verb, got %v", err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	all, err := r.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 1 || all[0].Content() != "u1*" {
		t.Fatalf("repaired history: %+v err=%v", all, err)
	}
	info, err := r.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 0 || info.MessageCount != 1 || info.ActiveCount != 1 {
		t.Fatalf("counters after roll-forward: %+v", info)
	}
}

// The repair's own late crash can leave the publish landed (payload ==
// live tail) with the cleanup half-done — a gapped parked leftover and
// the counters still counting the old segments. The escape hatch keeps
// this edit-shaped regardless of the parked gap: publishing is a no-op,
// so it rolls forward.
func TestRepairRewriteFinishesPublishedWindowWithGappedLeftovers(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2", "u3")
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum1, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, r, "c1", sum2, 1); err != nil { // seg1=[S1], active=[S2,u3], meta=2
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	seg0Lines, err := readLines(filepath.Join(dir, "0.jsonl"))
	if err != nil || len(seg0Lines) != 2 {
		t.Fatalf("sealed segment 0: %d lines, %v", len(seg0Lines), err)
	}
	seg1Lines, err := readLines(filepath.Join(dir, "1.jsonl"))
	if err != nil || len(seg1Lines) != 1 {
		t.Fatalf("sealed segment 1: %d lines, %v", len(seg1Lines), err)
	}
	merged := joinLines(append(append([][]byte{}, seg0Lines...), seg1Lines...))
	// Publish landed (payload == live tail); cleanup is half-done — the
	// numbered segments are gone but one parked name lingers out of order.
	if err := os.WriteFile(filepath.Join(dir, activeFile), merged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rewritePayload), merged, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"0.jsonl", "1.jsonl"} {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	lingering := filepath.Join(dir, "1.jsonl"+rewriteParkSuffix)
	if err := os.WriteFile(lingering, []byte("stale copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = func() error { _, err := r.Load(t.Context(), "c1"); return err }()
	if err == nil || !strings.Contains(err.Error(), "--finish-rewrite") {
		t.Fatalf("published window must name the repair verb, got %v", err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	all, err := r.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 3 ||
		all[0].Content() != "u1" || all[1].Content() != "u2" || all[2].Content() != "S1" {
		t.Fatalf("repaired history = %+v err=%v", all, err)
	}
	info, err := r.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 0 || info.MessageCount != 3 || info.ActiveCount != 3 {
		t.Fatalf("counters after roll-forward: %+v", info)
	}
	for _, name := range []string{"0.jsonl", "1.jsonl", rewritePayload, "1.jsonl" + rewriteParkSuffix} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("leftover %s survived repair", name)
		}
	}
}

// Crash after parking every segment but before the publish. Every
// numbered name is a parked prefix, no numbered segment remains, and the
// payload is a decodable merged stream that is not yet the live tail —
// the widest edit-shaped window still rolls forward.
func TestRepairRewriteFinishesAllParkedWindow(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2", "u3")
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum1, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, r, "c1", sum2, 1); err != nil { // seg1=[S1], active=[S2,u3]
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	seg0Lines, err := readLines(filepath.Join(dir, "0.jsonl"))
	if err != nil || len(seg0Lines) != 2 {
		t.Fatalf("sealed segment 0: %d lines, %v", len(seg0Lines), err)
	}
	seg1Lines, err := readLines(filepath.Join(dir, "1.jsonl"))
	if err != nil || len(seg1Lines) != 1 {
		t.Fatalf("sealed segment 1: %d lines, %v", len(seg1Lines), err)
	}
	// The edit targets S1 in seg1: merged = seg0 verbatim + seg1's cut.
	var edited lineRecord
	if err := json.Unmarshal(seg1Lines[0], &edited); err != nil {
		t.Fatal(err)
	}
	edited.Data.Content = "S1*"
	patched, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	merged := append(append([][]byte{}, seg0Lines...), patched)
	if err := os.WriteFile(filepath.Join(dir, rewritePayload), joinLines(merged), 0o644); err != nil {
		t.Fatal(err)
	}
	// Every numbered segment is parked; the publish never landed.
	for _, name := range []string{"0.jsonl", "1.jsonl"} {
		if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+rewriteParkSuffix)); err != nil {
			t.Fatal(err)
		}
	}

	err = func() error { _, err := r.Load(t.Context(), "c1"); return err }()
	if err == nil || !strings.Contains(err.Error(), "--finish-rewrite") {
		t.Fatalf("all-parked window must name the repair verb, got %v", err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	all, err := r.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil || len(all) != 3 ||
		all[0].Content() != "u1" || all[1].Content() != "u2" || all[2].Content() != "S1*" {
		t.Fatalf("repaired history = %+v err=%v", all, err)
	}
	info, err := r.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 0 || info.MessageCount != 3 || info.ActiveCount != 3 {
		t.Fatalf("counters after roll-forward: %+v", info)
	}
}

// The reviewer's counterexample: a hand-damaged layout the edit protocol
// cannot leave — meta counts two segments, only 0.jsonl is numbered, a
// parked name sits above it, and a stale (but decodable) payload is
// present. It is not the crash window, so RepairRewrite refuses, reads
// keep the mismatch's own guidance, and nothing on disk moves.
func TestRepairRewriteRefusesWrongSideParkedSet(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2", "u3")
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum1, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u4")
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, r, "c1", sum2, 2); err != nil { // seg1=[S1,u3], active=[S2,u4], meta=2
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	seg0, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	// Compound manual damage: segment 1 hand-deleted, debris parked under
	// its name, a stale payload left behind.
	if err := os.Remove(filepath.Join(dir, "1.jsonl")); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(dir, "1.jsonl"+rewriteParkSuffix)
	parkedBytes := []byte("parked debris\n")
	if err := os.WriteFile(parked, parkedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(dir, rewritePayload)
	payloadBytes := seg0 // decodable, so a refusal is about shape, not bytes
	if err := os.WriteFile(payload, payloadBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, read := range []func() error{
		func() error { _, err := r.Load(t.Context(), "c1"); return err },
		func() error { _, err := r.LoadAt(t.Context(), "c1", 0, 0); return err },
		func() error { _, err := r.Info(t.Context(), "c1"); return err },
	} {
		if err := read(); err == nil || !strings.Contains(err.Error(), "sealed segment 1.jsonl missing") ||
			strings.Contains(err.Error(), "--finish-rewrite") {
			t.Fatalf("reads must keep the mismatch guidance, got %v", err)
		}
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "not an interrupted edit") {
		t.Fatalf("repair must refuse a wrong-side parked set: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "0.jsonl")); err != nil || !bytes.Equal(got, seg0) {
		t.Fatalf("committed segment changed: %v", err)
	}
	if got, err := os.ReadFile(payload); err != nil || !bytes.Equal(got, payloadBytes) {
		t.Fatalf("payload changed: %v", err)
	}
	if got, err := os.ReadFile(parked); err != nil || !bytes.Equal(got, parkedBytes) {
		t.Fatalf("parked debris changed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, activeFile)); err != nil || !bytes.Equal(got, active) {
		t.Fatalf("live tail changed: %v", err)
	}
}

// A gap under a parked name is not the protocol's layout — it parks in
// ascending order and never skips one — so a lone 2.jsonl.rewrite-old
// beside a numbered 0.jsonl is refused like any other foreign mismatch.
func TestRepairRewriteRefusesGappedParkedSet(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2", "u3")
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum1, 2); err != nil { // seg0=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u4")
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, r, "c1", sum2, 2); err != nil { // seg1=[S1,u3], active=[S2,u4], meta=2
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	if err := os.Remove(filepath.Join(dir, "1.jsonl")); err != nil {
		t.Fatal(err)
	}
	seg0, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2.jsonl"+rewriteParkSuffix), []byte("debris\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A decodable payload, so the refusal is about the layout, not bytes.
	if err := os.WriteFile(filepath.Join(dir, rewritePayload), seg0, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "not an interrupted edit") {
		t.Fatalf("repair must refuse a gapped parked set: %v", err)
	}
	if _, err := r.Load(t.Context(), "c1"); err == nil || strings.Contains(err.Error(), "--finish-rewrite") {
		t.Fatalf("reads must keep the mismatch guidance, got %v", err)
	}
}

// The parked set is a valid contiguous prefix but a numbered segment sits
// at or below it — the protocol leaves only a suffix, so the layout is
// not the window and the verb refuses.
func TestRepairRewriteRefusesNumberedBelowParked(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2", "u3", "u4")
	sum1, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum1, 2); err != nil { // seg0=[u1,u2]
		t.Fatal(err)
	}
	sum2, _ := entity.NewMessage(enum.RoleUser, "S2")
	if err := compactAt(t, r, "c1", sum2, 1); err != nil { // seg1=[S1]
		t.Fatal(err)
	}
	sum3, _ := entity.NewMessage(enum.RoleUser, "S3")
	if err := compactAt(t, r, "c1", sum3, 1); err != nil { // seg2=[S2], meta=3
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	// Park a contiguous prefix 0,1 and keep 0.jsonl numbered as well.
	for _, n := range []string{"0.jsonl", "1.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n+rewriteParkSuffix), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"1.jsonl", "2.jsonl"} {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	seg0, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// A decodable payload, so the refusal is about the layout, not bytes.
	if err := os.WriteFile(filepath.Join(dir, rewritePayload), seg0, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "not an interrupted edit") {
		t.Fatalf("repair must refuse a numbered segment below the parked prefix: %v", err)
	}
}

// A debris payload beside a mismatch the edit never leaves is not this
// window — the orphan keeps its own guidance and the payload is never
// republished.
func TestStalePayloadDoesNotShadowOrphan(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "u1", "u2", "u3")
	sum, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := compactAt(t, r, "c1", sum, 2); err != nil { // seg0=[u1,u2], active=[S1,u3], meta=1
		t.Fatal(err)
	}
	dir := r.convDir("c1")
	// The Compact crash window: one extra numbered segment.
	active, err := os.ReadFile(filepath.Join(dir, activeFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.jsonl"), active, 0o644); err != nil {
		t.Fatal(err)
	}
	// …with a stale rewrite payload sitting next to it.
	if err := os.WriteFile(filepath.Join(dir, rewritePayload), active, 0o644); err != nil {
		t.Fatal(err)
	}
	err = func() error { _, err := r.Load(t.Context(), "c1"); return err }()
	if err == nil || !strings.Contains(err.Error(), "uncommitted sealed segment") ||
		strings.Contains(err.Error(), "--finish-rewrite") {
		t.Fatalf("debris payload must not rename the orphan window: %v", err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "not an interrupted edit") {
		t.Fatalf("repair must refuse a foreign mismatch: %v", err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate); err != nil {
		t.Fatalf("the right verb must still work: %v", err)
	}
	// The payload stays debris: readers see the healthy room, and the
	// verb still refuses to publish it.
	if _, err := r.Load(t.Context(), "c1"); err != nil {
		t.Fatal(err)
	}
	if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "already consistent") {
		t.Fatalf("debris payload must stay unpublished: %v", err)
	}
}

// RepairRewrite refuses states that are not the interrupted-edit window —
// the way RepairOrphan refuses a mismatched archive.
func TestRepairRewriteRefusesNonInterruptedStates(t *testing.T) {
	t.Run("healthy room", func(t *testing.T) {
		r := newRepo(t).(*ConversationRepository)
		if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
			t.Fatal(err)
		}
		appendMsgs(t, r, "c1", "u1")
		if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "nothing to repair") {
			t.Fatalf("repair on a healthy room = %v", err)
		}
	})
	t.Run("compact orphan has no payload", func(t *testing.T) {
		r := newRepo(t).(*ConversationRepository)
		if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
			t.Fatal(err)
		}
		appendMsgs(t, r, "c1", "u1")
		dir := r.convDir("c1")
		active, err := os.ReadFile(filepath.Join(dir, activeFile))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), active, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := r.RepairRewrite(t.Context(), "c1"); err == nil ||
			!strings.Contains(err.Error(), "not an interrupted edit") ||
			!strings.Contains(err.Error(), "backup") {
			t.Fatalf("payload-less mismatch needs manual-recovery guidance: %v", err)
		}
	})
	t.Run("consistent archive with stray payload", func(t *testing.T) {
		r := newRepo(t).(*ConversationRepository)
		if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
			t.Fatal(err)
		}
		appendMsgs(t, r, "c1", "u1")
		dir := r.convDir("c1")
		activeBefore, err := os.ReadFile(filepath.Join(dir, activeFile))
		if err != nil {
			t.Fatal(err)
		}
		payload := filepath.Join(dir, rewritePayload)
		if err := os.WriteFile(payload, append([]byte(nil), activeBefore...), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "already consistent") {
			t.Fatalf("repair must refuse a consistent archive: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, activeFile))
		if err != nil || string(got) != string(activeBefore) {
			t.Fatalf("refused repair republished a stray payload: %v", err)
		}
		if _, err := os.Stat(payload); err != nil {
			t.Fatalf("refused repair deleted the payload: %v", err)
		}
	})
	// A payload beside a mismatch the edit never leaves — a segment gone
	// with nothing parked and the payload not yet the live tail — is
	// debris, not a recovery source.
	t.Run("payload beside a foreign mismatch", func(t *testing.T) {
		r := newRepo(t).(*ConversationRepository)
		if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
			t.Fatal(err)
		}
		appendMsgs(t, r, "c1", "u1", "u2")
		sum, _ := entity.NewMessage(enum.RoleUser, "S1")
		if err := compactAt(t, r, "c1", sum, 1); err != nil {
			t.Fatal(err)
		}
		dir := r.convDir("c1")
		if err := os.Remove(filepath.Join(dir, "0.jsonl")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rewritePayload), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "not an interrupted edit") {
			t.Fatalf("foreign mismatch must refuse roll-forward: %v", err)
		}
	})
}

// A payload that is not a complete decodable stream is refused before
// anything is published — the parked segment stays as the recovery source.
func TestRepairRewriteRejectsUndecodablePayload(t *testing.T) {
	for name, payload := range map[string]string{
		"no trailing newline": `{"type":"message"}`,
		"corrupt line":        "not-json\n",
		"unknown line type":   "{\"type\":\"bogus\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRepo(t).(*ConversationRepository)
			if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
				t.Fatal(err)
			}
			appendMsgs(t, r, "c1", "u1", "u2")
			sum, _ := entity.NewMessage(enum.RoleUser, "S1")
			if err := compactAt(t, r, "c1", sum, 1); err != nil {
				t.Fatal(err)
			}
			dir := r.convDir("c1")
			seg := filepath.Join(dir, "0.jsonl")
			if err := os.Rename(seg, seg+rewriteParkSuffix); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, rewritePayload), []byte(payload), 0o644); err != nil {
				t.Fatal(err)
			}
			activeBefore, err := os.ReadFile(filepath.Join(dir, activeFile))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.RepairRewrite(t.Context(), "c1"); err == nil || !strings.Contains(err.Error(), "payload") {
				t.Fatalf("undecodable payload must refuse: %v", err)
			}
			if _, err := os.Stat(seg + rewriteParkSuffix); err != nil {
				t.Fatalf("refused repair touched the parked segment: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, activeFile))
			if err != nil || string(got) != string(activeBefore) {
				t.Fatalf("refused repair published a bad payload: %v", err)
			}
		})
	}
}

func TestRepairOrphanCommittedActiveRewrite(t *testing.T) {
	r := newRepo(t).(*ConversationRepository)
	if err := r.Save(t.Context(), newConv(t, "c1")); err != nil {
		t.Fatal(err)
	}
	appendMsgs(t, r, "c1", "one", "two", "three")
	dir := r.convDir("c1")
	unblock := blockAtomicWrites(t, metaFile)
	summary, _ := entity.NewMessage(enum.RoleUser, "summary")
	if err := compactAt(t, r, "c1", summary, 2); err == nil {
		t.Fatal("expected metadata failure")
	}
	unblock()
	segment := filepath.Join(dir, "0.jsonl")
	before, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate); err == nil {
		t.Fatal("must not discard unique history")
	}
	if err := r.RepairOrphan(t.Context(), "c1", drepo.RepairCommit); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(segment)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("sealed history changed: %v", err)
	}
	info, err := r.Info(t.Context(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 1 || info.MessageCount != 4 || info.ActiveCount != 2 {
		t.Fatalf("recovered counters: %+v", info)
	}
	if got := len(msgIDs(t, r, "c1")); got != 4 {
		t.Fatalf("messages = %d", got)
	}
}

// RepairOrphan requires canonical segment names: readers accept a
// numeric-but-renamed segment ("01.jsonl" reads as segment 1), but the
// repair tool never guesses at a rename — it reports the canonical name
// as missing and asks for manual reconciliation.
func TestRepairOrphanRequiresCanonicalSegmentNames(t *testing.T) {
	repo := NewConversationRepository(t.TempDir())
	dir := repo.convDir("c1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Committed archive: 0.jsonl + 01.jsonl (numeric segment 1, odd name)
	// plus an uncommitted 2.jsonl orphan.
	for _, name := range []string{"0.jsonl", "01.jsonl", "2.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meta := metaRecord{ID: "c1", Environment: "local", SealedSegments: 2}
	if err := repo.writeMeta("c1", meta); err != nil {
		t.Fatal(err)
	}
	// The archive IS broken (an uncommitted third segment exists) — that
	// is the orphan state the repair tool exists for.
	if _, err := repo.Info(t.Context(), "c1"); err == nil {
		t.Fatal("archive should not validate with an uncommitted segment")
	}
	err := repo.RepairOrphan(t.Context(), "c1", drepo.RepairDiscardDuplicate)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	// The diagnostic names canonical segment 1 missing although 01.jsonl
	// is present and reads numerically — repair enforces name equality
	// where readers enforce numeric ordering.
	if want := "sealed segment 1 missing"; !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %q diagnostic for a numeric-but-renamed segment, got %v", want, err)
	}
}
