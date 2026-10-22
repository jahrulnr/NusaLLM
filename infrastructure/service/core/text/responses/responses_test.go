package textresponses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// newService spins an httptest server and a fake gateway pointing at it.
func newService(t *testing.T, stream bool, handler http.HandlerFunc) (*Service, *aggregate.Gateway, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	gw, err := aggregate.NewGateway("test", "t", srv.URL+"/v1", enum.StyleResponses)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk-test")
	if stream {
		gw.EnableStream()
	} else {
		gw.DisableStream()
	}
	return New(coretext.Options{FileStore: repoworkspace.NewLocal()}), &gw, srv
}

func newConv(t *testing.T) *aggregate.Conversation {
	t.Helper()
	c, err := aggregate.NewConversation("c1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func events() (*[]event.TurnEvent, event.TurnEventHandler) {
	var got []event.TurnEvent
	return &got, func(e event.TurnEvent) { got = append(got, e) }
}

func decodeReq(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUnaryRoundtrip(t *testing.T) {
	var captured map[string]any
	var gotPath string
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Error("missing bearer auth")
		}
		captured = decodeReq(t, r)
		w.Header().Set("x-request-id", "req-1")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_1", "status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "hello there"}},
			}},
			"usage": map[string]any{"input_tokens": 1000, "output_tokens": 50,
				"input_tokens_details": map[string]any{"cached_tokens": 900}},
		})
	})

	conv := newConv(t)
	conv.SetSystemPrompt("be brief")
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	tool, _ := valueobject.NewToolSchema("get_time", "current time", json.RawMessage(`{"type":"object"}`), true, enum.ToolPermissionAgent)
	hidden, _ := valueobject.NewToolSchema("sys", "", nil, true, enum.ToolPermissionSystem)
	conv.SetTools([]valueobject.ToolSchema{tool, hidden})

	model, _ := entity.NewModel("gpt-x", "test")
	half := 0.5
	model.SetTemperature(&half)

	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}

	// Wire request shape.
	if gotPath != "/v1/responses" {
		t.Fatalf("path: %s", gotPath)
	}
	if captured["model"] != "gpt-x" || captured["store"] != false {
		t.Fatalf("params: %+v", captured)
	}
	if captured["instructions"] != "be brief" {
		t.Fatalf("instructions: %+v", captured["instructions"])
	}
	if captured["prompt_cache_key"] != conv.ID() {
		t.Fatalf("prompt_cache_key: %+v", captured["prompt_cache_key"])
	}
	inc, _ := captured["include"].([]any)
	if len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Fatalf("include: %+v", captured["include"])
	}
	if captured["temperature"] != 0.5 {
		t.Fatalf("temperature: %+v", captured["temperature"])
	}
	items, _ := captured["input"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["type"] != "message" || items[0].(map[string]any)["role"] != "user" {
		t.Fatalf("input: %+v", items)
	}
	tools, _ := captured["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "get_time" {
		t.Fatal("system-permission tool leaked onto wire")
	}
	if tools[0].(map[string]any)["parameters"] == nil {
		t.Fatal("parameters missing")
	}

	// Domain effects.
	last := conv.Last()
	if last == nil || last.Content() != "hello there" || last.FinishReason() != enum.FinishReasonStop {
		t.Fatalf("message: %+v", last)
	}
	// Inclusive usage: input total 1000, cached 900 → uncached 100.
	if conv.Usage().InputTokens() != 100 || conv.Usage().CacheReadTokens() != 900 {
		t.Fatalf("usage not normalized: %+v", conv.Usage())
	}
	if len(*ev) != 2 || (*ev)[0].Kind() != enum.TurnEventDelta || (*ev)[1].Kind() != enum.TurnEventFinished {
		t.Fatalf("events: %+v", *ev)
	}
	if last.OptionalContent()["responses.id"] != "resp_1" {
		t.Fatal("response id not stored")
	}
}

