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
// line, so only the data payload matters.
type sse struct{ data []byte }

// streamResponse consumes the SSE body per the wire rules: accumulate
// deltas in order, treat the terminal finish_reason/[DONE] as the only
// commit point, and bail on idle past s.idle (reset per chunk) — reasoning
// models are slow, dead connections are not.
func (s *Service) streamResponse(ctx context.Context, resp *http.Response, conv *aggregate.Conversation, onEvent event.LLMEventHandler) error {
	events := make(chan sse)
	errs := make(chan error, 1)
	go readSSE(resp.Body, events, errs)

	var (
		content   strings.Builder
		reasoning strings.Builder
		calls     []ToolCallBody // assembled by index — order preserved
		finish    string
		usage     *UsageBody
		logged    []json.RawMessage
	)
	timer := time.NewTimer(s.idle)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return apperror.New(502, "upstream stream idle", nil)
		case err := <-errs:
			s.logAppend(ctx, "provider.stream", logged)
			if err != nil {
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			// stream closed — commit whatever terminal state we saw
			return s.commitStream(conv, onEvent, content.String(), reasoning.String(), calls, finish, usage)
		case ev := <-events:
			timer.Reset(s.idle)
			var chunk ResponseBody
			if err := json.Unmarshal(ev.data, &chunk); err != nil {
				continue // partial/garbage lines are dropped, not fatal
			}
			logged = append(logged, ev.data)
			if chunk.Error != nil {
				raw, _ := json.Marshal(chunk.Error)
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
func (s *Service) commitStream(conv *aggregate.Conversation, onEvent event.LLMEventHandler, content, reasoning string, calls []ToolCallBody, finish string, usage *UsageBody) error {
	wire := MessageBody{Role: "assistant", Content: content, ToolCalls: calls, ReasoningContent: reasoning}
	msg, err := decodeMessage(wire, finish)
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
// across chunks) and strictly in order per index.
func mergeCallDelta(calls []ToolCallBody, d ToolCallBody) []ToolCallBody {
	i := 0
	if d.Index != nil {
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
// no HTTP status inside the stream, so derive one from the code/type.
func statusHint(e *ErrorBody) int {
	switch fmt.Sprint(e.Code) {
	case "429", "rate_limit_exceeded":
		return 429
	case "401", "authentication_error":
		return 401
	case "context_length_exceeded":
		return 413
	default:
		return 502
	}
}

// readSSE pumps decoded event payloads until [DONE] or EOF. [DONE] is the
// terminal sentinel on this surface — stop there, never wait for server
// EOF.
func readSSE(body io.Reader, out chan<- sse, errs chan<- error) {
	defer close(errs) // nil receive = clean close
	sc := wire.ScanSSE(body)
	for sc.Scan() {
		payload := wire.SSEData(sc.Bytes())
		if len(payload) == 0 {
			continue
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			return
		}
		out <- sse{data: payload}
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
