package textresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
		text     strings.Builder
		reason   strings.Builder
		fns      []fnAcc
	)
	timer := time.NewTimer(s.idle)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logAppend(ctx, "provider.stream", logged)
			if err := s.commitCanceled(conv, onEvent, text.String(), reason.String(), fns, nil); err != nil {
				return err
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
				text.WriteString(se.Delta)
				emit(onEvent, event.NewDeltaEvent(se.Delta))
			case "response.reasoning_summary_text.delta":
				// Official summary text — published for display, it may
				// cross to the owner.
				reason.WriteString(se.Delta)
				emit(onEvent, event.NewReasoningSummaryEvent(se.Delta))
			case "response.reasoning_text.delta":
				// Raw reasoning remains a distinct ordered content event.
				reason.WriteString(se.Delta)
				emit(onEvent, event.NewReasoningEvent(se.Delta))
			case "response.output_item.added":
				noteFunctionCall(&fns, se.OutputIndex, se.Item, false)
			case "response.function_call_arguments.delta":
				if slot := fnSlot(&fns, se.OutputIndex); slot != nil {
					slot.args.WriteString(se.Delta)
				}
			case "response.function_call_arguments.done":
				if slot := fnSlot(&fns, se.OutputIndex); slot != nil {
					if se.Arguments != "" {
						slot.args.Reset()
						slot.args.WriteString(se.Arguments)
					}
					slot.done = true
				}
			case "response.output_item.done":
				if len(se.Item) > 0 {
					fallback = append(fallback, se.Item)
					noteFunctionCall(&fns, se.OutputIndex, se.Item, true)
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
			// response.created, content_part.* — nothing else to fold in; a
			// completed turn still rebuilds from the terminal output.
		}
	}
}

// fnAcc is one function call assembled from argument deltas. done means
// the provider closed the call; a still-open call is kept only when its
// argument JSON has already closed.
type fnAcc struct {
	id, name string
	args     strings.Builder
	done     bool
}

func fnSlot(fns *[]fnAcc, i int) *fnAcc {
	if i < 0 || i > 64 {
		return nil
	}
	for len(*fns) <= i {
		*fns = append(*fns, fnAcc{})
	}
	return &(*fns)[i]
}

func noteFunctionCall(fns *[]fnAcc, index int, raw json.RawMessage, done bool) {
	var it ItemShape
	if json.Unmarshal(raw, &it) != nil || it.Type != "function_call" {
		return
	}
	slot := fnSlot(fns, index)
	if slot == nil {
		return
	}
	if it.CallID != "" {
		slot.id = it.CallID
	}
	if it.Name != "" {
		slot.name = it.Name
	}
	if it.Arguments != "" {
		slot.args.Reset()
		slot.args.WriteString(it.Arguments)
	}
	if done {
		slot.done = true
	}
}

func fnComplete(c fnAcc) bool {
	if c.id == "" || c.name == "" {
		return false
	}
	args := c.args.String()
	if args == "" {
		return c.done
	}
	return json.Valid([]byte(args))
}

// commitCanceled writes the text, reasoning, and finished tool calls that
// arrived before the caller stopped the stream. An open tool-argument
// delta is left out. A stream with nothing to keep is a no-op.
func (s *Service) commitCanceled(conv *aggregate.Conversation, onEvent event.TurnEventHandler, text, reasoning string, calls []fnAcc, usage *UsageBody) error {
	var output []json.RawMessage
	if text != "" {
		raw, err := json.Marshal(map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]string{{"type": "output_text", "text": text}},
		})
		if err != nil {
			return err
		}
		output = append(output, raw)
	}
	if reasoning != "" {
		raw, err := json.Marshal(map[string]any{
			"type":    "reasoning",
			"summary": []map[string]string{{"type": "summary_text", "text": reasoning}},
		})
		if err != nil {
			return err
		}
		output = append(output, raw)
	}
	for _, c := range calls {
		if !fnComplete(c) {
			continue
		}
		args := c.args.String()
		if args == "" {
			args = "{}"
		}
		raw, err := json.Marshal(ItemBody{Type: "function_call", CallID: c.id, Name: c.name, Arguments: args})
		if err != nil {
			return err
		}
		output = append(output, raw)
	}
	if len(output) == 0 {
		return nil
	}
	return s.commitStream(conv, onEvent, &ResponseBody{Status: "incomplete", Output: output, Usage: usage}, nil)
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