func TestToolCallRoundtrip(t *testing.T) {
	var captured2 map[string]any
	call := 0
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "resp_1", "status": "completed",
				"output": []any{
					map[string]any{"type": "message", "role": "assistant",
						"content": []any{map[string]any{"type": "output_text", "text": "checking"}}},
					map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_time", "arguments": `{"tz":"UTC"}`},
				},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
			})
			return
		}
		captured2 = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_2", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "it is noon"}}}},
		})
	})

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "what time")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")

	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	last := conv.Last()
	if last == nil || last.FinishReason() != enum.FinishReasonToolCalls {
		t.Fatalf("finish: %+v", last)
	}
	tcs := last.ToolCalls()
	if len(tcs) != 1 || tcs[0].ID() != "call_1" || tcs[0].Name() != "get_time" {
		t.Fatalf("tool calls: %+v", tcs)
	}
	if !json.Valid([]byte(tcs[0].Input())) || !strings.Contains(tcs[0].Input(), `"tz":"UTC"`) {
		t.Fatalf("input not raw JSON: %s", tcs[0].Input())
	}

	// Execute the tool and send again — replay must carry function_call +
	// function_call_output items pairing on call_id.
	if err := last.SetToolOutput("call_1", "12:00"); err != nil {
		t.Fatal(err)
	}
	m2, _ := entity.NewMessage(enum.RoleUser, "thanks?")
	conv.Append(m2)
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}

	items, _ := captured2["input"].([]any)
	var fc, fco map[string]any
	for _, raw := range items {
		it := raw.(map[string]any)
		switch it["type"] {
		case "function_call":
			fc = it
		case "function_call_output":
			fco = it
		}
	}
	if fc == nil || fc["call_id"] != "call_1" || fc["name"] != "get_time" || fc["arguments"] != `{"tz":"UTC"}` {
		t.Fatalf("function_call replay: %+v", fc)
	}
	if fco == nil || fco["call_id"] != "call_1" ||
		!strings.HasPrefix(fco["output"].(string), `<untrusted_tool latency="`) ||
		!strings.Contains(fco["output"].(string), "12:00") ||
		!strings.HasSuffix(fco["output"].(string), "</untrusted_tool>") {
		t.Fatalf("function_call_output: %+v", fco)
	}
}

func TestReasoningReplaysVerbatim(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r2", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		})
	})
	conv := newConv(t)
	asst, _ := entity.NewMessage(enum.RoleAssistant, "answer")
	asst.SetOptionalContent(itemsKey, `[
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"hmm"}],"encrypted_content":"ENC=="},
		{"type":"message"},
		{"type":"reasoning","id":"rs_2","summary":[{"type":"summary_text","text":"dropped"}]},
		{"type":"web_search_call","id":"ws_1","status":"completed"}
	]`)
	conv.Append(asst)
	m, _ := entity.NewMessage(enum.RoleUser, "next")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	items, _ := captured["input"].([]any)
	// Expect: reasoning(with enc) verbatim, refilled message, reasoning
	// WITHOUT encrypted_content dropped, server item verbatim.
	if len(items) != 4 { // 3 assistant items + the user message
		t.Fatalf("items: %+v", items)
	}
	r0 := items[0].(map[string]any)
	if r0["type"] != "reasoning" || r0["encrypted_content"] != "ENC==" || r0["id"] != "rs_1" {
		t.Fatalf("reasoning replay: %+v", r0)
	}
	if items[1].(map[string]any)["type"] != "message" || items[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("message refill: %+v", items[1])
	}
	if items[2].(map[string]any)["type"] != "web_search_call" || items[2].(map[string]any)["id"] != "ws_1" {
		t.Fatalf("server item replay: %+v", items[2])
	}
	if items[3].(map[string]any)["role"] != "user" {
		t.Fatalf("user item: %+v", items[3])
	}
	for _, raw := range items {
		if it := raw.(map[string]any); it["id"] == "rs_2" {
			t.Fatal("reasoning without encrypted_content must be dropped")
		}
	}
}

const streamFixture = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"let me "}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"think"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"let me think"}],"encrypted_content":"ENC=="}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","role":"assistant","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":1,"delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":1,"delta":" there"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"let me think"}],"encrypted_content":"ENC=="},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]}],"usage":{"input_tokens":100,"output_tokens":15,"input_tokens_details":{"cached_tokens":40}}}}

