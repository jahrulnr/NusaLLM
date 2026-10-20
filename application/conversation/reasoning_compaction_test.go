package appconversation

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	textmessages "github.com/jahrulnr/nusallm/infrastructure/service/core/text/messages"
	textresponses "github.com/jahrulnr/nusallm/infrastructure/service/core/text/responses"
)

// setFixtureModelOptional updates the catalog model the app will load on
// the next send. styleFixture stores the gateway in the real repository, so
// the mutated copy must be saved back before exercising the app.
func setFixtureModelOptional(t *testing.T, app *ConversationApp, gw *aggregate.Gateway, key, value string) {
	t.Helper()
	models := gw.Models()
	if len(models) != 1 {
		t.Fatalf("fixture has %d models, want 1", len(models))
	}
	models[0].SetOptionalContent(key, value)
	gw.SetModels(models)
	if err := app.deps.Gateways.Save(t.Context(), gw); err != nil {
		t.Fatal(err)
	}
}

func setFixtureKeepTail(app *ConversationApp, n int) {
	app.cfg.CompactKeepTail = n
	app.deps.Config.CompactKeepTail = n
}

// bodyHasThinking reports whether a messages request replays a thinking or
// redacted_thinking block. Unparseable bodies are treated as having none;
// the caller asserts the request shape separately.
func bodyHasThinking(body []byte) bool {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	for _, m := range req.Messages {
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				return true
			}
		}
	}
	return false
}

func writeMessagesResponse(w http.ResponseWriter, content string, stop string) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":`+
		content+`,"stop_reason":"`+stop+`","usage":{"input_tokens":10,"output_tokens":5}}`)
}

// TestMessagesCompactionSanitizesSignedThinking drives the real messages
// adapter through the application compaction path. The upstream rejects any
// thinking block in the summary request (the summary dialogue changes the
// tool prefix) and any thinking replay after the fold; the retained assistant
// turn must survive as text only.
func TestMessagesCompactionSanitizesSignedThinking(t *testing.T) {
	var calls atomic.Int32
	var replay []byte
	handler := func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		switch n {
		case 1:
			writeMessagesResponse(w, `[
				{"type":"thinking","thinking":"secret","signature":"SIG"},
				{"type":"redacted_thinking","data":"ENC"},
				{"type":"text","text":"one"}
			]`, "end_turn")
		case 2:
			writeMessagesResponse(w, `[{"type":"text","text":"two"}]`, "end_turn")
		case 3:
			if bodyHasThinking(raw) {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`)
				return
			}
			writeMessagesResponse(w, `[{"type":"tool_use","id":"sum_1","name":"`+appjob.SummaryToolName+`","input":{"text":"CHECKPOINT"}}]`, "tool_use")
		default:
			replay = raw
			writeMessagesResponse(w, `[{"type":"text","text":"three"}]`, "end_turn")
		}
	}

	app, gw := styleFixture(t, enum.StyleMessages, textmessages.New(coretext.Options{}), false, handler)
	setFixtureKeepTail(app, 4)

	for _, text := range []string{"first", "second"} {
		if _, err := app.Send(t.Context(), "room", "gw1", "m1", text, nil, appjob.SendOptions{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.Compact(t.Context(), "room"); err != nil {
		t.Fatalf("compaction with signed thinking must sanitize the scratch request: %v", err)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "third", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(replay) == 0 {
		t.Fatal("third send did not reach the upstream")
	}
	if bodyHasThinking(replay) {
		t.Fatalf("post-compaction replay retained signed thinking:\n%s", replay)
	}

	conv, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range conv.Messages() {
		raw := m.OptionalContent()[messagesBlocksKey]
		if raw == "" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
			t.Fatalf("stored blocks: %v\n%s", err, raw)
		}
		for _, b := range blocks {
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				t.Fatalf("compacted tail persisted %s: %s", b.Type, raw)
			}
		}
	}

	// The retained assistant text must survive the sanitization.
	if !strings.Contains(string(replay), `"one"`) {
		t.Fatalf("compaction dropped retained assistant text:\n%s", replay)
	}

	_ = gw // the fixture's gateway is only used to rewrite the stored model
}

func TestMessagesAnthropicBetaHeader(t *testing.T) {
	var (
		header string
		body   []byte
	)
	handler := func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("anthropic-beta")
		body, _ = io.ReadAll(r.Body)
		writeMessagesResponse(w, `[{"type":"text","text":"ok"}]`, "end_turn")
	}
	app, gw := styleFixture(t, enum.StyleMessages, textmessages.New(coretext.Options{}), false, handler)
	setFixtureModelOptional(t, app, gw, "messages.anthropic_beta",
		`["interleaved-thinking-2025-05-14","context-management-2025-06-27"]`)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if want := "interleaved-thinking-2025-05-14, context-management-2025-06-27"; header != want {
		t.Fatalf("anthropic-beta header = %q, want %q", header, want)
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v\n%s", err, body)
	}
	for _, key := range []string{"anthropic_beta", "anthropic-beta"} {
		if _, ok := req[key]; ok {
			t.Fatalf("beta selector leaked into the request body as %q:\n%s", key, body)
		}
	}
}

func TestResponsesIncludeKeepsEncryptedReasoning(t *testing.T) {
	var body []byte
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`)
	}
	app, gw := styleFixture(t, enum.StyleResponses, textresponses.New(coretext.Options{}), false, handler)
	setFixtureModelOptional(t, app, gw, "responses.include", `["web_search_call.action.sources"]`)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Include []string `json:"include"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode request: %v\n%s", err, body)
	}
	want := []string{"reasoning.encrypted_content", "web_search_call.action.sources"}
	if !slices.Equal(req.Include, want) {
		t.Fatalf("include = %v, want %v", req.Include, want)
	}
}

func TestResponsesNullEncryptedReasoningNotReplayed(t *testing.T) {
	handler, second := captureSecondRequest(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{
			  "id":"resp_1","status":"completed",
			  "output":[
			    {"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"hmm"}],"encrypted_content":null},
			    {"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}
			  ],
			  "usage":{"input_tokens":10,"output_tokens":2}
			}`)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"resp_2","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`)
		},
	)
	app, _ := styleFixture(t, enum.StyleResponses, textresponses.New(coretext.Options{}), false, handler)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hello", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Send(t.Context(), "room", "", "", "again", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	var req struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(second(), &req); err != nil {
		t.Fatalf("decode replay request: %v\n%s", err, second())
	}
	for _, item := range req.Input {
		var kind string
		json.Unmarshal(item["type"], &kind)
		if kind == "reasoning" {
			t.Fatalf("null encrypted reasoning replayed:\n%s", second())
		}
	}
}
