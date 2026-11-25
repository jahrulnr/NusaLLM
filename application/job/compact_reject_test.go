package job

import (
	"context"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

// A room with no new history is a request the user can act on: one answer,
// one status. The manual job says 400 before it resolves a route or spends a
// provider call.
func TestCompactJobRejectsUnfoldableRoom(t *testing.T) {
	d := Deps{
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{CompactKeepTail: 4},
		SummaryTool: mustSummaryTool(t),
	}
	seedRoom(t, d.Convs, "room", "hi")

	j := &Compact{Deps: d}
	j.Result = make(chan error, 1)
	j.Bind("room")
	err := j.Execute(t.Context())
	ae := apperror.As(err)
	if ae.StatusCode() != 400 {
		t.Fatalf("status = %d (%v), want 400", ae.StatusCode(), err)
	}
	for _, want := range []string{"nothing to compact", "1 message(s)", "keep-tail 4"} {
		if !strings.Contains(ae.Error(), want) {
			t.Fatalf("error %q does not name the reason (%q)", ae.Error(), want)
		}
	}
	if got := <-j.Result; got == nil {
		t.Fatal("the caller got no reply")
	}
}

// countingService answers nothing and counts calls: the foldability verdict
// must land before the checkpoint dialogue touches the provider.
type countingService struct{ calls int }

func (s *countingService) Send(context.Context, *aggregate.Conversation, *aggregate.Gateway, entity.Model, event.TurnEventHandler) error {
	s.calls++
	return nil
}

func TestCompactGuardRefusesBeforeTheProviderCall(t *testing.T) {
	d := Deps{
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{CompactKeepTail: 4},
		SummaryTool: mustSummaryTool(t),
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &countingService{}

	err := d.compact(t.Context(), "room", conv, nil, entity.Model{}, svc, 4, nil)
	ae := apperror.As(err)
	if ae.StatusCode() != 400 {
		t.Fatalf("status = %d (%v), want 400", ae.StatusCode(), err)
	}
	if svc.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 — the fold was refused before the dialogue", svc.calls)
	}
}

var _ dservice.TextModelService = (*countingService)(nil)
