package textmessages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// sse is one decoded event block — Anthropic streams are named, so the
// event name (not just the payload) drives the state machine.
type sse struct {
	event string
	data  []byte
}

// maxBlockIndex bounds content_block_* indexes — index is
// provider-controlled, so a hostile/absurd value must never size our
// slice.
const maxBlockIndex = 1024

// blockAcc accumulates one streamed content block. Which builder fills
// depends on the block kind and its delta types; raw keeps the start
// payload for kinds we never model (server tool blocks, …) so replay can
// still emit something faithful.
type blockAcc struct {
	kind     string
	text     strings.Builder // text_delta
	thinking strings.Builder // thinking_delta
	sig      strings.Builder // signature_delta
	args     strings.Builder // input_json_delta
	id, name string          // tool_use head at block start
	raw      json.RawMessage // non-model block start payload
	done     bool            // content_block_stop seen
}

// streamResponse consumes the named-SSE body per the wire rules: blocks
// accumulate by index, message_delta carries the terminal stop_reason and
// cumulative usage, message_stop is the only commit point, and an idle
// past s.idle (reset per event) is a dead connection — thinking phases
// run minutes with no text output.
func (s *Service) streamResponse(ctx context.Context, resp *http.Response, conv *aggregate.Conversation, onEvent event.TurnEventHandler) error {
	events := make(chan sse)
	errs := make(chan error, 1)
	done := make(chan struct{})
	defer close(done) // unblocks a pending send in the pump on early return
	go readSSE(resp.Body, done, events, errs)

	var (
		blocks   []blockAcc
		finish   string
		usage    *UsageBody
		stopSeen bool
		logged   []json.RawMessage
	)
	timer := time.NewTimer(s.idle)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logAppend(ctx, "provider.stream", logged)
			kept := replayableBlocks(blocks)
			if blocksHaveContent(kept) {
				if err := s.commitStream(conv, onEvent, kept, "", usage, logged); err != nil {
					return err
				}
			}
			return apperror.New(408, "request canceled or timed out", ctx.Err())
		case <-timer.C:
			s.logAppend(ctx, "provider.stream", logged)
			// message_stop — or the stop_reason that precedes it — already
			// proved the turn ended; a stalled tail after a verdict commits
			// like stream EOF, not a 502. The verdict is the semantic
			// terminal, the same rule the completion wire applies to
			// finish_reason.
			if !stopSeen && finish == "" {
				return apperror.New(502, "upstream stream idle", nil)
			}
			if finish == "" {
				finish = "end_turn"
			}
			if truncatedToolUse(blocks) {
				return apperror.New(502, "upstream truncated a tool call", nil)
			}
			return s.commitStream(conv, onEvent, replayableBlocks(blocks), finish, usage, logged)
		case err := <-errs:
			s.logAppend(ctx, "provider.stream", logged)
			// stream closed — commit when the terminal marker or the
			// stop_reason was seen; EOF or a transport error with neither
			// means a truncated upstream stream. A reset in the tail gap
			// after message_stop (or after the verdict) must not discard a
			// complete turn.
			if !stopSeen && finish == "" {
				if err == nil {
					err = apperror.New(502, "upstream stream truncated", nil)
				}
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			if finish == "" {
				// message_stop reached without stop_reason — a settled
				// commit always carries a verdict; "" only survives on
				// partial (canceled) commits.
				finish = "end_turn"
			}
			if truncatedToolUse(blocks) {
				return apperror.New(502, "upstream truncated a tool call", nil)
			}
			return s.commitStream(conv, onEvent, replayableBlocks(blocks), finish, usage, logged)
		case ev := <-events:
			timer.Reset(s.idle)
			if len(ev.data) == 0 {
				continue
			}
			var se StreamEvent
			if err := json.Unmarshal(ev.data, &se); err != nil {
				// A named error event whose payload does not fit the nested
				// envelope is still the provider saying why it stopped — a
				// proxy that flattens it must not be read as a truncated
				// stream.
				if ev.event == "error" {
					err := wire.WithRetryAfter(streamErrorEvent(ev.data), wire.RetryAfter(resp.Header.Get("Retry-After")))
					s.logAppend(ctx, "provider.stream", logged)
					emit(onEvent, event.NewErrorEvent(err))
					return err
				}
				continue // partial/garbage lines are dropped, not fatal
			}
			logged = append(logged, ev.data)
			switch se.Type {
			case "message_start":
				if se.Message != nil {
					usage = mergeUsage(usage, se.Message.Usage)
				}
			case "content_block_start":
				if se.ContentBlock != nil {
					blocks = startBlock(blocks, se.Index, *se.ContentBlock)
				}
			case "content_block_delta":
				if se.Delta != nil {
					applyDelta(blocks, se.Index, *se.Delta, onEvent)
				}
			case "message_delta":
				if se.Delta != nil && se.Delta.StopReason != "" {
					finish = se.Delta.StopReason
				}
				if se.Usage != nil {
					// Cumulative, but a proxy may repeat only the counters
					// that changed — merge so the input count message_start
					// carried is not zeroed by a partial message_delta.
					usage = mergeUsage(usage, se.Usage)
				}
			case "content_block_stop":
				if se.Index >= 0 && se.Index < len(blocks) {
					blocks[se.Index].done = true
				}
			case "message_stop":
				stopSeen = true
			case "error":
				if se.Error == nil {
					// The event name says error but the payload carries no
					// envelope — still a terminal failure, never silence.
					err := wire.WithRetryAfter(streamErrorEvent(ev.data), wire.RetryAfter(resp.Header.Get("Retry-After")))
					s.logAppend(ctx, "provider.stream", logged)
					emit(onEvent, event.NewErrorEvent(err))
					return err
				}
				if se.Error != nil {
					raw, _ := json.Marshal(map[string]any{"error": se.Error})
					err := wire.WithRetryAfter(statusError(streamStatusHint(se.Error), raw), wire.RetryAfter(resp.Header.Get("Retry-After")))
					s.logAppend(ctx, "provider.stream", logged)
					emit(onEvent, event.NewErrorEvent(err))
					return err
				}
			}
			// ping, content_block_stop, unknown types — nothing to fold in.
		}
	}
}

