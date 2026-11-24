package job

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

// Tests stub the backoff to keep the package fast — the loop's
// attempt counting, not its wall-clock pacing, is what matters here.
func init() {
	sendRetryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
}

// flakySvc fails its first scripted calls with the given status before
// answering — the attempt count proves the retry loop engaged.
type flakySvc struct {
	calls  int32
	fails  int32
	status int
}

func (s *flakySvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	if atomic.AddInt32(&s.calls, 1) <= s.fails {
		return apperror.New(s.status, "upstream unreachable", errors.New("dial failed"))
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

// A transient 5xx clears on its own: the turn retries inside the same
// call and completes — the user never sees the blip.
func TestRunTurnRetriesTransientUpstreamFailure(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &flakySvc{fails: 2, status: 502}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	if want := int32(1 + len(sendRetryBackoff)); svc.calls != want {
		t.Fatalf("calls = %d, want %d (1 attempt + %d retries)", svc.calls, want, len(sendRetryBackoff))
	}
	if last := conv.Last(); last == nil || last.Content() != "done" {
		t.Fatalf("reply = %+v", conv.Last())
	}
}

// Each retry pause is announced on the turn's stream before it starts — a
// provider asking for quiet reads as a pause, not as a stalled reply.
func TestRunTurnAnnouncesRetryWait(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &flakySvc{fails: 2, status: 503}
	var waits []enum.TurnStatus
	onEvent := func(e event.TurnEvent) {
		if e.Kind() == enum.TurnEventStatus {
			waits = append(waits, e.Status())
		}
	}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, onEvent, nil); err != nil {
		t.Fatal(err)
	}
	if len(waits) != len(sendRetryBackoff) {
		t.Fatalf("retry_wait announcements = %d, want one per retry (%d)", len(waits), len(sendRetryBackoff))
	}
	for _, s := range waits {
		if s != enum.TurnStatusRetryWait {
			t.Fatalf("status = %q, want %q", s, enum.TurnStatusRetryWait)
		}
	}
}

// Client statuses never clear on their own — one call and out.
func TestRunTurnNoRetryOnClientError(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &flakySvc{fails: 9, status: 400}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil); err == nil {
		t.Fatal("a definitive provider error must fail the turn")
	}
	if svc.calls != 1 {
		t.Fatalf("calls = %d, want 1 — client errors are not retried", svc.calls)
	}
}

// A provider that stays down exhausts the bounded retries and the turn
// fails — the backlog never stalls on it.
func TestRunTurnRetryExhaustion(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &flakySvc{fails: 9, status: 503}
	err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil)
	if err == nil {
		t.Fatal("exhausted retries must fail the turn")
	}
	if want := int32(1 + len(sendRetryBackoff)); svc.calls != want {
		t.Fatalf("calls = %d, want %d", svc.calls, want)
	}
}

// emitThenFailSvc streams a delta, then fails mid-read on every attempt —
// the retry loop's exhaustion path.
type emitThenFailSvc struct {
	calls  int32
	status int
}

func (s *emitThenFailSvc) Send(_ context.Context, _ *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, emit event.TurnEventHandler) error {
	atomic.AddInt32(&s.calls, 1)
	emit(event.NewDeltaEvent("partial"))
	return apperror.New(s.status, "upstream stream broken", errors.New("mid-read failure"))
}

// emittedThenAnswerSvc streams a partial answer, dies, then answers on the
// next attempt — the mid-answer 429 the retry gate exists for.
type emittedThenAnswerSvc struct {
	calls int32
	fail  int32
}

func (s *emittedThenAnswerSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, emit event.TurnEventHandler) error {
	n := atomic.AddInt32(&s.calls, 1)
	if n <= s.fail {
		emit(event.NewDeltaEvent("abandoned tail"))
		return apperror.New(429, "rate limited", errors.New("too many requests"))
	}
	emit(event.NewDeltaEvent("whole answer"))
	m, _ := entity.NewMessage(enum.RoleAssistant, "whole answer")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

// appendThenFailSvc appends a message and then fails — an adapter that
// breaks the one-append-per-call contract. An appended message is the one
// thing that makes a transient failure definitive.
type appendThenFailSvc struct {
	calls int32
}

func (s *appendThenFailSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	atomic.AddInt32(&s.calls, 1)
	m, _ := entity.NewMessage(enum.RoleAssistant, "already appended")
	conv.Append(m)
	return apperror.New(502, "upstream unreachable", errors.New("dial failed"))
}

// A transient failure after streamed output retries: the attempt committed
// nothing, and the client drops the abandoned tail on retry_wait. The
// store ends with exactly one assistant line — the surviving attempt's.
func TestRunTurnRetriesAfterStreamedDeltas(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &emittedThenAnswerSvc{fail: 1}
	var waits int
	onEvent := func(e event.TurnEvent) {
		if e.Kind() == enum.TurnEventStatus && e.Status() == enum.TurnStatusRetryWait {
			waits++
		}
	}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, onEvent, nil); err != nil {
		t.Fatalf("a transient mid-answer failure must be retried: %v", err)
	}
	if svc.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one retry)", svc.calls)
	}
	if waits != 1 {
		t.Fatalf("retry_wait announcements = %d, want 1", waits)
	}
	assistants := 0
	for _, m := range conv.Messages() {
		if m.Role() != enum.RoleAssistant {
			continue
		}
		assistants++
		if m.Content() != "whole answer" || m.FinishReason() != enum.FinishReasonStop {
			t.Fatalf("assistant line = %q/%q, want only the surviving attempt", m.Content(), m.FinishReason())
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant lines = %d, want 1 — the abandoned attempt must not persist", assistants)
	}
}

