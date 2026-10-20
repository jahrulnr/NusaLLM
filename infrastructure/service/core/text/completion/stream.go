package textcompletion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// sse is one decoded event block — OpenAI-style streams carry no event:
// line, so only the data payload matters. done marks the [DONE] sentinel.
type sse struct {
	data []byte
	done bool
}

// maxCallIndex bounds tool_calls fragments — index is provider-controlled,
// so a hostile/absurd value must never size our slice.
const maxCallIndex = 1024

// streamResponse consumes the SSE body per the wire rules: accumulate
// deltas in order, treat a terminal finish_reason/[DONE] as the only
// commit point, and bail on idle past s.idle (reset per chunk) — reasoning
// models are slow, dead connections are not.
func (s *Service) streamResponse(ctx context.Context, resp *http.Response, conv *aggregate.Conversation, onEvent event.TurnEventHandler) error {
	events := make(chan sse)
	errs := make(chan error, 1)
	done := make(chan struct{})
	defer close(done) // unblocks a pending send in the pump on early return
	go readSSE(resp.Body, done, events, errs)

	var (
		content          strings.Builder
		reasoning        strings.Builder // OpenRouter `reasoning` plaintext
		reasoningContent strings.Builder // DeepSeek `reasoning_content`
		details          []json.RawMessage
		calls            []ToolCallBody // assembled by index — order preserved
		finish           string
		usage            *UsageBody
		doneSeen         bool
		logged           []json.RawMessage
	)
	timer := time.NewTimer(s.idle)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logAppend(ctx, "provider.stream", logged)
			// Keep text and reasoning that already arrived. A tool call
			// stays only when its argument delta is valid JSON; a call
			// still streaming arguments is dropped.
			kept := completeToolCalls(calls)
			if content.Len() > 0 || reasoning.Len() > 0 || reasoningContent.Len() > 0 || len(kept) > 0 {
				// Details are deliberately dropped on cancel: a partially
				// streamed signature/encrypted blob is not replayable.
				if err := s.commitStream(conv, onEvent, content.String(), reasoning.String(), reasoningContent.String(), nil, kept, "", usage); err != nil {
					return err
				}
			}
			return apperror.New(408, "request canceled or timed out", ctx.Err())
		case <-timer.C:
			s.logAppend(ctx, "provider.stream", logged)
			return apperror.New(502, "upstream stream idle", nil)
		case err := <-errs:
			s.logAppend(ctx, "provider.stream", logged)
			if err != nil {
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			// stream closed — only commit when a real terminal marker was
			// seen; EOF without one means a truncated upstream stream.
			if finish == "" && !doneSeen {
				err := apperror.New(502, "upstream stream truncated", nil)
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			return s.commitStream(conv, onEvent, content.String(), reasoning.String(), reasoningContent.String(), details, completeToolCalls(calls), finish, usage)
		case ev := <-events:
			timer.Reset(s.idle)
			if ev.done {
				doneSeen = true
				continue
			}
			var chunk ResponseBody
			if err := json.Unmarshal(ev.data, &chunk); err != nil {
				continue // partial/garbage lines are dropped, not fatal
			}
			logged = append(logged, ev.data)
			if chunk.Error != nil {
				// Re-wrap into the {"error":{…}} envelope statusError parses.
				raw, _ := json.Marshal(map[string]any{"error": chunk.Error})
				err := statusError(statusHint(chunk.Error), raw)
				s.logAppend(ctx, "provider.stream", logged)
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			if chunk.Usage != nil {
				usage = chunk.Usage // may ride an empty-delta choice — keep both
			}
			for _, ch := range chunk.Choices {
				if ch.FinishReason != "" {
					finish = ch.FinishReason
				}
				if t, ok := ch.Delta.Content.(string); ok {
					content.WriteString(t)
					emit(onEvent, event.NewDeltaEvent(t))
				}
				// Keep reasoning separate from answer deltas while preserving
				// its original arrival order. OpenRouter spells the plaintext
				// alias `reasoning`; DeepSeek uses `reasoning_content`; the
				// structured `reasoning_details` array carries signatures and
				// encrypted blobs. Prefer the plaintext alias for display so
				// a chunk carrying both does not double-emit.
				switch {
				case ch.Delta.Reasoning != "":
					reasoning.WriteString(ch.Delta.Reasoning)
					emit(onEvent, event.NewReasoningEvent(ch.Delta.Reasoning))
				case ch.Delta.ReasoningContent != "":
					reasoningContent.WriteString(ch.Delta.ReasoningContent)
					emit(onEvent, event.NewReasoningEvent(ch.Delta.ReasoningContent))
				}
				if len(ch.Delta.ReasoningDetails) > 0 {
					var chunkDetails []json.RawMessage
					if json.Unmarshal(ch.Delta.ReasoningDetails, &chunkDetails) == nil {
						details = append(details, chunkDetails...)
						if ch.Delta.Reasoning == "" && ch.Delta.ReasoningContent == "" {
							if text := reasoningTextFromDetails(ch.Delta.ReasoningDetails); text != "" {
								emit(onEvent, event.NewReasoningEvent(text))
							}
						}
					}
				}
				for _, tc := range ch.Delta.ToolCalls {
					calls = mergeCallDelta(calls, tc)
				}
			}
		}
	}
}

// commitStream turns the accumulated stream state into the assistant
// message exactly once — called on clean close or EOF-after-terminal.
func (s *Service) commitStream(conv *aggregate.Conversation, onEvent event.TurnEventHandler, content, reasoning, reasoningContent string, details []json.RawMessage, calls []ToolCallBody, finish string, usage *UsageBody) error {
	var rawDetails json.RawMessage
	if len(details) > 0 {
		if raw, err := json.Marshal(details); err == nil {
			rawDetails = raw
		}
	}
	msg, err := decodeMessage(MessageBody{
		Role: "assistant", Content: content, ToolCalls: calls,
		Reasoning: reasoning, ReasoningContent: reasoningContent, ReasoningDetails: rawDetails,
	}, finish)
	if err != nil {
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

// completeToolCalls keeps calls whose id, name, and argument JSON all
// arrived. A truncated argument string is not a call yet.
func completeToolCalls(calls []ToolCallBody) []ToolCallBody {
	kept := make([]ToolCallBody, 0, len(calls))
	for _, c := range calls {
		args := c.Function.Arguments
		if c.ID == "" || c.Function.Name == "" || args == "" || !json.Valid([]byte(args)) {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

// mergeCallDelta folds a streamed tool_calls fragment into the call at its
// index — fragments arrive partial (id once, name once, arguments split
// across chunks) and strictly in order per index. Index is untrusted:
// negatives are dropped and absurd values are capped.
func mergeCallDelta(calls []ToolCallBody, d ToolCallBody) []ToolCallBody {
	i := 0
	if d.Index != nil {
		if *d.Index < 0 || *d.Index > maxCallIndex {
			return calls
		}
		i = *d.Index
	}
	for len(calls) <= i {
		var t ToolCallBody
		t.Type = "function"
		calls = append(calls, t)
	}
	if d.ID != "" {
		calls[i].ID = d.ID
	}
	if d.Function.Name != "" {
		calls[i].Function.Name += d.Function.Name
	}
	calls[i].Function.Arguments += d.Function.Arguments
	return calls
}

// statusHint maps a mid-stream error envelope to a status — providers send
// no HTTP status inside the stream, so derive one from the code and type.
func statusHint(e *ErrorBody) int {
	for _, c := range []string{fmt.Sprint(e.Code), e.Type} {
		switch c {
		case "429", "rate_limit_exceeded", "rate_limit_error":
			return 429
		case "401", "authentication_error":
			return 401
		case "context_length_exceeded":
			return 413
		}
	}
	return 502
}

// readSSE pumps decoded event payloads until [DONE] or EOF. [DONE] is the
// terminal sentinel on this surface — stop there, never wait for server
// EOF. Sends select on done so a canceled consumer can't leak the pump.
func readSSE(body io.Reader, done <-chan struct{}, out chan<- sse, errs chan<- error) {
	defer close(errs) // nil receive = clean close
	sc := wire.ScanSSE(body)
	for sc.Scan() {
		payload := wire.SSEData(sc.Bytes())
		if len(payload) == 0 {
			continue
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			select {
			case out <- sse{done: true}:
			case <-done:
			}
			return
		}
		select {
		case out <- sse{data: payload}:
		case <-done:
			return
		}
	}
	if err := sc.Err(); err != nil {
		errs <- apperror.New(502, "upstream stream broken", err)
	}
}

func readAll(r io.Reader) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(io.LimitReader(r, 32<<20)) // 32MB guard
	return b.Bytes(), err
}
