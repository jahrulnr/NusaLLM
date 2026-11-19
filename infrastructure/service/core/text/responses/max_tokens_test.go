package textresponses

import (
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	textwire "github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// max_output_tokens shares the context window with the prompt. An
// explicit value equal to the raw window must be reduced to the room the
// prompt leaves, or the request asks the provider for an impossible
// prompt+completion total.
func TestExplicitMaxOutputRespectsPromptWindow(t *testing.T) {
	conv, _ := aggregate.NewConversation("c")
	msg, _ := entity.NewMessage(enum.RoleUser, strings.Repeat("x", 4000))
	conv.Append(msg)
	model, _ := entity.NewModel("m", "")
	model.SetContextWindow(4096)
	max := 4096
	model.SetMaxOutputTokens(&max)

	body, err := encodeRequest(conv, model, false, textwire.MediaRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if body.MaxOutputTokens == nil {
		t.Fatal("explicit maxOutputTokens disappeared")
	}
	if *body.MaxOutputTokens+conv.EstimatedTokens() > 4096 {
		t.Fatalf("max_output_tokens=%d + prompt≈%d exceeds contextWindow 4096",
			*body.MaxOutputTokens, conv.EstimatedTokens())
	}
}
