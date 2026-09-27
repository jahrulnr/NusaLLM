package toolsummary

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Name is the harness contract compaction.md mandates: the model answers
// only through this tool's `text` field.
const Name = "summary"

// SummaryTool is the compaction checkpoint contract — unlike ordinary
// tools the call input IS the result, so Execute just extracts it. The
// compaction job owns the dialogue; this type only carries the schema
// and the input contract.
type SummaryTool struct {
	schema valueobject.ToolSchema
}

func NewSummaryTool() *SummaryTool {
	schema, err := valueobject.NewToolSchema(
		Name,
		"Write the compaction handoff checkpoint in the text field.",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"The complete handover checkpoint"}}, "required":["text"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		panic("toolsummary: schema: " + err.Error()) // a constant malformed at init is a bug, not a runtime condition
	}
	return &SummaryTool{schema: schema}
}

func (tool *SummaryTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *SummaryTool) Execute(_ context.Context, call entity.ToolCall, _ service.ToolEmitter) (service.ToolResult, error) {
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil || strings.TrimSpace(input.Text) == "" {
		return service.ToolResult{}, fmt.Errorf("the text field must hold the checkpoint")
	}
	return service.ToolResult{Text: input.Text}, nil
}
