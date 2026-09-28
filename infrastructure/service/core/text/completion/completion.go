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
	"strings"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
)

// Service is the stateless translator for this wire — endpoint,
// credentials, and stream preference are read from the gateway argument
// of every Send, so one instance serves every completion-style gateway.
type Service struct {
	client   *http.Client
	log      drepo.Logger
	referer  string
	title    string
	fileRoot string
	idle     time.Duration
}

// New builds the service — Options only; gateways arrive per Send.
func New(opts coretext.Options) *Service {
	// Options map onto the service config section — zero fields take the
	// struct-tag defaults, so this adapter owns no literal of its own.
	svcCfg := config.ServiceConfig{
		HeaderTimeout: config.Duration(opts.HeaderTimeout),
		IdleTimeout:   config.Duration(opts.IdleTimeout),
	}
	svcCfg.Normalize()
	client := opts.Client
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: time.Duration(svcCfg.HeaderTimeout),
		}}
	}
	idle := time.Duration(svcCfg.IdleTimeout)
	return &Service{
		client:   client,
		log:      opts.Logger,
		referer:  opts.Referer,
		title:    opts.Title,
		fileRoot: opts.FileRoot,
		idle:     idle,
	}
}

// Send performs one completion attempt — request marshal, POST, decode,
// then append the assistant message and usage onto conv. Stream mode is
// selected by gw; events reach onEvent uniformly either way.
func (s *Service) Send(ctx context.Context, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, onEvent event.TurnEventHandler) error {
	stream := gw.StreamMode()
	body, err := encodeRequest(conv, model, stream, coretext.FileRoot(conv, s.fileRoot), gw.Endpoint())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	s.logAppend(ctx, "provider.request", json.RawMessage(raw))

	// gw.Endpoint() is the provider's base URL (e.g. https://api.openai.com/v1);
	// this wire lives at /chat/completions under it.
	url := strings.TrimRight(gw.Endpoint(), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+gw.ApiKey())
	req.Header.Set("Content-Type", "application/json")
	if strings.Contains(gw.Endpoint(), enum.OpenCode.String()) {
		req.Header.Set("x-opencode-session", conv.ID())
	}
	if s.referer != "" {
		req.Header.Set("HTTP-Referer", s.referer)
	}
	if s.title != "" {
		req.Header.Set("X-Title", s.title)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil { // caller gave up — not an upstream failure
			return apperror.New(408, "request canceled or timed out", ctx.Err())
		}
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

	if stream {
		return s.streamResponse(ctx, resp, conv, onEvent)
	}
	return s.unaryResponse(ctx, resp, conv, onEvent)
}

// unaryResponse decodes the full body, appends the message, and emits the
// synthetic delta+finished pair the port promises non-stream callers.
func (s *Service) unaryResponse(ctx context.Context, resp *http.Response, conv *aggregate.Conversation, onEvent event.TurnEventHandler) error {
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
	if r := msg.OptionalContent()["completion.reasoning_content"]; r != "" {
		// Preserve reasoning separately so the application can store it
		// in the message timeline and the delivery layer can render it.
		emit(onEvent, event.NewReasoningEvent(r))
	}
	emit(onEvent, event.NewDeltaEvent(msg.Content()))
	for _, tc := range msg.ToolCalls() {
		emit(onEvent, event.NewToolCallEvent(tc))
	}
	emit(onEvent, event.NewFinishedEvent(msg.FinishReason(), usage))
	return nil
}

func emit(h event.TurnEventHandler, e event.TurnEvent) {
	if h != nil {
		h(e)
	}
}

func (s *Service) logAppend(ctx context.Context, kind string, v any) {
	if s.log != nil {
		_ = s.log.Append(ctx, kind, v) // diagnostics must never break a send
	}
}
