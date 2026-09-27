package appconversation

import (
	"context"

	"github.com/jahrulnr/llm-base/infrastructure/prompt"
)

// ContextFooterStats is the estimated live input size and the model's input
// allowance for the REPL footer (~estimate/total), aligned with compaction.
type ContextFooterStats struct {
	Estimate int
	Total    int
}

// ContextFooterStats loads the conversation's estimated input tokens and
// resolves the input allowance from the bound route/model.
func (app *ConversationApp) ContextFooterStats(ctx context.Context, convID, route, modelID string) (ContextFooterStats, error) {
	var stats ContextFooterStats
	if convID == "" {
		return stats, nil
	}
	conv, err := app.deps.Convs.Load(ctx, convID)
	if err != nil {
		return stats, err
	}
	// Mirror send/runTurn: system prompt and tool catalog are code applied
	// every request, not restored from the conversation record.
	if conv.SystemPrompt() == "" {
		conv.SetSystemPrompt(prompt.SystemPrompt("conversation"))
	}
	conv.SetTools(app.ToolSchemas())
	stats.Estimate = conv.EstimatedTokens()
	if route == "" {
		route = conv.Route()
	}
	if modelID == "" {
		modelID = conv.ModelID()
	}
	if route == "" || modelID == "" {
		return stats, nil
	}
	gw, err := app.deps.Gateways.Load(ctx, route)
	if err != nil {
		return stats, nil
	}
	model, ok := gw.Model(modelID)
	if !ok {
		return stats, nil
	}
	stats.Total = model.CompactionInputAllowance()
	return stats, nil
}