`

func TestStreamRoundtrip(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		var captured map[string]any
		json.NewDecoder(r.Body).Decode(&captured)
		if captured["stream"] != true {
			t.Error("stream flag missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(streamFixture))
	})

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")

	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}

	last := conv.Last()
	if last == nil || last.Content() != "Hello there" || last.FinishReason() != enum.FinishReasonStop {
		t.Fatalf("message: %+v", last)
	}
	// Inclusive: 100 total, 40 cached → 60 uncached.
	if conv.Usage().InputTokens() != 60 || conv.Usage().OutputTokens() != 15 || conv.Usage().CacheReadTokens() != 40 {
		t.Fatalf("usage: %+v", conv.Usage())
	}
	var kinds []enum.TurnEventKind
	for _, e := range *ev {
		kinds = append(kinds, e.Kind())
	}
	if kinds[0] != enum.TurnEventReasoningSummary || kinds[1] != enum.TurnEventReasoningSummary {
		t.Fatalf("reasoning events: %+v", kinds)
	}
	if kinds[len(kinds)-1] != enum.TurnEventFinished {
		t.Fatalf("last event: %+v", kinds)
	}

	// Replay template: reasoning item stored verbatim with the blob.
	var template []map[string]any
	raw := last.OptionalContent()[itemsKey]
	if raw == "" {
		t.Fatal("output template not stored")
	}
	json.Unmarshal([]byte(raw), &template)
	if len(template) != 2 || template[0]["type"] != "reasoning" || template[0]["encrypted_content"] != "ENC==" {
		t.Fatalf("template: %s", raw)
	}
}

func TestStreamToolCall(t *testing.T) {
	fixture := `event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"get_time","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"tz\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"UTC\"}"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"get_time","arguments":"{\"tz\":\"UTC\"}"}],"usage":{"input_tokens":10,"output_tokens":8}}}

`
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(fixture))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}
	last := conv.Last()
	tcs := last.ToolCalls()
	if len(tcs) != 1 || tcs[0].ID() != "call_1" || tcs[0].Input() != `{"tz":"UTC"}` {
		t.Fatalf("streamed tool call: %+v", tcs)
	}
	if last.FinishReason() != enum.FinishReasonToolCalls {
		t.Fatalf("finish: %v", last.FinishReason())
	}
	var toolEvents int
	for _, e := range *ev {
		if e.Kind() == enum.TurnEventToolCall {
			toolEvents++
		}
	}
	if toolEvents != 1 {
		t.Fatalf("tool events: %+v", *ev)
	}
}

func TestIncompleteMapsLength(t *testing.T) {
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r", "status": "incomplete",
			"output":             []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "cut o"}}}},
			"incomplete_details": map[string]any{"reason": "max_output_tokens"},
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if conv.Last().FinishReason() != enum.FinishReasonLength {
		t.Fatalf("finish: %v", conv.Last().FinishReason())
	}
}

func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{429, `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, 429},
		{401, `{"error":{"message":"bad key","type":"authentication_error","code":"invalid_api_key"}}`, 401},
		{400, `{"error":{"message":"Your input exceeds the context window","code":"context_length_exceeded","param":"input"}}`, 413},
		{500, `{"error":{"message":"oops","type":"server_error"}}`, 500},
	} {
		svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		})
		conv := newConv(t)
		m, _ := entity.NewMessage(enum.RoleUser, "hi")
		conv.Append(m)
		model, _ := entity.NewModel("gpt-x", "test")
		err := svc.Send(context.Background(), conv, gw, model, nil)
		if err == nil {
			t.Fatalf("status %d: want error", tc.status)
		}
		ae, ok := err.(apperror.IAppError)
		if !ok || ae.StatusCode() != tc.want {
			t.Fatalf("status %d → %v (want %d)", tc.status, err, tc.want)
		}
	}
}

func TestStreamTruncated(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// text delta but no response.completed — server cut.
		w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"hi\"}\n\n"))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	err := svc.Send(context.Background(), conv, gw, model, nil)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("want truncated stream error, got %v", err)
	}
	if conv.Last() != nil && conv.Last().Role() == enum.RoleAssistant {
		t.Fatal("truncated stream must not commit a message")
	}
}

func TestStreamMidError(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: error\ndata: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"slow down\"}\n\n"))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	err := svc.Send(context.Background(), conv, gw, model, nil)
	ae, ok := err.(apperror.IAppError)
	if !ok || ae.StatusCode() != 429 {
		t.Fatalf("want 429, got %v", err)
	}
}

