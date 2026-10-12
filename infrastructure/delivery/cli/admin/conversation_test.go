package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

func TestConversationRepairCommandUsesConfiguredStore(t *testing.T) {
	storage := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"storage":{"dir":%q}}`, storage)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	repo := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conv, err := aggregate.NewConversation("c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	msg, _ := entity.NewMessage(enum.RoleUser, "one")
	if _, err := repo.AppendMessage(t.Context(), "c1", msg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(storage, "conversations", "c1")
	active, err := os.ReadFile(filepath.Join(dir, "active.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), active, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := ConversationRepair([]string{"c1", "--discard-duplicate"}, &out, &errs); code != 0 {
		t.Fatalf("repair exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out.String(), "repaired conversation c1") {
		t.Fatalf("repair output: %q", out.String())
	}
	if _, err := repo.Info(t.Context(), "c1"); err != nil {
		t.Fatalf("room remains inaccessible: %v", err)
	}
}

func TestConversationRepairKeepsStorageDirWhenOtherConfigFieldIsInvalid(t *testing.T) {
	t.Chdir(t.TempDir())
	storage := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"service":{"idle_timeout":"not-a-duration"},"storage":{"dir":%q}}`, storage)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	configured := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conv, err := aggregate.NewConversation("c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := configured.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	msg, _ := entity.NewMessage(enum.RoleUser, "one")
	if _, err := configured.AppendMessage(t.Context(), "c1", msg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(storage, "conversations", "c1")
	active, err := os.ReadFile(filepath.Join(dir, "active.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0.jsonl"), active, 0o644); err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join("data", "conversations", "c1")
	if err := os.MkdirAll(wrong, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"meta.json", "active.jsonl", "0.jsonl"} {
		source := filepath.Join(dir, name)
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wrong, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out, errs bytes.Buffer
	if code := ConversationRepair([]string{"c1", "--discard-duplicate"}, &out, &errs); code != 0 {
		t.Fatalf("repair exit %d: %s", code, errs.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "0.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("configured store orphan remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wrong, "0.jsonl")); err != nil {
		t.Fatalf("default store was modified: %v", err)
	}
}

func TestLoadConfigLenientRejectsUnreliableStoragePath(t *testing.T) {
	for _, raw := range []string{
		`{"storage":{"dir":42},"service":{"idle_timeout":"not-a-duration"}}`,
		`{"storage":{"dir":"/tmp/unfinished"}`, // intentionally truncated JSON
	} {
		t.Run(raw, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CONFIG_PATH", configPath)
			if _, _, err := LoadConfig(false); err == nil {
				t.Fatal("invalid storage location must not silently fall back to ./data")
			}
		})
	}
}

// The interrupted sealed-edit window built on disk the way the store
// leaves it: the merged stream staged under the non-archive payload name,
// the numbered segment parked under its rewrite-old name, and the metadata
// counter still counting the parked era. The operator verb rolls the
// payload forward — publish over the live tail, sweep the leftovers,
// recount — and refuses to run twice.
func TestConversationRepairFinishRewriteRollsForwardInterruptedEdit(t *testing.T) {
	storage := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"storage":{"dir":%q}}`, storage)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", configPath)
	repo := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conv, err := aggregate.NewConversation("c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"u1", "u2", "u3"} {
		msg, _ := entity.NewMessage(enum.RoleUser, content)
		if _, err := repo.AppendMessage(t.Context(), "c1", msg); err != nil {
			t.Fatal(err)
		}
	}
	summary, _ := entity.NewMessage(enum.RoleUser, "S1")
	if err := repo.Compact(t.Context(), "c1", summary, 2); err != nil { // 0.jsonl=[u1,u2], active=[S1,u3]
		t.Fatal(err)
	}
	dir := filepath.Join(storage, "conversations", "c1")
	segment, err := os.ReadFile(filepath.Join(dir, "0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(segment), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("sealed segment lines = %d, want 2", len(lines))
	}
	// The edit targeted the era's last message: the merged stream is the
	// sealed segment verbatim with that line's content patched in place,
	// and the discarded tail leaves no patch lines behind.
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &record); err != nil {
		t.Fatal(err)
	}
	data, ok := record["data"].(map[string]any)
	if !ok {
		t.Fatalf("sealed line carries no message data: %s", lines[1])
	}
	data["content"] = "u2*"
	edited, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active.jsonl.rewrite"), []byte(lines[0]+"\n"+string(edited)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "0.jsonl"), filepath.Join(dir, "0.jsonl.rewrite-old")); err != nil {
		t.Fatal(err)
	}

	var out, errs bytes.Buffer
	if code := ConversationRepair([]string{"c1", "--finish-rewrite"}, &out, &errs); code != 0 {
		t.Fatalf("finish-rewrite exit %d: %s", code, errs.String())
	}
	if !strings.Contains(out.String(), "repaired conversation c1 (finish-rewrite)") {
		t.Fatalf("repair output: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "active.jsonl.rewrite")); !os.IsNotExist(err) {
		t.Fatalf("payload survived roll-forward: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "0.jsonl.rewrite-old")); !os.IsNotExist(err) {
		t.Fatalf("parked segment survived roll-forward: %v", err)
	}
	history, err := repo.LoadAt(t.Context(), "c1", 0, 0)
	if err != nil {
		t.Fatalf("room unreadable after roll-forward: %v", err)
	}
	if len(history) != 2 || history[0].Content() != "u1" || history[1].Content() != "u2*" {
		t.Fatalf("repaired history = %+v", history)
	}
	out.Reset()
	errs.Reset()
	if code := ConversationRepair([]string{"c1", "--finish-rewrite"}, &out, &errs); code == 0 {
		t.Fatalf("second finish-rewrite must refuse: %s", out.String())
	}
}
