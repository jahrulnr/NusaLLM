package job

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// runTurn is the agent loop shared by send and edit: provider call →
// persist → execute pending tool calls → provider call — until the model
// stops requesting tools or the step cap is hit.
func (d Deps) runTurn(ctx context.Context, convID string, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, svc dservice.TextModelService, onEvent event.TurnEventHandler) error {
	// Hand-built Deps may carry a zero or invalid config — normalize
	// clamps and tag defaults fill it.
	ccfg := d.Config
	// Directory-aware tools work under the room's bound workspace.
	ctx = dservice.WithWorkspaceDir(ctx, conv.Workspace())
	ccfg.Normalize()
	wireModel := model.ApplyUnknownContextDefaults()
	// The call surface is live config: a stored conversation can predate
	// the catalog, so apply the current set every turn — never trust what
	// the record hydrated. Without this the wire carries no tools and the
	// model invents names it was never offered.
	conv.SetTools(d.ToolSchemas())
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
		var parts []entity.MessagePart
		captureEvent := func(ev event.TurnEvent) {
			switch ev.Kind() {
			case enum.TurnEventDelta, enum.TurnEventReasoning, enum.TurnEventReasoningSummary:
				kind := entity.MessagePartText
				if ev.Kind() == enum.TurnEventReasoning || ev.Kind() == enum.TurnEventReasoningSummary {
					kind = entity.MessagePartReasoning
				}
				if text := ev.Delta(); text != "" {
					if len(parts) > 0 && parts[len(parts)-1].Kind == kind {
						parts[len(parts)-1].Text += text
					} else {
						parts = append(parts, entity.MessagePart{Kind: kind, Text: text})
					}
				}
			case enum.TurnEventToolCall:
				parts = append(parts, entity.MessagePart{
					Kind: entity.MessagePartToolCall, ToolCallID: ev.ToolCall().ID(),
				})
			}
			if onEvent != nil {
				onEvent(ev)
			}
		}
		err := svc.Send(ctx, conv, gw, wireModel, captureEvent)
		added := conv.Messages()[before:]
		// A canceled stream may already have appended the text and
		// reasoning that arrived, plus tool calls whose argument deltas
		// finished. Persist that partial message and mark those calls
		// interrupted instead of dropping the turn from history.
		canceled := err != nil && (ctx.Err() != nil || apperror.As(err).StatusCode() == 408)
		if err != nil && !canceled && apperror.As(err).StatusCode() == 413 && !didCompact && len(conv.Messages()) > ccfg.CompactKeepTail {
			// The provider says overflow even though our estimate
			// disagreed — believe the provider once: compact and retry.
			if err := d.compact(ctx, convID, conv, gw, wireModel, svc, ccfg.CompactKeepTail); err != nil {
				return err
			}
			didCompact = true
			continue
		}
		if err != nil && !canceled {
			// A cancel landing mid-body-read can surface as an upstream
			// error from the adapter — normalize before it misroutes.
			if ctx.Err() != nil {
				return apperror.New(408, "turn canceled", ctx.Err())
			}
			return err
		}
		if canceled && len(added) == 0 {
			if ctx.Err() != nil {
				return apperror.New(408, "turn canceled", ctx.Err())
			}
			return err
		}
		// The port contract is one assistant append per call — verify it
		// instead of trusting conv.Last(), so a misbehaving adapter can't
		// duplicate the user line or silently drop output.
		if len(added) == 0 {
			return apperror.New(500, "adapter produced no message", fmt.Errorf("service appended 0 messages"))
		}
		if len(parts) > 0 {
			conv.Last().SetParts(parts)
		}
		if canceled || ctx.Err() != nil {
			if err := stampInterrupted(conv.Last()); err != nil {
				return err
			}
		}
		for _, m := range added {
			if _, err := d.Convs.AppendMessage(ctx, convID, m); err != nil {
				return err
			}
		}
		// Meta (usage counters) travels with each step — a crash mid-loop
		// leaves messages and accounting consistent.
		if err := d.Convs.Save(ctx, conv); err != nil {
			return err
		}
		if canceled || ctx.Err() != nil {
			// The partial reply is already persisted. A canceled caller
			// must not read success or continue into tool execution.
			if ctx.Err() != nil {
				return apperror.New(408, "turn canceled", ctx.Err())
			}
			return err
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
				if err := d.persistInterrupted(ctx, convID, last); err != nil {
					return err
				}
				return apperror.New(408, "turn canceled", err)
			}
			if err := d.execTool(ctx, convID, onEvent, conv, last, call); err != nil {
				if ctx.Err() != nil {
					if stampErr := d.persistInterrupted(ctx, convID, last); stampErr != nil {
						return stampErr
					}
					return apperror.New(408, "turn canceled", ctx.Err())
				}
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

// ToolSchemas returns the registered catalog sorted by name — the
// deterministic order the provider sees every turn.
func (d Deps) ToolSchemas() []valueobject.ToolSchema {
	schemas := make([]valueobject.ToolSchema, 0, len(d.Tools))
	for _, tool := range d.Tools {
		schemas = append(schemas, tool.Schema())
	}
	slices.SortFunc(schemas, func(a, b valueobject.ToolSchema) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return schemas
}

// stampInterrupted records "interrupted by user" on origin calls that
// never received a result. Incomplete argument streams are not on the
// message — adapters omit them before appending.
func stampInterrupted(msg *entity.Message) error {
	if msg == nil {
		return nil
	}
	for _, tc := range msg.ToolCalls() {
		if tc.Type() != enum.TooltypeOrigin || tc.Output() != "" {
			continue
		}
		if err := msg.SetToolOutput(tc.ID(), "interrupted by user"); err != nil {
			return err
		}
	}
	return nil
}

// persistInterrupted writes the interrupted marker for calls that are
// already in history with an empty output — a stop during execution.
func (d Deps) persistInterrupted(ctx context.Context, convID string, msg *entity.Message) error {
	if msg == nil {
		return nil
	}
	for _, tc := range msg.ToolCalls() {
		if tc.Type() != enum.TooltypeOrigin || tc.Output() != "" {
			continue
		}
		if err := msg.SetToolOutput(tc.ID(), "interrupted by user"); err != nil {
			return err
		}
		updated := ""
		for _, cur := range msg.ToolCalls() {
			if cur.ID() == tc.ID() {
				updated = cur.Output()
				break
			}
		}
		if err := d.Convs.UpdateToolCallOutput(ctx, convID, tc.ID(), updated, nil); err != nil {
			return err
		}
	}
	return nil
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
	var outAtts []valueobject.Attachment
	permitted := false
	for _, s := range conv.Tools() {
		if s.Name() == call.Name() && s.CallableByAgent() {
			permitted = true
			break
		}
	}
	// Live chunks flow to the frontend scoped by call id — they are a
	// preview, never history; only the Execute result lands in output.
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
			out, outAtts = res.Text, res.Attachments
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
	// Attachments land even when the text output was rejected above and
	// replaced by an error — the media is produced work, and the model
	// still deserves to see it.
	if err := msg.SetToolOutputAttachments(call.ID(), outAtts); err != nil {
		return apperror.New(500, "tool output lost", err)
	}
	for _, tc := range msg.ToolCalls() {
		if tc.ID() == call.ID() {
			return d.Convs.UpdateToolCallOutput(ctx, convID, call.ID(), tc.Output(), tc.OutputAttachments())
		}
	}
	return apperror.New(500, "tool output lost", fmt.Errorf("call %s vanished after update", call.ID()))
}