// mergeUsage folds a later cumulative usage object over what arrived
// earlier. Presence decides whether a field overwrites — a later object that
// repeats only the counters that changed leaves the rest intact, and a
// legitimate zero (the cache count dropped) still lands. Gating on non-zero
// would silently keep a stale count.
func mergeUsage(prev, next *UsageBody) *UsageBody {
	if next == nil {
		return prev
	}
	if prev == nil {
		return next
	}
	if next.InputTokens != nil {
		prev.InputTokens = next.InputTokens
	}
	if next.OutputTokens != nil {
		prev.OutputTokens = next.OutputTokens
	}
	if next.CacheCreationInputTokens != nil {
		prev.CacheCreationInputTokens = next.CacheCreationInputTokens
	}
	if next.CacheReadInputTokens != nil {
		prev.CacheReadInputTokens = next.CacheReadInputTokens
	}
	return prev
}

// startBlock opens the accumulator at index — bounded, sparse-tolerant.
func startBlock(blocks []blockAcc, i int, head ContentBlock) []blockAcc {
	if i < 0 || i > maxBlockIndex {
		return blocks
	}
	for len(blocks) <= i {
		blocks = append(blocks, blockAcc{})
	}
	blocks[i] = blockAcc{kind: head.Type, id: head.ID, name: head.Name}
	if head.Type != "text" && head.Type != "thinking" && head.Type != "tool_use" {
		raw, _ := json.Marshal(head) // redacted_thinking, server blocks…
		blocks[i].raw = raw
	}
	if head.Text != "" {
		blocks[i].text.WriteString(head.Text)
	}
	if head.Thinking != "" {
		blocks[i].thinking.WriteString(head.Thinking)
	}
	return blocks
}

// replayableBlocks drops state that cannot replay: a tool_use whose
// argument delta never finished, and a thinking block whose signature never
// arrived — Anthropic rejects an unsigned thinking block, so committing
// one poisons every later turn.
func replayableBlocks(blocks []blockAcc) []blockAcc {
	kept := make([]blockAcc, 0, len(blocks))
	for _, b := range blocks {
		if b.kind == "tool_use" && !toolUseComplete(b) {
			continue
		}
		if b.kind == "thinking" && b.sig.Len() == 0 {
			continue
		}
		kept = append(kept, b)
	}
	return kept
}

// truncatedToolUse reports a settled stream whose assembled tool call never
// closed: the terminal arrived, so the provider finished, but the call it was
// building cannot replay. Committing without it would silently drop an action
// the model asked for — the step fails instead, and the turn may retry while
// nothing was persisted.
func truncatedToolUse(blocks []blockAcc) bool {
	for _, b := range blocks {
		if b.kind != "tool_use" || b.id == "" || b.name == "" {
			continue
		}
		if args := b.args.String(); args != "" && !json.Valid([]byte(args)) {
			return true
		}
	}
	return false
}

