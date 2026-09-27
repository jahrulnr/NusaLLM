package aggregate

import (
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

func mustConv(t *testing.T) *Conversation {
	t.Helper()
	c, err := NewConversation("conv-1")
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	return c
}

func TestNewConversation(t *testing.T) {
	if _, err := NewConversation("../bad"); err == nil {
		t.Fatal("expected error for path-element id")
	}
	minted, err := NewConversation("") // empty mints a fresh id
	if err != nil || minted.ID() == "" {
		t.Fatalf("minted id: %q err=%v", minted.ID(), err)
	}
	c := mustConv(t)
	if c.ID() != "conv-1" || c.Last() != nil {
		t.Fatalf("unexpected conversation: %+v", c)
	}
}

func TestConversationAppendLast(t *testing.T) {
	c := mustConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	c.Append(m)
	if c.Last() == nil || c.Last().Content() != "hi" {
		t.Fatal("Last did not return appended message")
	}
}

func TestConversationEstimatedTokens(t *testing.T) {
	c := mustConv(t)
	c.SetSystemPrompt(strings.Repeat("s", 400)) // ~100 tokens

	m, _ := entity.NewMessage(enum.RoleUser, strings.Repeat("x", 400))
	m.SetOptionalContent("gemini.thought_signature", strings.Repeat("s", 40))
	a, _ := valueobject.NewAttachment("https://x/i.png", "image/png")
	m.AddAttachment(a)
	c.Append(m)

	// Tool-produced media bills like a message attachment — a media-heavy
	// history must still trip the compaction budget.
	am, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("c1", "read_media", strings.Repeat("i", 40), enum.TooltypeOrigin)
	tc.SetStoredOutput(strings.Repeat("o", 40))
	tc.SetOutputAttachments([]valueobject.Attachment{a})
	am.AddToolCall(tc)
	c.Append(am)

	// (400 + 400 + 40 + 40 + 40)/4 + 2×1000 flat attachments = 2230
	if got := c.EstimatedTokens(); got != 2230 {
		t.Fatalf("EstimatedTokens = %d, want 2230", got)
	}
}

func TestConversationNeedCompaction(t *testing.T) {
	c := mustConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, strings.Repeat("x", 400))
	c.Append(m) // 100 tokens

	if c.NeedCompaction(0) {
		t.Fatal("zero budget must fail open")
	}
	if !c.NeedCompaction(100) {
		t.Fatal("estimate at budget must trigger")
	}
	if c.NeedCompaction(101) {
		t.Fatal("estimate below budget must not trigger")
	}
}

func TestConversationCompact(t *testing.T) {
	c := mustConv(t)
	for _, txt := range []string{"m1", "m2", "m3", "m4"} {
		m, _ := entity.NewMessage(enum.RoleUser, txt)
		c.Append(m)
	}
	sum, _ := entity.NewMessage(enum.RoleUser, "SUMMARY")
	sum.SetUserType(enum.LLMUserTypeSystem)
	c.Compact(sum, 2)

	msgs := c.Messages()
	if len(msgs) != 3 || msgs[0].Content() != "SUMMARY" ||
		msgs[1].Content() != "m3" || msgs[2].Content() != "m4" {
		t.Fatalf("compact produced %+v", msgs)
	}

	c.Compact(sum, -5)
	if got := c.Messages(); len(got) != 4 || got[0].Content() != "SUMMARY" {
		t.Fatalf("negative dropBefore must clamp to 0, got %+v", got)
	}
	c.Compact(sum, 99)
	if got := c.Messages(); len(got) != 1 || got[0].Content() != "SUMMARY" {
		t.Fatalf("overflow dropBefore must clamp to len, got %+v", got)
	}
}

func TestConversationAddUsageAccumulates(t *testing.T) {
	c := mustConv(t)
	u1, _ := valueobject.NewUsage(100, 0, 0, 50)
	u2, _ := valueobject.NewUsage(200, 10, 0, 60)
	c.AddUsage(u1)
	c.AddUsage(u2)
	if c.Usage().Total() != 420 {
		t.Fatalf("accumulated total = %d, want 420", c.Usage().Total())
	}
}

func BenchmarkConversationEstimatedTokens(b *testing.B) {
	c, _ := NewConversation("conv")
	for i := 0; i < 200; i++ {
		m, _ := entity.NewMessage(enum.RoleUser, strings.Repeat("x", 2000))
		tc, _ := entity.NewToolCall("id", "name", strings.Repeat("i", 500), enum.TooltypeOrigin)
		_ = tc.SetOutput(strings.Repeat("o", 3000))
		m.AddToolCall(tc)
		c.Append(m)
	}
	b.ResetTimer()
	for b.Loop() {
		_ = c.EstimatedTokens()
	}
}