// A response.failed event with no message still names the provider's
// error code — the shared FailureMessage fallback wording.
func TestStreamFailedEventFallback(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"r1\",\"status\":\"failed\",\"error\":{\"code\":\"internal_error\"}}}\n\n"))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	err := svc.Send(context.Background(), conv, gw, model, nil)
	ae, ok := err.(apperror.IAppError)
	if !ok || ae.StatusCode() != 502 {
		t.Fatalf("want 502, got %v", err)
	}
	if got := ae.Error(); got != "provider rejected the request (internal_error)" {
		t.Fatalf("message = %q", got)
	}
}

// A failed unary response with no error message surfaces the same
// fallback — the provider's code becomes the parenthetical.
func TestUnaryFailedFallback(t *testing.T) {
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r1", "status": "failed",
			"error": map[string]any{"code": "server_error"},
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	err := svc.Send(context.Background(), conv, gw, model, nil)
	ae, ok := err.(apperror.IAppError)
	if !ok || ae.StatusCode() != 502 {
		t.Fatalf("want 502, got %v", err)
	}
	if got := ae.Error(); got != "provider rejected the request (server_error)" {
		t.Fatalf("message = %q", got)
	}
}

// A failed unary response routes like the streamed response.failed
// event — the provider's own code/message remaps to the status the
// application routes on.
func TestUnaryFailedRemaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  map[string]any
		want int
	}{
		{"rate limit", map[string]any{"code": "rate_limit_exceeded", "message": "slow down"}, 429},
		{"context overflow", map[string]any{"code": "context_length_exceeded", "message": "input exceeds the maximum context"}, 413},
	} {
		svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "r1", "status": "failed",
				"error": tc.err,
			})
		})
		conv := newConv(t)
		m, _ := entity.NewMessage(enum.RoleUser, "hi")
		conv.Append(m)
		model, _ := entity.NewModel("gpt-x", "test")
		err := svc.Send(context.Background(), conv, gw, model, nil)
		ae, ok := err.(apperror.IAppError)
		if !ok || ae.StatusCode() != tc.want {
			t.Fatalf("%s: want %d, got %v", tc.name, tc.want, err)
		}
	}
}

func TestEncodeAttachmentRejectsPinnedRemoteFileURL(t *testing.T) {
	a, _ := valueobject.NewAttachment("https://example.com/doc.pdf", "application/pdf")
	a.SetSHA256(strings.Repeat("a", 64))
	if _, err := encodeAttachment(a, wire.MediaRoot{}); err == nil {
		t.Fatal("pinned remote https file attachment must fail")
	}
}

func TestImageFileAttachmentBecomesDataURI(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47} // tiny png stub
	path := filepath.Join(t.TempDir(), "x.png")
	os.WriteFile(path, img, 0o644)

	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		})
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "look")
	a, _ := valueobject.NewAttachment("file://"+path, "image/png")
	m.AddAttachment(a)
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText, enum.ModalityImage})
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	items, _ := captured["input"].([]any)
	parts := items[0].(map[string]any)["content"].([]any)
	imgPart := parts[1].(map[string]any)
	if imgPart["type"] != "input_image" {
		t.Fatalf("part: %+v", imgPart)
	}
	url, _ := imgPart["image_url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image_url: %s", url)
	}
}

// Tool-output media rides inside its function_call_output — this wire
// carries parts there, so no role:"user" item may be synthesized.
func TestToolOutputMediaInsideFunctionCallOutput(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47}
	path := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(path, img, 0o644)

	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		})
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("call_1", "read_media", `{}`, enum.TooltypeOrigin)
	m.AddToolCall(tc)
	m.SetToolOutput("call_1", "loaded")
	a, _ := valueobject.NewAttachment("file://"+path, "image/png")
	m.SetToolOutputAttachments("call_1", []valueobject.Attachment{a})
	conv.Append(m)

	model, _ := entity.NewModel("gpt-x", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText, enum.ModalityImage})
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	items, _ := captured["input"].([]any)
	var fco map[string]any
	for _, it := range items {
		m2 := it.(map[string]any)
		if m2["type"] == "message" && m2["role"] == "user" {
			t.Fatalf("no user item may carry tool media: %+v", m2)
		}
		if m2["type"] == "function_call_output" {
			fco = m2
		}
	}
	parts, ok := fco["output"].([]any)
	if !ok || len(parts) < 2 {
		t.Fatalf("output must be parts [text, image], got %+v", fco["output"])
	}
	if parts[0].(map[string]any)["type"] != "input_text" ||
		parts[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("output parts: %+v", parts)
	}
}

