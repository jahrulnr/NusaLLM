package job

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

// snapTool produces a fixed number of image attachments per call — the
// screenshot loop that walks a serving into its per-request image cap.
type snapTool struct{ images int }

func (t snapTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema("snap", "take a screenshot", nil, true, enum.ToolPermissionAgent)
	return s
}

func (t snapTool) Execute(_ context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	var atts []valueobject.Attachment
	for i := 0; i < t.images; i++ {
		a, err := valueobject.NewAttachment(fmt.Sprintf("file://shot-%d.png", i), "image/png")
		if err != nil {
			return dservice.ToolResult{}, err
		}
		atts = append(atts, a)
	}
	return dservice.ToolResult{Text: "screenshot taken", Attachments: atts}, nil
}

// snapSvc asks for the snap tool on its first live Send and answers
// finally on the second; a Send aimed at any other conversation is the
// compaction scratch dialogue, which gets a plain-text handoff.
type snapSvc struct {
	live   *aggregate.Conversation
	calls  int
	folded bool
}

func (s *snapSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	if s.live != nil && conv != s.live {
		s.folded = true
		m, _ := entity.NewMessage(enum.RoleAssistant, "handoff: earlier screenshots summarized")
		conv.Append(m)
		return nil
	}
	s.calls++
	m, _ := entity.NewMessage(enum.RoleAssistant, "working")
	if s.calls == 1 {
		tc, err := entity.NewToolCall("snap1", "snap", `{}`, enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
	}
	conv.Append(m)
	return nil
}

func snapRoom(t *testing.T, d Deps, userImages, extra int) *aggregate.Conversation {
	t.Helper()
	conv, _ := aggregate.NewConversation("room")
	user, _ := entity.NewMessage(enum.RoleUser, "check the app")
	for i := 0; i < userImages; i++ {
		a, _ := valueobject.NewAttachment(fmt.Sprintf("file://seed-%d.png", i), "image/png")
		user.AddAttachment(a)
	}
	msgs := []entity.Message{user}
	for i := 0; i < extra; i++ {
		role := enum.RoleUser
		if i%2 == 1 {
			role = enum.RoleAssistant
		}
		m, _ := entity.NewMessage(role, fmt.Sprintf("earlier turn %d", i))
		msgs = append(msgs, m)
	}
	for _, m := range msgs {
		conv.Append(m)
	}
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if _, err := d.Convs.AppendMessage(t.Context(), "room", m); err != nil {
			t.Fatal(err)
		}
	}
	return conv
}

func mediaDeps(t *testing.T, keepTail int) Deps {
	t.Helper()
	return Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 8, CompactKeepTail: keepTail},
	}
}

// The recorded tool call for "snap1" inside conv's live history.
func snapCall(t *testing.T, conv *aggregate.Conversation) entity.ToolCall {
	t.Helper()
	for _, m := range conv.Messages() {
		for _, tc := range m.ToolCalls() {
			if tc.Name() == "snap" {
				return tc
			}
		}
	}
	t.Fatal("snap call not found in live history")
	return entity.ToolCall{}
}

// Under the cap everything lands; at/over it the excess images are
// withheld with an explanation, while the recorded history — earlier
// attachments included — stays untouched.
func TestMediaGateLimitsNewAttachments(t *testing.T) {
	for _, tc := range []struct {
		name       string
		userImages int
		maxImages  int
		toolImages int
		wantAtts   int
		wantNote   string
	}{
		{name: "fail open when undeclared", userImages: 1, maxImages: 0, toolImages: 2, wantAtts: 2},
		{name: "under limit attaches all", userImages: 1, maxImages: 8, toolImages: 2, wantAtts: 2},
		{name: "partial room keeps what fits", userImages: 1, maxImages: 2, toolImages: 2, wantAtts: 1,
			wantNote: "1 image output(s) withheld"},
		{name: "at limit withholds all", userImages: 8, maxImages: 8, toolImages: 1, wantAtts: 0,
			wantNote: "1 image output(s) withheld"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mediaDeps(t, 100) // keepTail 100 → the fold can't fire
			conv := snapRoom(t, d, tc.userImages, 0)
			model := entity.Model{}
			model.SetMaxImages(tc.maxImages)
			svc := &snapSvc{live: conv}
			driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"snap": snapTool{images: tc.toolImages}}}
			if err := d.runTurn(t.Context(), "room", conv, driver, nil, model, svc, nil, nil); err != nil {
				t.Fatal(err)
			}
			if svc.folded {
				t.Fatal("fold must not fire when the tail cannot compact")
			}
			call := snapCall(t, conv)
			if got := len(call.OutputAttachments()); got != tc.wantAtts {
				t.Fatalf("stored attachments = %d, want %d", got, tc.wantAtts)
			}
			if tc.wantNote == "" {
				if strings.Contains(call.Output(), "withheld") {
					t.Fatalf("output should not carry a withhold note: %q", call.Output())
				}
			} else if !strings.Contains(call.Output(), tc.wantNote) ||
				!strings.Contains(call.Output(), fmt.Sprintf("at most %d images", tc.maxImages)) {
				t.Fatalf("output = %q, want note %q citing the limit", call.Output(), tc.wantNote)
			}
			// History is never rewritten: the user's seeded images are
			// still attached — the gate only withholds NEW tool output.
			if got := conv.MediaCount(enum.ModalityImage); got != tc.userImages+tc.wantAtts {
				t.Fatalf("live image count = %d, want %d", got, tc.userImages+tc.wantAtts)
			}
		})
	}
}

// When the withheld images mean the replayed history is already over the
// serving's cap, the same fold that serves the token budget runs and the
// sealed screenshots stop counting.
func TestMediaGateFoldOnLimit(t *testing.T) {
	d := mediaDeps(t, 0)
	conv := snapRoom(t, d, 8, 6) // past the default keepTail so the fold can fire
	model := entity.Model{}
	model.SetMaxImages(8)
	svc := &snapSvc{live: conv}
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"snap": snapTool{images: 1}}}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, model, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !svc.folded {
		t.Fatal("a withheld image over the cap should trigger the handoff fold")
	}
	if got := conv.MediaCount(enum.ModalityImage); got != 0 {
		t.Fatalf("post-fold live image count = %d, want 0", got)
	}
	first := conv.Messages()[0]
	if first.Role() != enum.RoleUser || first.Editable() || !strings.Contains(first.Content(), "handoff") {
		t.Fatalf("folded head = %q, want the compacted-continue checkpoint", first.Content())
	}
	// The fold persists — a reload replays the same under-limit history.
	reloaded, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.MediaCount(enum.ModalityImage); got != 0 {
		t.Fatalf("persisted image count = %d, want 0", got)
	}
}

// The step-top fold needs room to exist: when the tail is exactly
// keepTail at the check and the model then finishes in one more message,
// the turn-end fold picks up the deferred relief instead of leaving the
// next turn to replay the over-limit history.
func TestMediaGateFoldAtTurnEnd(t *testing.T) {
	d := mediaDeps(t, 0) // keepTail normalizes to the default 4
	conv := snapRoom(t, d, 4, 1)
	model := entity.Model{}
	model.SetMaxImages(4)
	svc := &snapSvc{live: conv}
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"snap": snapTool{images: 1}}}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, model, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !svc.folded {
		t.Fatal("an at-cap turn end should fold once the tail can compact")
	}
	if got := conv.MediaCount(enum.ModalityImage); got != 0 {
		t.Fatalf("post-fold live image count = %d, want 0", got)
	}
}
