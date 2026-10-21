package appconversation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	textcompletion "github.com/jahrulnr/nusallm/infrastructure/service/core/text/completion"
	textmessages "github.com/jahrulnr/nusallm/infrastructure/service/core/text/messages"
	textresponses "github.com/jahrulnr/nusallm/infrastructure/service/core/text/responses"
)

const messagesBlocksKey = "messages.content_blocks"

const completionReasoningDetails = `[
  {"type":"reasoning.text","text":"pondering","signature":"SIG-TEXT","format":"anthropic-claude-v1","index":0},
  {"type":"reasoning.encrypted","data":"ENC-BLOB","format":"anthropic-claude-v1","index":1}
]`

// styleFixture wires the real JSONL repositories and the real adapter for
// one wire style behind a stub upstream. Tests drive it through the
// application's public Send path, so persistence and replay are exercised
// as one flow rather than as isolated adapter helpers.
func styleFixture(t *testing.T, style enum.Style, svc dservice.TextModelService, stream bool, handler http.HandlerFunc) (*ConversationApp, *aggregate.Gateway) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	gw, err := aggregate.NewGateway("gw1", "stub", srv.URL, style)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk-test")
	if stream {
		gw.EnableStream()
	} else {
		gw.DisableStream()
	}
	m, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{m})

	secrets, err := reposecret.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { secrets.Close() })
	gateways := repogateway.NewGatewayRepository(t.TempDir(), secrets)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}

	app := New(
		repoconversation.NewConversationRepository(t.TempDir()),
		gateways,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{style: svc},
		}},
		config.NewConfig(),
		nil,
		"",
		nil,
	)
	return app, &gw
}

// captureSecondRequest returns the body of the second provider call; the
// first call is served by first, later calls by later.
func captureSecondRequest(t *testing.T, first, later http.HandlerFunc) (http.HandlerFunc, func() []byte) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls int
		body  []byte
	)
	return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			if call == 1 {
				first(w, r)
				return
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request: %v", err)
			}
			mu.Lock()
			body = raw
			mu.Unlock()
			later(w, r)
		}, func() []byte {
			mu.Lock()
			defer mu.Unlock()
			return append([]byte(nil), body...)
		}
}

func assistantMessage(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var req struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode replay request: %v\n%s", err, body)
	}
	for _, m := range req.Messages {
		var role string
		if err := json.Unmarshal(m["role"], &role); err == nil && role == "assistant" {
			return m
		}
	}
	t.Fatalf("replay request has no assistant message:\n%s", body)
	return nil
}

func requireCompletionReasoningDetails(t *testing.T, body []byte) {
	t.Helper()
	assistant := assistantMessage(t, body)
	raw, ok := assistant["reasoning_details"]
	if !ok {
		t.Fatalf("replay dropped reasoning_details:\n%s", body)
	}
	var got, want any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode replayed reasoning_details: %v", err)
	}
	if err := json.Unmarshal([]byte(completionReasoningDetails), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed reasoning_details = %s, want %s", raw, completionReasoningDetails)
	}
}

