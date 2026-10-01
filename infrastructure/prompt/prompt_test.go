package prompt

import "testing"

// NEVER test prompts, they may change over time and break the tests.

// TestUsedPromptNamesExist pins only the names the code calls — prompt
// *content* is never asserted. A lookup miss now panics (a name is a
// compile-time asset), so this test is the tripwire for a typo or a
// renamed/deleted file.
func TestUsedPromptNamesExist(t *testing.T) {
	if SystemPrompt("conversation") == "" {
		t.Fatal("system prompt conversation is empty")
	}
	for _, name := range []string{"compaction", "compacted-continue"} {
		if UserPrompt(name) == "" {
			t.Fatalf("user prompt %s is empty", name)
		}
	}
}
