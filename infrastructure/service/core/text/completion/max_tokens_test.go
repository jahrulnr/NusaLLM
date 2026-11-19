package textcompletion

import (
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	textwire "github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// The optional max-completion field is still bounded by the window the
// prompt already occupies. A stored explicit cap equal to the raw window
// (write-path validation allows it) would otherwise send prompt+reply
// past the model's context.
func TestExplicitMaxCompletionRespectsPromptWindow(t *testing.T) {
	conv, _ := aggregate.NewConversation("c")
	msg, _ := entity.NewMessage(enum.RoleUser, strings.Repeat("x", 4000))
	conv.Append(msg)
	model, _ := entity.NewModel("m", "")
	model.SetContextWindow(4096)
	max := 4096
	model.SetMaxOutputTokens(&max)

	body, err := encodeRequest(conv, model, false, textwire.MediaRoot{}, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if body.MaxTokens == nil {
		t.Fatal("explicit maxOutputTokens disappeared")
	}
	if *body.MaxTokens+conv.EstimatedTokens() > 4096 {
		t.Fatalf("max_completion_tokens=%d + prompt≈%d exceeds contextWindow 4096",
			*body.MaxTokens, conv.EstimatedTokens())
	}
}