// An appended message — not streamed output — makes a transient failure
// definitive: a second call would append a second line.
func TestRunTurnNoRetryAfterAppendedMessage(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &appendThenFailSvc{}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil); err == nil {
		t.Fatal("a failure after an appended message must fail the turn")
	}
	if svc.calls != 1 {
		t.Fatalf("calls = %d, want 1 — an appended message blocks retry", svc.calls)
	}
}

// When the retries run out, the last attempt's shown text is kept as
// exactly one interrupted reply — never one line per attempt.
func TestRunTurnExhaustedRetriesKeepOneInterruptedReply(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &emitThenFailSvc{status: 503}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil); err == nil {
		t.Fatal("exhausted retries must fail the turn")
	}
	if want := int32(1 + len(sendRetryBackoff)); svc.calls != want {
		t.Fatalf("calls = %d, want %d", svc.calls, want)
	}
	reloaded, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	for label, c := range map[string]*aggregate.Conversation{"memory": conv, "stored": reloaded} {
		var assistants []entity.Message
		for _, m := range c.Messages() {
			if m.Role() == enum.RoleAssistant {
				assistants = append(assistants, m)
			}
		}
		if len(assistants) != 1 {
			t.Fatalf("%s: assistant lines = %d, want exactly one interrupted reply", label, len(assistants))
		}
		if got := assistants[0]; got.FinishReason() != enum.FinishReasonInterrupted || got.Content() != "partial" {
			t.Fatalf("%s: reply = %q/%q, want the last attempt's text as interrupted", label, got.Content(), got.FinishReason())
		}
	}
}

// The text a failed stream already showed the owner survives the failure
// as an interrupted reply, in memory and on disk. Without it a Retry
// regenerates the answer and the partial the reader watched vanishes.
func TestRunTurnKeepsStreamedTextOnFailure(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &emitThenFailSvc{status: 502}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil); err == nil {
		t.Fatal("a mid-stream failure must fail the turn")
	}
	check := func(label string, c *aggregate.Conversation) {
		t.Helper()
		last := c.Last()
		if last == nil || last.Role() != enum.RoleAssistant || last.Content() != "partial" {
			t.Fatalf("%s: tail = %+v, want the streamed partial reply", label, last)
		}
		if last.FinishReason() != enum.FinishReasonInterrupted {
			t.Fatalf("%s: finish = %q, want interrupted", label, last.FinishReason())
		}
	}
	check("in memory", conv)
	reloaded, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	check("reloaded", reloaded)
}

// Only answer text makes a kept reply: tool-call parts point at calls that
// never reached history, and reasoning alone has nothing a Retry can replay.
// Dropping the call must not leave two adjacent text parts — SetParts
// canonicalizes them into one stretch, so a renderer sees one block.
func TestInterruptedReplyKeepsAnswerTextOnly(t *testing.T) {
	msg, ok, err := interruptedReply([]entity.MessagePart{
		{Kind: entity.MessagePartReasoning, Text: "considering"},
		{Kind: entity.MessagePartText, Text: "Hel"},
		{Kind: entity.MessagePartToolCall, ToolCallID: "call_1"},
		{Kind: entity.MessagePartText, Text: "lo"},
	})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want a kept reply", ok, err)
	}
	if msg.Content() != "Hello" || msg.FinishReason() != enum.FinishReasonInterrupted {
		t.Fatalf("reply = %q/%q, want Hello/interrupted", msg.Content(), msg.FinishReason())
	}
	got := msg.Parts()
	if len(got) != 2 {
		t.Fatalf("parts = %+v, want reasoning + one merged text part with the call dropped", got)
	}
	if got[0].Kind != entity.MessagePartReasoning || got[1].Kind != entity.MessagePartText || got[1].Text != "Hello" {
		t.Fatalf("parts = %+v, want reasoning then text Hello", got)
	}
	if _, ok, _ := interruptedReply([]entity.MessagePart{{Kind: entity.MessagePartReasoning, Text: "thinking"}}); ok {
		t.Fatal("reasoning without answer text must not be kept")
	}
}

