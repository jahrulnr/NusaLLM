package job

import (
	"context"
	"fmt"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// runTurn is the agent loop shared by send and edit: provider call →
// persist → execute pending tool calls → provider call — until the model
// stops requesting tools or the step cap is hit.
func (d Deps) runTurn(ctx context.Context, convID string, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, svc dservice.TextModelService, onEvent event.TurnEventHandler) error {
	// Hand-built Deps may carry a zero or invalid config — normalize
	// clamps and tag defaults fill it.
	ccfg := d.Config
	ccfg.Normalize()
	didCompact := false
	for step := 0; step < ccfg.MaxTurnStep; step++ {
		if err := ctx.Err(); err != nil {
			return apperror.New(408, "turn canceled", err)
		}
		if !didCompact {
			// One compaction per turn: a fat tail that still overflows
			// after folding is the provider's call — refiring every step
			// would summarize the previous checkpoint at full cost.
			shrank, err := d.maybeCompact(ctx, convID, conv, gw, model, svc, ccfg.CompactBudgetPct, ccfg.CompactKeepTail)
			if err != nil {
				return err
			}
			didCompact = shrank
		}
		before := len(conv.Messages())
		err := svc.Send(ctx, conv, gw, model, onEvent)
		if err != nil && apperror.As(err).StatusCode() == 413 && !didCompact && len(conv.Messages()) > ccfg.CompactKeepTail {
			// The provider says overflow even though our estimate
			// disagreed — believe the provider once: compact and retry.
			if err := d.compact(ctx, convID, conv, gw, model, svc, ccfg.CompactKeepTail); err != nil {
				return err
			}
			didCompact = true
			continue
		}
		if err != nil {
			// A cancel landing mid-body-read can surface as an upstream
			// error from the adapter — normalize before it misroutes.
			if ctx.Err() != nil {
				return apperror.New(408, "turn canceled", ctx.Err())
			}
			return err
		}
		// The port contract is one assistant append per call — verify it
		// instead of trusting conv.Last(), so a misbehaving adapter can't
		// duplicate the user line or silently drop output.
		added := conv.Messages()[before:]
		if len(added) == 0 {
			return apperror.New(500, "adapter produced no message", fmt.Errorf("service appended 0 messages"))
		}
		for _, m := range added {
			if err := d.Convs.AppendMessage(ctx, convID, m); err != nil {
				return err
			}
		}
		// Meta (usage counters) travels with each step — a crash mid-loop
		// leaves messages and accounting consistent.
		if err := d.Convs.Save(ctx, conv); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			// The reply is already persisted — paid for, keep it — but a
			// canceled caller must not read success.
			return apperror.New(408, "turn canceled", err)
		}

		msgs := conv.Messages()
		last := &msgs[len(msgs)-1]
		pending := pendingCalls(*last)
		if len(pending) == 0 {
			return nil // the model is done
		}
		if step == ccfg.MaxTurnStep-1 {
			// The unanswered pending calls stay in history — a retry
			// replays them, which is honest: the model did ask.
			return apperror.New(500, "tool loop limit reached", fmt.Errorf("%d steps without a final answer", ccfg.MaxTurnStep))
		}
		for _, call := range pending {
			if err := ctx.Err(); err != nil {
				return apperror.New(408, "turn canceled", err)
			}
			if err := d.execTool(ctx, convID, onEvent, conv, last, call); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveRoute picks the effective route/model — job overrides win, then
// the stored binding — and resolves them to live objects: the Gateway
// aggregate, the catalog Model, and the wire adapter for the style.
func (d Deps) resolveRoute(ctx context.Context, convID string, conv *aggregate.Conversation, route, modelID string) (
	gw *aggregate.Gateway, model entity.Model, svc dservice.TextModelService, err error,
) {
	if route == "" {
		route = conv.Route()
	}
	if route == "" {
		return nil, model, nil, apperror.New(400, "no route bound", fmt.Errorf("conversation %s has no route", convID))
	}
	if modelID == "" {
		modelID = conv.ModelID()
	}
	if modelID == "" {
		return nil, model, nil, apperror.New(400, "no model bound", fmt.Errorf("conversation %s has no model", convID))
	}
	gw, err = d.Gateways.Load(ctx, route)
	if err != nil {
		return nil, model, nil, err // 404 = dangling route — the caller picks another
	}
	m, ok := gw.Model(modelID)
	if !ok {
		return nil, model, nil, apperror.NewNotFoundError("model "+modelID, fmt.Errorf("route %s catalog has no such model", route))
	}
	svc, ok = d.Services[gw.Style()]
	if !ok {
		// The gateway config is fine; the server simply wired no adapter
		// for this style — that's a deployment defect, not a user error.
		return nil, model, nil, apperror.New(500, "no adapter for gateway style", fmt.Errorf("style %s unregistered", gw.Style()))
	}
	return gw, m, svc, nil
}

// pendingCalls returns the model-originated calls still awaiting output on
// a message — injection entries and already-answered calls are skipped.
func pendingCalls(m entity.Message) []entity.ToolCall {
	var out []entity.ToolCall
	for _, tc := range m.ToolCalls() {
		if tc.Type() == enum.TooltypeOrigin && tc.Output() == "" {
			out = append(out, tc)
		}
	}
	return out
}

// execTool resolves and runs one call, then records the result twice: on
// the in-memory message (so the next provider call sees it) and as a patch
// record in the repository (so a reload restores it). Executor failures
// become tool output — the model deserves honest results — while context
// cancellation aborts the whole turn.
func (d Deps) execTool(ctx context.Context, convID string, onEvent event.TurnEventHandler, conv *aggregate.Conversation, msg *entity.Message, call entity.ToolCall) error {
	out := ""
	permitted := false
	for _, s := range conv.Tools() {
		if s.Name() == call.Name() && s.CallableByAgent() {
			permitted = true
			break
		}
	}
	// Live chunks flow to the frontend scoped by call id — they are a
	// preview, never history; only the Execute return lands in output.
	emit := func(chunk string) {
		if onEvent != nil {
			onEvent(event.NewToolOutputEvent(call, chunk))
		}
	}
	switch tool, registered := d.Tools[call.Name()]; {
	case !permitted:
		out = "error: tool not permitted"
	case !registered:
		out = "error: unknown tool " + call.Name()
	default:
		res, err := tool.Execute(ctx, call, emit)
		switch {
		case err == nil:
			out = res
		case ctx.Err() != nil:
			return apperror.New(408, "turn canceled", ctx.Err())
		default:
			out = "error: " + err.Error()
		}
	}
	if err := msg.SetToolOutput(call.ID(), out); err != nil {
		// Over-budget output records the failure instead of aborting the turn.
		if err := msg.SetToolOutput(call.ID(), "error: "+err.Error()); err != nil {
			return apperror.New(500, "tool output rejected", err)
		}
	}
	for _, tc := range msg.ToolCalls() {
		if tc.ID() == call.ID() {
			return d.Convs.UpdateToolCallOutput(ctx, convID, call.ID(), tc.Output())
		}
	}
	return apperror.New(500, "tool output lost", fmt.Errorf("call %s vanished after update", call.ID()))
}
