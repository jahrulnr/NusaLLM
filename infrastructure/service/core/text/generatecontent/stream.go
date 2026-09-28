package textgeneratecontent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// streamResponse consumes the SSE body — each data: payload is a partial
// GenerateContentResponse, and there is no terminal sentinel: end-of-
// stream is connection close after the final candidate chunk. Text
// streams live, parts accumulate (consecutive same-kind text parts merge
// and adopt the latest signature), the last finishReason/usageMetadata
// win, and EOF is the commit point. An idle past s.idle (reset per chunk)
// is a dead connection — thinking phases run silent for minutes.
func (s *Service) streamResponse(ctx context.Context, resp *http.Response, conv *aggregate.Conversation, onEvent event.TurnEventHandler) error {
	events := make(chan []byte)
	errs := make(chan error, 1)
	done := make(chan struct{})
	defer close(done) // unblocks a pending send in the pump on early return
	go readSSE(resp.Body, done, events, errs)

	var (
		parts    []json.RawMessage
		finish   string
		usage    *UsageBody
		logged   []json.RawMessage
		blocked  string
		anyChunk bool
	)
	timer := time.NewTimer(s.idle)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logAppend(ctx, "provider.stream", logged)
			kept := completeParts(parts)
			if partsHaveContent(kept) {
				if err := s.commitStream(conv, onEvent, kept, "", usage, blocked); err != nil {
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
			// EOF is this wire's terminal marker — commit whatever
			// accumulated. A stream that died before its first chunk is a
			// truncated upstream, not an empty answer.
			if !anyChunk {
				err := apperror.New(502, "upstream stream truncated", nil)
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			return s.commitStream(conv, onEvent, parts, finish, usage, blocked)
		case data := <-events:
			timer.Reset(s.idle)
			logged = append(logged, data)
			var rb ResponseBody
			if err := json.Unmarshal(data, &rb); err != nil {
				continue // partial/garbage lines are dropped, not fatal
			}
			if rb.Error != nil {
				err := streamError(rb.Error, data)
				s.logAppend(ctx, "provider.stream", logged)
				emit(onEvent, event.NewErrorEvent(err))
				return err
			}
			anyChunk = true
			if rb.PromptFeedback != nil && rb.PromptFeedback.BlockReason != "" {
				blocked = rb.PromptFeedback.BlockReason
			}
			for _, c := range rb.Candidates {
				for _, raw := range c.Content.Parts {
					parts = appendPart(parts, raw, onEvent)
				}
				if c.FinishReason != "" {
					finish = c.FinishReason // the last one wins
				}
			}
			if rb.Usage != nil {
				usage = rb.Usage // cumulative — the last one wins
			}
		}
	}
}

// completeParts drops a function call whose args object is still open.
// Gemini delivers calls as whole parts; a truncated args payload is not
// a call yet. Text and thought parts stay.
func completeParts(parts []json.RawMessage) []json.RawMessage {
	kept := make([]json.RawMessage, 0, len(parts))
	for _, raw := range parts {
		var p PartBody
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		if p.FunctionCall != nil {
			args := p.FunctionCall.Args
			if p.FunctionCall.Name == "" || len(args) == 0 || !json.Valid(args) {
				continue
			}
		}
		kept = append(kept, raw)
	}
	return kept
}

func partsHaveContent(parts []json.RawMessage) bool {
	for _, raw := range parts {
		var p PartBody
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		if p.Text != "" || p.FunctionCall != nil || p.Thought {
			return true
		}
	}
	return false
}

// appendPart folds one streamed part into the accumulator and emits its
// live event. Text fragments of the same thought-ness merge into the
// trailing part (a signature lands on the last fragment and binds the
// merged whole); calls and other parts append as distinct entries.
func appendPart(parts []json.RawMessage, raw json.RawMessage, onEvent event.TurnEventHandler) []json.RawMessage {
	var p PartBody
	if err := json.Unmarshal(raw, &p); err != nil {
		return parts
	}
	if p.FunctionCall == nil && (p.Text != "" || p.ThoughtSignature != "" || textPart(raw)) {
		if p.Thought {
			emit(onEvent, event.NewReasoningEvent(p.Text))
		} else {
			emit(onEvent, event.NewDeltaEvent(p.Text))
		}
		// Merge into a trailing text-kind part of the same thought-ness.
		if len(parts) > 0 {
			var tail PartBody
			if json.Unmarshal(parts[len(parts)-1], &tail) == nil &&
				tail.FunctionCall == nil && tail.InlineData == nil && tail.FileData == nil &&
				tail.Thought == p.Thought {
				tail.Text += p.Text
				if p.ThoughtSignature != "" {
					tail.ThoughtSignature = p.ThoughtSignature // signature binds the merged whole
				}
				if merged, err := json.Marshal(tail); err == nil {
					parts[len(parts)-1] = merged
					return parts
				}
			}
		}
	}
	return append(parts, raw)
}

// commitStream turns the accumulated parts into the assistant message at
// EOF — blocked prompts never produce candidates; everything else commits
// through the shared decode.
func (s *Service) commitStream(conv *aggregate.Conversation, onEvent event.TurnEventHandler, parts []json.RawMessage, finish string, usage *UsageBody, blocked string) error {
	msg, _, err := decodeParts(parts, finish) // reasoning already streamed live
	if err != nil {
		return err
	}
	if blocked != "" && msg.FinishReason() == "" {
		msg.SetFinishReason(enum.FinishReasonContentFilter)
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

// readSSE pumps SSE data payloads until EOF — no [DONE] sentinel on this
// wire; payloads arrive as bare data: JSON. Sends select on done so a
// canceled consumer can't leak the pump.
func readSSE(body io.Reader, done <-chan struct{}, out chan<- []byte, errs chan<- error) {
	defer close(errs) // nil receive = clean close
	sc := wire.ScanSSE(body)
	for sc.Scan() {
		payload := wire.SSEData(sc.Bytes())
		if len(payload) == 0 {
			continue
		}
		select {
		case out <- payload:
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