// A 413 after emitted output can't compact-and-retry either — the
// delta already crossed the wire, so the overflow error propagates.
func TestRunTurn413AfterEmittedDeltaPropagates(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv, _ := aggregate.NewConversation("room")
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	// A tail longer than keepTail arms the compact-retry branch —
	// only the emitted delta stands between it and the fold.
	for _, seed := range []struct {
		role enum.Role
		text string
	}{
		{enum.RoleUser, "hi"},
		{enum.RoleAssistant, "first answer"},
		{enum.RoleUser, "follow-up"},
		{enum.RoleAssistant, "second answer"},
		{enum.RoleUser, "again"},
	} {
		m, _ := entity.NewMessage(seed.role, seed.text)
		conv.Append(m)
		if _, err := d.Convs.AppendMessage(t.Context(), "room", m); err != nil {
			t.Fatal(err)
		}
	}
	svc := &emitThenFailSvc{status: 413}
	err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil)
	if apperror.As(err).StatusCode() != 413 {
		t.Fatalf("post-delta 413 must propagate, got %v", err)
	}
	if svc.calls != 1 {
		t.Fatalf("compact-retry fired despite the emitted delta: calls=%d", svc.calls)
	}
}

// A 429 carrying Retry-After is an instruction, not a hint we may ignore:
// the next attempt must wait at least that long. The fixed sendRetryBackoff
// (1ms in tests) must not fire first and hammer a provider that asked for
// quiet.
type retryAfterSvc struct {
	calls int32
}

type retryAfterError struct {
	err   error
	delay time.Duration
}

func (e retryAfterError) Error() string             { return e.err.Error() }
func (e retryAfterError) Unwrap() error             { return e.err }
func (e retryAfterError) RetryAfter() time.Duration { return e.delay }

func (s *retryAfterSvc) Send(_ context.Context, _ *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	atomic.AddInt32(&s.calls, 1)
	return retryAfterError{err: apperror.New(429, "rate limited", errors.New("too many requests")), delay: 5 * time.Second}
}

func TestRunTurnHonorsRetryAfter(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &retryAfterSvc{}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := d.runTurn(ctx, "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil)
	if apperror.As(err).StatusCode() != 408 {
		t.Fatalf("retry-after wait must end as a canceled turn, got %v", err)
	}
	if svc.calls != 1 {
		t.Fatalf("calls = %d, want 1 — Retry-After forbids an early retry", svc.calls)
	}
}

// A provider asking for a wait longer than a turn may sit out is not retried:
// the 429 surfaces immediately, still carrying the wait so the terminal can
// tell the owner when the route is usable again.
func TestRunTurnLongRetryAfterFailsImmediately(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &auditDelaySvc{status: 429, delay: maxRateLimitWait + time.Minute}
	err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil)
	if err == nil {
		t.Fatal("a wait past maxRateLimitWait must fail the turn")
	}
	var delayed interface{ RetryAfter() time.Duration }
	if !errors.As(err, &delayed) {
		t.Fatalf("error %v lost the provider wait", err)
	}
	if got := delayed.RetryAfter(); got != maxRateLimitWait+time.Minute {
		t.Fatalf("retry-after = %v, want the provider's %v", got, maxRateLimitWait+time.Minute)
	}
	if svc.calls != 1 {
		t.Fatalf("calls = %d, want 1 — the wait is too long to sit out", svc.calls)
	}
}

// When every attempt is a 429 asking for the same quiet, the exhausted turn
// still reports the last wait.
func TestRunTurnExhaustionKeepsProviderWait(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	// Short enough to keep the suite fast; the value survives, not the wait.
	svc := &auditDelaySvc{status: 429, delay: 20 * time.Millisecond}
	err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil)
	if err == nil {
		t.Fatal("exhausted retries must fail the turn")
	}
	var delayed interface{ RetryAfter() time.Duration }
	if !errors.As(err, &delayed) || delayed.RetryAfter() != 20*time.Millisecond {
		t.Fatalf("error %v lost the provider wait", err)
	}
	if want := int32(1 + len(sendRetryBackoff)); svc.calls != want {
		t.Fatalf("calls = %d, want %d", svc.calls, want)
	}
}

// auditDelaySvc fails every call with a status plus a provider-asked wait.
type auditDelaySvc struct {
	calls  int32
	status int
	delay  time.Duration
}

func (s *auditDelaySvc) Send(_ context.Context, _ *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	atomic.AddInt32(&s.calls, 1)
	return retryAfterError{
		err:   apperror.FromStatus(s.status, "rate limited", errors.New("too many requests")),
		delay: s.delay,
	}
}