func TestAudioAttachmentOmitted(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "listen")
	a, _ := valueobject.NewAttachment("https://x.test/a.mp3", "audio/mpeg")
	m.AddAttachment(a)
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText, enum.ModalityAudio})
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	items, _ := captured["input"].([]any)
	parts := items[0].(map[string]any)["content"].([]any)
	if parts[1].(map[string]any)["type"] != "input_text" {
		t.Fatalf("audio must degrade to a text note, got %+v", parts[1])
	}
}

func TestEffortClampAndEscapeHatch(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gpt-x", "test")
	model.SetEffort(enum.EffortXHigh) // no wire equivalent → clamp high
	model.SetOptionalContent("responses.service_tier", `"flex"`)
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	reasoning, _ := captured["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("effort: %+v", reasoning)
	}
	// Effort implies a reasoning model — the summary opt-in rides with it
	// or its reasoning returns nothing the user can see.
	if reasoning["summary"] != "auto" {
		t.Fatalf("summary opt-in: %+v", reasoning)
	}
	if captured["service_tier"] != "flex" {
		t.Fatalf("escape hatch: %+v", captured["service_tier"])
	}
}

func TestNoEffortSendsNoReasoning(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "r", "status": "completed",
			"output": []any{map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	// Unset effort means provider default — a non-reasoning model must
	// never see a reasoning object (it would reject the unknown effort).
	model, _ := entity.NewModel("gpt-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if _, sent := captured["reasoning"]; sent {
		t.Fatalf("unset effort must not send reasoning: %+v", captured["reasoning"])
	}
}

func TestMergeIncludeKeepsDefaults(t *testing.T) {
	base := []string{"reasoning.encrypted_content"}
	cases := []struct {
		raw  string
		want []string
	}{
		{`["web_search_call.action.sources"]`, []string{"reasoning.encrypted_content", "web_search_call.action.sources"}},
		{`"web_search_call.action.sources"`, []string{"reasoning.encrypted_content", "web_search_call.action.sources"}},
		{"a, b", []string{"reasoning.encrypted_content", "a", "b"}},
		{`["reasoning.encrypted_content","a","a"]`, []string{"reasoning.encrypted_content", "a"}},
		{`[]`, []string{"reasoning.encrypted_content"}},
	}
	for _, tc := range cases {
		if got := mergeInclude(base, tc.raw); !slices.Equal(got, tc.want) {
			t.Errorf("mergeInclude(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// The system-role workspace pin encodes as the completed-call pair it
// records — a function_call item immediately answered by a
// function_call_output item — never a role:"system" input message.
func TestSystemHydrationEmitsCallPair(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_1", "status": "completed",
			"output": []any{map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "ok"}},
			}},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
		})
	})

	conv := newConv(t)
	u, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(u)
	pin, _ := entity.NewMessage(enum.RoleSystem, "")
	call, _ := entity.NewToolCall("call_ws", "workspace", `{"path":"/srv/ws"}`, enum.TooltypeInjection)
	call.SetStoredOutput("workspace changed to /srv/ws", "")
	pin.AddToolCall(call)
	conv.Append(pin)

	model, _ := entity.NewModel("gpt-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	input, _ := captured["input"].([]any)
	var callItem, outItem map[string]any
	for _, raw := range input {
		m := raw.(map[string]any)
		switch m["type"] {
		case "function_call":
			if m["name"] == "workspace" {
				callItem = m
			}
		case "function_call_output":
			if callItem != nil && m["call_id"] == callItem["call_id"] {
				outItem = m
			}
		case "message":
			if m["role"] == "system" {
				t.Fatalf("pin must not emit a role:system input item: %+v", input)
			}
			if c, ok := m["content"].([]any); ok {
				for _, p := range c {
					if pm, ok := p.(map[string]any); ok && pm["text"] == "" {
						t.Fatalf("empty output_text item emitted for pin turn")
					}
				}
			}
		}
	}
	if callItem == nil || outItem == nil {
		t.Fatalf("pin pair missing from input: %+v", input)
	}
	out, _ := outItem["output"].(string)
	if !strings.Contains(out, "workspace changed to /srv/ws") {
		t.Fatalf("pin output = %q", out)
	}
}
