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

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
	"github.com/jahrulnr/llm-base/infrastructure/service/wire"
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
		content   strings.Builder
		reasoning strings.Builder
		calls     []ToolCallBody // assembled by index — order preserved
		finish    string
		usage     *UsageBody
		doneSeen  bool
		logged    []json.RawMessage
	)
	timer := time.NewTimer(s.idle)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logAppend(ctx, "provider.stream", logged)
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
			return s.commitStream(conv, onEvent, content.String(), reasoning.String(), calls, finish, usage)
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
				if ch.Delta.ReasoningContent != "" {
					reasoning.WriteString(ch.Delta.ReasoningContent)
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
func (s *Service) commitStream(conv *aggregate.Conversation, onEvent event.TurnEventHandler, content, reasoning string, calls []ToolCallBody, finish string, usage *UsageBody) error {
	msg, err := decodeMessage(MessageBody{Role: "assistant", Content: content, ToolCalls: calls, ReasoningContent: reasoning}, finish)
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
