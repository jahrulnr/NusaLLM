package job

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// SummaryToolName is the harness contract compaction.md mandates: the model
// answers only through this tool's `text` field.
const SummaryToolName = "summary"

// SummaryTool is the compaction checkpoint contract — unlike ordinary
// tools the call input IS the result, so Execute just extracts it. The
// compaction job owns the dialogue; this type only carries the schema
// and the input contract. It is part of the use case, not the
// environment's agent-facing catalog — never register it in a driver's
// Tools map.
type SummaryTool struct {
	schema valueobject.ToolSchema
}

func NewSummaryTool() *SummaryTool {
	schema, err := valueobject.NewToolSchema(
		SummaryToolName,
		"Write the compaction handoff checkpoint in the text field.",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"The complete handover checkpoint"}}, "required":["text"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		panic("job: summary schema: " + err.Error()) // a constant malformed at init is a bug, not a runtime condition
	}
	return &SummaryTool{schema: schema}
}

func (tool *SummaryTool) Schema() valueobject.ToolSchema {
	return tool.schema
}

func (tool *SummaryTool) Execute(_ context.Context, call entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(call.Input()), &input); err != nil || strings.TrimSpace(input.Text) == "" {
		return dservice.ToolResult{}, fmt.Errorf("the text field must hold the checkpoint")
	}
	return dservice.ToolResult{Text: input.Text}, nil
}