func TestCompletionReasoningDetailsReplayUnary(t *testing.T) {
	var reasoning strings.Builder
	handler, second := captureSecondRequest(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"answer","reasoning":"pondering","reasoning_details":`+completionReasoningDetails+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"again"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
		},
	)
	app, _ := styleFixture(t, enum.StyleCompletion, textcompletion.New(coretext.Options{}), false, handler)

	onEvent := func(ev event.TurnEvent) {
		if ev.Kind() == enum.TurnEventReasoning {
			reasoning.WriteString(ev.Delta())
		}
	}
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, appjob.SendOptions{}, onEvent); err != nil {
		t.Fatal(err)
	}
	if got := reasoning.String(); !strings.Contains(got, "pondering") {
		t.Fatalf("reasoning event = %q, want it to contain pondering", got)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "again", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	requireCompletionReasoningDetails(t, second())
}

func completionSSE(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range chunks {
		io.WriteString(w, "data: "+chunk+"\n\n")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestCompletionReasoningDetailsReplayStream(t *testing.T) {
	var reasoning strings.Builder
	handler, second := captureSecondRequest(t,
		func(w http.ResponseWriter, _ *http.Request) {
			completionSSE(w,
				`{"choices":[{"index":0,"delta":{"reasoning":"pondering","reasoning_details":[{"type":"reasoning.text","text":"pondering","signature":"SIG-TEXT","format":"anthropic-claude-v1","index":0}]}}]}`,
				`{"choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.encrypted","data":"ENC-BLOB","format":"anthropic-claude-v1","index":1}]}}]}`,
				`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				`{"choices":[{"index":0,"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			completionSSE(w,
				`{"choices":[{"index":0,"delta":{"content":"again"}}]}`,
				`{"choices":[{"index":0,"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		},
	)
	app, _ := styleFixture(t, enum.StyleCompletion, textcompletion.New(coretext.Options{}), true, handler)

	onEvent := func(ev event.TurnEvent) {
		if ev.Kind() == enum.TurnEventReasoning {
			reasoning.WriteString(ev.Delta())
		}
	}
	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, appjob.SendOptions{}, onEvent); err != nil {
		t.Fatal(err)
	}
	if got := reasoning.String(); !strings.Contains(got, "pondering") {
		t.Fatalf("reasoning event = %q, want it to contain pondering", got)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "again", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	requireCompletionReasoningDetails(t, second())
}

func messagesSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		io.WriteString(w, event+"\n\n")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestMessagesOmittedThinkingReplaysRequiredField(t *testing.T) {
	handler, second := captureSecondRequest(t,
		func(w http.ResponseWriter, _ *http.Request) {
			messagesSSE(w,
				`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude","usage":{"input_tokens":10,"output_tokens":1}}}`,
				`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG"}}`,
				`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
				`event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
				`event: content_block_stop
data: {"type":"content_block_stop","index":1}`,
				`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
				`event: message_stop
data: {"type":"message_stop"}`,
			)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			messagesSSE(w,
				`event: message_start
data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude","usage":{"input_tokens":10,"output_tokens":1}}}`,
				`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"again"}}`,
				`event: content_block_stop
data: {"type":"content_block_stop","index":0}`,
				`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`event: message_stop
data: {"type":"message_stop"}`,
			)
		},
	)
	app, _ := styleFixture(t, enum.StyleMessages, textmessages.New(coretext.Options{}), true, handler)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "again", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}

	body := second()
	assistant := assistantMessage(t, body)
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(assistant["content"], &blocks); err != nil {
		t.Fatalf("assistant content is not blocks: %v\n%s", err, body)
	}
	for _, block := range blocks {
		var kind string
		json.Unmarshal(block["type"], &kind)
		if kind != "thinking" {
			continue
		}
		rawThinking, ok := block["thinking"]
		if !ok {
			t.Fatalf("thinking block omitted required thinking field:\n%s", body)
		}
		var thinking, signature string
		json.Unmarshal(rawThinking, &thinking)
		json.Unmarshal(block["signature"], &signature)
		if thinking != "" || signature != "SIG" {
			t.Fatalf("thinking block = thinking %q signature %q, want empty text with SIG\n%s", thinking, signature, body)
		}
		return
	}
	t.Fatalf("replay request has no thinking block:\n%s", body)
}

func TestMessagesCancelDropsUnsignedThinking(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		messagesSSE(w,
			`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude","usage":{"input_tokens":10,"output_tokens":1}}}`,
			`event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"partial"}}`,
		)
		<-r.Context().Done()
	}
	app, _ := styleFixture(t, enum.StyleMessages, textmessages.New(coretext.Options{}), true, handler)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var canceled atomic.Bool
	onEvent := func(ev event.TurnEvent) {
		if ev.Kind() == enum.TurnEventReasoning && canceled.CompareAndSwap(false, true) {
			cancel()
		}
	}
	_, err := app.Send(ctx, "room", "gw1", "m1", "think", nil, appjob.SendOptions{}, onEvent)
	if got := apperror.As(err).StatusCode(); got != 408 {
		t.Fatalf("canceled send → %v (status %d), want 408", err, got)
	}
	if !canceled.Load() {
		t.Fatal("upstream thinking never reached the event handler")
	}

	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range conv.Messages() {
		if m.Role() != enum.RoleAssistant {
			continue
		}
		raw := m.OptionalContent()[messagesBlocksKey]
		if raw == "" {
			continue
		}
		var blocks []map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
			t.Fatalf("stored blocks: %v\n%s", err, raw)
		}
		for _, block := range blocks {
			var kind, signature string
			json.Unmarshal(block["type"], &kind)
			if kind != "thinking" {
				continue
			}
			json.Unmarshal(block["signature"], &signature)
			if signature == "" {
				t.Fatalf("canceled turn persisted an unsigned thinking block: %s", raw)
			}
		}
	}
}

func TestResponsesMessagePhaseAndTextReplay(t *testing.T) {
	handler, second := captureSecondRequest(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{
			  "id":"resp_1","status":"completed",
			  "output":[
			    {"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"commentary","content":[{"type":"output_text","text":"checking","annotations":[]}]},
			    {"type":"message","id":"msg_2","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"done","annotations":[]}]}
			  ],
			  "usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
			}`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp_2","status":"completed","output":[{"type":"message","id":"msg_3","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
		},
	)
	app, _ := styleFixture(t, enum.StyleResponses, textresponses.New(coretext.Options{}), false, handler)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "again", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}

	body := second()
	var req struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode replay request: %v\n%s", err, body)
	}
	type assistantItem struct {
		phase string
		text  string
	}
	var got []assistantItem
	for _, item := range req.Input {
		var kind, role string
		json.Unmarshal(item["type"], &kind)
		json.Unmarshal(item["role"], &role)
		if kind != "message" || role != "assistant" {
			continue
		}
		var phase string
		if raw, ok := item["phase"]; ok {
			json.Unmarshal(raw, &phase)
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		json.Unmarshal(item["content"], &parts)
		var text strings.Builder
		for _, part := range parts {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
		got = append(got, assistantItem{phase: phase, text: text.String()})
	}
	want := []assistantItem{
		{phase: "commentary", text: "checking"},
		{phase: "final_answer", text: "done"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed assistant messages = %+v, want %+v\nrequest:\n%s", got, want, body)
	}
}
