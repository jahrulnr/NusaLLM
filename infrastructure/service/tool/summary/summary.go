// Package summary provides the compaction checkpoint contract tool.
// The compaction job builds a scratch conversation whose whole catalog
// is this schema and reads the checkpoint back out of the model's call —
// the call input IS the result, so Execute only extracts it. The tool is
// never registered in a driver's Tools map: it is offered ad hoc inside
// the scratch dialogue, not part of a room's agent-facing surface.
package summary

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

// ToolName is the harness contract compaction.md mandates: the model
// answers only through this tool's `text` field.
const ToolName = "summary"

// SummaryTool carries the checkpoint schema and input contract; the
// compaction job owns the dialogue around it.
type SummaryTool struct {
	schema valueobject.ToolSchema
}

func NewSummaryTool() (dservice.Tool, error) {
	schema, err := valueobject.NewToolSchema(
		ToolName,
		"Write the compaction handoff checkpoint in the text field.",
		json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"The complete handover checkpoint"}}, "required":["text"]}`),
		true,
		enum.ToolPermissionAgent,
	)
	if err != nil {
		return nil, err
	}
	return &SummaryTool{schema: schema}, nil
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
