package appconversation

import (
	"context"
)

// ContextFooterStats is the estimated live input size and the bound model's
// total context window for the REPL footer (~estimate/total).
type ContextFooterStats struct {
	Estimate int
	Total    int
}

// ContextFooterStats loads the conversation's estimated input tokens and
// resolves the effective total context from the bound route/model.
func (app *ConversationApp) ContextFooterStats(ctx context.Context, convID, route, modelID string) (ContextFooterStats, error) {
	var stats ContextFooterStats
	if convID == "" {
		return stats, nil
	}
	conv, err := app.deps.Convs.Load(ctx, convID)
	if err != nil {
		return stats, err
	}
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
	stats.Total = model.ApplyUnknownContextDefaults().EffectiveContextWindow()
	return stats, nil
}
