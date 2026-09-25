// Package textcompletion is the OpenAI-shaped /chat/completions adapter —
// the widest wire in the ecosystem: OpenAI, OpenRouter, DeepSeek, Groq,
// xAI, Mistral, and the compatible long tail all speak it, so one adapter
// routes them all. Modality input rides as content parts; generation
// endpoints (images/audio/…) are separate families, not this wire.
package textcompletion

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	drepo "github.com/jahrulnr/llm-base/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// Options configures the service. Client defaults to a transport with a
// header deadline only — interactive streams must not carry a fixed total
// timeout (reasoning models think for minutes); the idle deadline resets
// per SSE chunk. Referer/Title ride as OpenRouter attribution headers.
type Options struct {
	Client      *http.Client
	Logger      drepo.Logger
	Referer     string        // → HTTP-Referer
	Title       string        // → X-Title
	IdleTimeout time.Duration // per-chunk deadline; default 90s
}

// Service implements TextModelService against a bound gateway.
type Service struct {
	endpoint string
	apiKey   string
	stream   bool
	client   *http.Client
	log      drepo.Logger
	referer  string
	title    string
	idle     time.Duration
}

// New binds the service to one gateway — endpoint, credentials, and the
// stream preference live there.
func New(gw *aggregate.Gateway, opts Options) *Service {
	client := opts.Client
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		}}
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = 90 * time.Second
	}
	return &Service{
		endpoint: gw.Endpoint(),
		apiKey:   gw.ApiKey(),
		stream:   gw.StreamMode(),
		client:   client,
		log:      opts.Logger,
		referer:  opts.Referer,
		title:    opts.Title,
		idle:     idle,
	}
}

// Send performs one completion attempt — request marshal, POST, decode,
// then append the assistant message and usage onto conv. Stream mode is
// selected by the bound gateway; events reach onEvent uniformly either
// way.
func (s *Service) Send(ctx context.Context, conv *aggregate.Conversation, model entity.Model, onEvent event.LLMEventHandler) error {
	body, err := encodeRequest(conv, model, s.stream)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	s.logAppend(ctx, "provider.request", json.RawMessage(raw))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if s.referer != "" {
		req.Header.Set("HTTP-Referer", s.referer)
	}
	if s.title != "" {
		req.Header.Set("X-Title", s.title)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return apperror.New(502, "upstream unreachable", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := readAll(resp.Body)
		s.logAppend(ctx, "provider.error", map[string]any{
			"status": resp.StatusCode, "request_id": resp.Header.Get("x-request-id"), "body": json.RawMessage(raw),
		})
		return statusError(resp.StatusCode, raw)
	}

	if s.stream {
		return s.streamResponse(ctx, resp, conv, onEvent)
	}
	return s.unaryResponse(ctx, resp, conv, onEvent)
}

// unaryResponse decodes the full body, appends the message, and emits the
// synthetic delta+finished pair the port promises non-stream callers.
func (s *Service) unaryResponse(ctx context.Context, resp *http.Response, conv *aggregate.Conversation, onEvent event.LLMEventHandler) error {
	raw, err := readAll(resp.Body)
	if err != nil {
		return apperror.New(502, "unreadable upstream body", err)
	}
	s.logAppend(ctx, "provider.response", map[string]any{
		"request_id": resp.Header.Get("x-request-id"),
		"model":      resp.Header.Get("openai-model"),
		"body":       json.RawMessage(raw),
	})
	var rb ResponseBody
	if err := json.Unmarshal(raw, &rb); err != nil {
		return apperror.New(502, "malformed upstream body", err)
	}
	if len(rb.Choices) == 0 {
		return apperror.New(502, "upstream returned no choices", nil)
	}
	ch := rb.Choices[0]
	msg, err := decodeMessage(ch.Message, ch.FinishReason)
	if err != nil {
		return err
	}
	usage, err := decodeUsage(rb.Usage)
	if err != nil {
		return err
	}
	conv.Append(msg)
	conv.AddUsage(usage)
	emit(onEvent, event.NewDeltaEvent(msg.Content()))
	for _, tc := range msg.ToolCalls() {
		emit(onEvent, event.NewToolCallEvent(tc))
	}
	emit(onEvent, event.NewFinishedEvent(msg.FinishReason(), usage))
	return nil
}

func emit(h event.LLMEventHandler, e event.StreamEvent) {
	if h != nil {
		h(e)
	}
}

func (s *Service) logAppend(ctx context.Context, kind string, v any) {
	if s.log != nil {
		_ = s.log.Append(ctx, kind, v) // diagnostics must never break a send
	}
}
