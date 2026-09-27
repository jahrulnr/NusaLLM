package textresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// sse is one decoded event block — named events, and the payload's own
// type field is the dispatch token.
type sse struct {
	event string
	data  []byte
}

// streamResponse consumes the named-SSE body per the wire rules: text and
// reasoning deltas stream live, output_item.done items accumulate as a
// fallback, and response.completed — which carries the full response
// object — is the commit point. response.incomplete commits with its
// truncation mapping; response.failed/error events are terminal errors.
// An idle past s.idle (reset per event) is a dead connection — reasoning
// phases run minutes with no text output.
func (s *Service) streamResponse(ctx context.Context, resp *http.Response, conv *aggregate.Conversation, onEvent event.TurnEventHandler) error {
	events := make(chan sse)
	errs := make(chan error, 1)
	done := make(chan struct{})
	defer close(done) // unblocks a pending send in the pump on early return
	go readSSE(resp.Body, done, events, errs)

	var (
		fallback []json.RawMessage
		terminal *ResponseBody
		stopSeen bool
		logged   []json.RawMessage
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
			// stream closed — only commit when the terminal marker was
			// seen; EOF without one means a truncated upstream stream.
			if !stopSeen {
				err := apperror.New(502, "upstream stream truncated", nil)
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			return s.commitStream(conv, onEvent, terminal, fallback)
		case ev := <-events:
			timer.Reset(s.idle)
			if len(ev.data) == 0 {
				continue
			}
			var se StreamEvent
			if err := json.Unmarshal(ev.data, &se); err != nil {
				continue // partial/garbage lines are dropped, not fatal
			}
			logged = append(logged, ev.data)
			switch se.Type {
			case "response.output_text.delta":
				emit(onEvent, event.NewDeltaEvent(se.Delta))
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				emit(onEvent, event.NewReasoningEvent(se.Delta))
			case "response.output_item.done":
				if len(se.Item) > 0 {
					fallback = append(fallback, se.Item)
				}
			case "response.completed", "response.incomplete":
				stopSeen = true
				terminal = se.Response
				// The terminal event is the commit point — close now per
				// the wire rules instead of waiting for server EOF.
				return s.commitStream(conv, onEvent, terminal, fallback)
			case "response.failed":
				stopSeen = true
				err := streamFailure(se.Response, ev.data)
				s.logAppend(ctx, "provider.stream", logged)
				emit(onEvent, event.NewErrorEvent(err))
				return err
			case "error":
				err := statusError(streamStatusHint(&ErrorBody{
					Message: se.Message, Code: se.Code, Param: se.Param,
				}), ev.data)
				s.logAppend(ctx, "provider.stream", logged)
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			// response.created, output_item.added, content_part.*,
			// function_call_arguments.delta — nothing to fold in; the
			// committed message rebuilds from terminal output anyway.
		}
	}
}

// commitStream turns the terminal response into the assistant message
// exactly once. response.completed carries the full output array; when it
// is missing (partial implementations), the accumulated output_item.done
// items stand in — better a faithful commit than none.
func (s *Service) commitStream(conv *aggregate.Conversation, onEvent event.TurnEventHandler, terminal *ResponseBody, fallback []json.RawMessage) error {
	rb := terminal
	if rb == nil {
		rb = &ResponseBody{Status: "completed", Output: fallback}
	} else if len(rb.Output) == 0 && len(fallback) > 0 {
		cp := *rb
		cp.Output = fallback
		rb = &cp
	}
	msg, _, err := decodeResponse(rb) // reasoning already streamed live
	if err != nil {
		return err
	}
	u, err := decodeUsage(rb.Usage)
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

// streamFailure builds the error for a response.failed terminal event —
// the response object's error field carries the cause when populated.
func streamFailure(rb *ResponseBody, raw []byte) error {
	msg := "upstream response failed"
	code := ""
	if rb != nil && rb.Error != nil {
		if rb.Error.Message != "" {
			msg = rb.Error.Message
		}
		code = errorCode(*rb.Error)
	}
	if code == "rate_limit_exceeded" {
		return apperror.FromStatus(429, msg, wire.UpstreamError(raw))
	}
	if contextOverflow(msg, code) {
		return apperror.FromStatus(413, "context length exceeded", wire.UpstreamError(raw))
	}
	return apperror.FromStatus(502, msg, wire.UpstreamError(raw))
}

// readSSE pumps named SSE event payloads until EOF — this wire ends on
// response.completed inside the payload grammar, not a [DONE] sentinel.
// Sends select on done so a canceled consumer can't leak the pump.
func readSSE(body io.Reader, done <-chan struct{}, out chan<- sse, errs chan<- error) {
	defer close(errs) // nil receive = clean close
	sc := wire.ScanSSE(body)
	for sc.Scan() {
		_, payload := wire.SSENamed(sc.Bytes())
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
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
