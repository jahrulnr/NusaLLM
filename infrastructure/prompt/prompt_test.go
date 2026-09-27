package prompt

import (
	"strings"
	"testing"
)

func TestConversationPromptKeepsPrivateReasoningOutOfUserText(t *testing.T) {
	text := SystemPrompt("conversation")

	if !strings.Contains(text, "Keep private chain-of-thought and internal deliberation out of user-visible text") {
		t.Fatal("conversation prompt must keep private reasoning out of user-visible text")
	}
	if strings.Contains(text, "streaming thinking & responses") {
		t.Fatal("conversation prompt must not ask the model to stream private thinking")
	}
}