// streamErrorEvent builds the failure for a named error event that does not
// carry the nested {error:{…}} envelope. The provider's own message is used
// when the payload names one; the raw payload stays in the cause.
func streamErrorEvent(payload []byte) error {
	msg := ""
	var flat struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &flat) == nil {
		msg = strings.TrimSpace(flat.Message)
	}
	if msg == "" {
		msg = "upstream stream error"
	}
	return apperror.FromStatus(502, msg, wire.UpstreamError(payload))
}

func toolUseComplete(b blockAcc) bool {
	if b.id == "" || b.name == "" {
		return false
	}
	args := b.args.String()
	if args == "" {
		return b.done
	}
	return json.Valid([]byte(args))
}

func blocksHaveContent(blocks []blockAcc) bool {
	for _, b := range blocks {
		if b.text.Len() > 0 || b.thinking.Len() > 0 || b.kind == "tool_use" || len(b.raw) > 0 {
			return true
		}
	}
	return false
}

// applyDelta folds one delta into the accumulator at index and emits the
// live events the port promises — text and thinking stream through, tool
// arguments assemble silently (surfaced as a call at commit).
func applyDelta(blocks []blockAcc, i int, d DeltaBody, onEvent event.TurnEventHandler) {
	if i < 0 || i > maxBlockIndex || i >= len(blocks) {
		return
	}
	switch d.Type {
	case "text_delta":
		blocks[i].text.WriteString(d.Text)
		emit(onEvent, event.NewDeltaEvent(d.Text))
	case "thinking_delta":
		blocks[i].thinking.WriteString(d.Thinking)
		emit(onEvent, event.NewReasoningEvent(d.Thinking))
	case "signature_delta":
		blocks[i].sig.WriteString(d.Signature)
	case "input_json_delta":
		blocks[i].args.WriteString(d.PartialJSON)
	}
}

// commitStream turns the accumulated blocks into the assistant message
// exactly once — called on clean close after message_stop.
func (s *Service) commitStream(conv *aggregate.Conversation, onEvent event.TurnEventHandler, blocks []blockAcc, finish string, usage *UsageBody, logged []json.RawMessage) error {
	raw := make([]json.RawMessage, 0, len(blocks))
	for _, b := range blocks {
		switch b.kind {
		case "text":
			raw = append(raw, mustMarshal(ContentBlock{Type: "text", Text: b.text.String()}))
		case "thinking":
			// thinking is required even when display:"omitted" leaves
			// the text empty; ContentBlock's omitempty would drop it.
			raw = append(raw, mustMarshal(thinkingBlock{Type: "thinking", Thinking: b.thinking.String(), Signature: b.sig.String()}))
		case "tool_use":
			in := json.RawMessage(b.args.String())
			if !json.Valid(in) || len(in) == 0 {
				in = json.RawMessage("{}")
			}
			raw = append(raw, mustMarshal(ContentBlock{Type: "tool_use", ID: b.id, Name: b.name, Input: in}))
		default:
			if len(b.raw) > 0 {
				raw = append(raw, b.raw)
			}
		}
	}
	msg, meta, err := decodeMessage(raw, finish) // reasoning already streamed live as thinking_delta
	if err != nil {
		return err
	}
	if err := coretext.EmptyAnswerError(msg, meta.artifacts, "", logged); err != nil {
		return err
	}
	u, err := decodeUsage(usage)
	if err != nil {
		return err
	}
	conv.Append(msg)
	conv.AddUsage(u)
	for _, tc := range msg.ToolCalls() {
		emit(onEvent, event.NewToolCallEvent(tc))
	}
	emit(onEvent, event.NewFinishedEvent(msg.FinishReason(), u))
	return nil
}

func mustMarshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

// readSSE pumps named SSE event payloads until EOF — this wire has no
// [DONE] sentinel, message_stop marks the terminal turn boundary inside
// the payload grammar. Sends select on done so a canceled consumer can't
// leak the pump.
func readSSE(body io.Reader, done <-chan struct{}, out chan<- sse, errs chan<- error) {
	defer close(errs) // nil receive = clean close
	sc := wire.ScanSSE(body)
	for sc.Scan() {
		name, payload := wire.SSENamed(sc.Bytes())
		if len(payload) == 0 {
			// Comments/keepalives are traffic too — heartbeat the idle
			// timer; the consumer drops the empty event.
			select {
			case out <- sse{}:
			case <-done:
				return
			}
			continue
		}
		select {
		case out <- sse{event: name, data: payload}:
		case <-done:
			return
		}
	}
	if err := sc.Err(); err != nil {
		errs <- apperror.New(502, "upstream stream broken", err)
	}
}
