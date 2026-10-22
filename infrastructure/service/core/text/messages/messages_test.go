package textmessages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	gw, err := aggregate.NewGateway("test", "t", srv.URL+"/v1", enum.StyleMessages)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk-ant-test")
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
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Error("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version header")
		}
		captured = decodeReq(t, r)
		w.Header().Set("request-id", "req-1")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-x",
			"content":     []any{map[string]any{"type": "text", "text": "hello there"}},
			"stop_reason": "end_turn",
			"usage": map[string]any{"input_tokens": 100, "output_tokens": 50,
				"cache_read_input_tokens": 900, "cache_creation_input_tokens": 20},
		})
	})

	conv := newConv(t)
	conv.SetSystemPrompt("be brief")
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	tool, _ := valueobject.NewToolSchema("get_time", "current time", json.RawMessage(`{"type":"object"}`), true, enum.ToolPermissionAgent)
	hidden, _ := valueobject.NewToolSchema("sys", "", nil, true, enum.ToolPermissionSystem)
	conv.SetTools([]valueobject.ToolSchema{tool, hidden})

	model, _ := entity.NewModel("claude-x", "test")
	half := 0.5
	model.SetTemperature(&half)

	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}

	// Wire request shape.
	if gotPath != "/v1/messages" {
		t.Fatalf("path: %s", gotPath)
	}
	if captured["model"] != "claude-x" || captured["max_tokens"] != float64(defaultMaxTokens) {
		t.Fatalf("params: %+v", captured)
	}
	if captured["system"] != "be brief" {
		t.Fatalf("system: %+v", captured["system"])
	}
	if captured["temperature"] != 0.5 {
		t.Fatalf("temperature: %+v", captured["temperature"])
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages: %+v", msgs)
	}
	tools, _ := captured["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "get_time" {
		t.Fatal("system-permission tool leaked onto wire")
	}
	if tools[0].(map[string]any)["input_schema"] == nil {
		t.Fatal("input_schema missing")
	}

	// Domain effects.
	last := conv.Last()
	if last == nil || last.Content() != "hello there" || last.FinishReason() != enum.FinishReasonStop {
		t.Fatalf("message: %+v", last)
	}
	// Disjoint usage: input_tokens is already uncached.
	if conv.Usage().InputTokens() != 100 || conv.Usage().CacheReadTokens() != 900 || conv.Usage().CacheWriteTokens() != 20 {
		t.Fatalf("usage not normalized: %+v", conv.Usage())
	}
	if len(*ev) != 2 || (*ev)[0].Kind() != enum.TurnEventDelta || (*ev)[1].Kind() != enum.TurnEventFinished {
		t.Fatalf("events: %+v", *ev)
	}
}

func TestMaxTokensOverride(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "role": "assistant", "content": []any{}, "stop_reason": "end_turn",
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	mt := 4096
	model.SetMaxOutputTokens(&mt)
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if captured["max_tokens"] != float64(4096) {
		t.Fatalf("max_tokens: %+v", captured["max_tokens"])
	}
}

func TestToolUseRoundtrip(t *testing.T) {
	// First send: model returns a tool_use block.
	var captured2 map[string]any
	call := 0
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_1", "role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "checking"},
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "get_time", "input": map[string]any{"tz": "UTC"}},
				},
				"stop_reason": "tool_use",
				"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
			})
			return
		}
		captured2 = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_2", "role": "assistant",
			"content":     []any{map[string]any{"type": "text", "text": "it is noon"}},
			"stop_reason": "end_turn",
		})
	})

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "what time")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")

	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	last := conv.Last()
	if last == nil || last.FinishReason() != enum.FinishReasonToolCalls {
		t.Fatalf("finish: %+v", last)
	}
	tcs := last.ToolCalls()
	if len(tcs) != 1 || tcs[0].ID() != "toolu_1" || tcs[0].Name() != "get_time" {
		t.Fatalf("tool calls: %+v", tcs)
	}
	if !json.Valid([]byte(tcs[0].Input())) || !strings.Contains(tcs[0].Input(), `"tz":"UTC"`) {
		t.Fatalf("input not raw JSON: %s", tcs[0].Input())
	}

	// Execute the tool and send again — replay must carry tool_use +
	// tool_result, not a tool role.
	if err := last.SetToolOutput("toolu_1", "12:00"); err != nil {
		t.Fatal(err)
	}
	m2, _ := entity.NewMessage(enum.RoleUser, "thanks?")
	conv.Append(m2)
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}

	msgs, _ := captured2["messages"].([]any)
	var asst, toolResultTurn map[string]any
	for _, raw := range msgs {
		mm := raw.(map[string]any)
		if mm["role"] == "assistant" {
			asst = mm
		}
		if mm["role"] == "user" {
			if blocks, ok := mm["content"].([]any); ok {
				if b0, ok := blocks[0].(map[string]any); ok && b0["type"] == "tool_result" {
					toolResultTurn = mm
				}
			}
		}
	}
	if asst == nil {
		t.Fatal("assistant turn missing on replay")
	}
	blocks := asst["content"].([]any)
	if len(blocks) != 2 || blocks[1].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("assistant blocks: %+v", blocks)
	}
	tu := blocks[1].(map[string]any)
	if tu["id"] != "toolu_1" || tu["name"] != "get_time" {
		t.Fatalf("tool_use replay: %+v", tu)
	}
	if inp, ok := tu["input"].(map[string]any); !ok || inp["tz"] != "UTC" {
		t.Fatalf("tool_use input replay: %+v", tu["input"])
	}
	if toolResultTurn == nil {
		t.Fatal("tool_result user turn missing")
	}
	tr := toolResultTurn["content"].([]any)[0].(map[string]any)
	if tr["tool_use_id"] != "toolu_1" ||
		!strings.HasPrefix(tr["content"].(string), `<untrusted_tool latency="`) ||
		!strings.Contains(tr["content"].(string), "12:00") ||
		!strings.HasSuffix(tr["content"].(string), "</untrusted_tool>") {
		t.Fatalf("tool_result: %+v", tr)
	}
}

func TestInterruptedToolResult(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	})
	conv := newConv(t)
	asst, _ := entity.NewMessage(enum.RoleAssistant, "calling")
	tc, _ := entity.NewToolCall("toolu_9", "exec", `{"cmd":"ls"}`, enum.TooltypeOrigin)
	asst.AddToolCall(tc) // no output — interrupted turn
	conv.Append(asst)
	m, _ := entity.NewMessage(enum.RoleUser, "next")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := captured["messages"].([]any)
	found := false
	for _, raw := range msgs {
		blocks, _ := raw.(map[string]any)["content"].([]any)
		for _, b := range blocks {
			bb := b.(map[string]any)
			if bb["type"] == "tool_result" &&
				strings.HasPrefix(bb["content"].(string), "<untrusted_tool>\n(interrupted") &&
				strings.HasSuffix(bb["content"].(string), "</untrusted_tool>") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("interrupted tool_result placeholder missing: %+v", msgs)
	}
}

const streamFixture = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","content":[],"usage":{"input_tokens":25,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG9"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":" there"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":25,"output_tokens":15,"cache_read_input_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

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
	model, _ := entity.NewModel("claude-x", "test")

	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}

	last := conv.Last()
	if last == nil || last.Content() != "Hello there" || last.FinishReason() != enum.FinishReasonStop {
		t.Fatalf("message: %+v", last)
	}
	if conv.Usage().InputTokens() != 25 || conv.Usage().OutputTokens() != 15 || conv.Usage().CacheReadTokens() != 7 {
		t.Fatalf("usage: %+v", conv.Usage())
	}
	// Live events: reasoning chunks, text deltas, finished.
	var kinds []enum.TurnEventKind
	for _, e := range *ev {
		kinds = append(kinds, e.Kind())
	}
	if kinds[0] != enum.TurnEventReasoning || kinds[1] != enum.TurnEventReasoning {
		t.Fatalf("reasoning events: %+v", kinds)
	}
	if kinds[len(kinds)-1] != enum.TurnEventFinished {
		t.Fatalf("last event: %+v", kinds)
	}

	// Replay template: thinking block stored verbatim with signature.
	var template []map[string]any
	raw := last.OptionalContent()[blocksKey]
	if raw == "" {
		t.Fatal("block template not stored")
	}
	json.Unmarshal([]byte(raw), &template)
	if len(template) != 2 || template[0]["type"] != "thinking" || template[0]["signature"] != "SIG9" {
		t.Fatalf("template: %s", raw)
	}
	if template[0]["thinking"] != "let me think" {
		t.Fatalf("thinking text: %v", template[0]["thinking"])
	}
}

func TestThinkingReplaysVerbatim(t *testing.T) {
	// Seed a conversation whose assistant message carries a stored
	// template — the encode path must replay the thinking block bytes
	// untouched and refill text at its marked position.
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	})
	conv := newConv(t)
	asst, _ := entity.NewMessage(enum.RoleAssistant, "answer")
	asst.SetOptionalContent(blocksKey, `[{"type":"thinking","thinking":"hmm","signature":"SIG"},{"type":"text"},{"type":"redacted_thinking","data":"XYZ"}]`)
	conv.Append(asst)
	m, _ := entity.NewMessage(enum.RoleUser, "next")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := captured["messages"].([]any)
	blocks := msgs[0].(map[string]any)["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("blocks: %+v", blocks)
	}
	if blocks[0].(map[string]any)["signature"] != "SIG" || blocks[0].(map[string]any)["thinking"] != "hmm" {
		t.Fatalf("thinking replay: %+v", blocks[0])
	}
	if blocks[1].(map[string]any)["type"] != "text" || blocks[1].(map[string]any)["text"] != "answer" {
		t.Fatalf("text refill: %+v", blocks[1])
	}
	if blocks[2].(map[string]any)["type"] != "redacted_thinking" || blocks[2].(map[string]any)["data"] != "XYZ" {
		t.Fatalf("redacted replay: %+v", blocks[2])
	}
}

const streamToolFixture = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_time"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"UTC\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":8}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStreamToolUse(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(streamToolFixture))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}
	last := conv.Last()
	tcs := last.ToolCalls()
	if len(tcs) != 1 || tcs[0].ID() != "toolu_1" || tcs[0].Input() != `{"tz":"UTC"}` {
		t.Fatalf("streamed tool call: %+v", tcs)
	}
	if last.FinishReason() != enum.FinishReasonToolCalls {
		t.Fatalf("finish: %v", last.FinishReason())
	}
	// Tool call surfaces at commit, not mid-stream.
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

func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, 429},
		{401, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, 401},
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`, 413},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, 529},
	} {
		svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		})
		conv := newConv(t)
		m, _ := entity.NewMessage(enum.RoleUser, "hi")
		conv.Append(m)
		model, _ := entity.NewModel("claude-x", "test")
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
		// message_start + delta but no message_stop — server cut.
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"x\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
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
		w.Write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	err := svc.Send(context.Background(), conv, gw, model, nil)
	ae, ok := err.(apperror.IAppError)
	if !ok || ae.StatusCode() != 529 {
		t.Fatalf("want 529, got %v", err)
	}
}

func TestEncodeAttachmentRejectsPinnedRemoteURL(t *testing.T) {
	a, _ := valueobject.NewAttachment("https://example.com/x.png", "image/png")
	a.SetSHA256(strings.Repeat("a", 64))
	if _, err := encodeAttachment(a, wire.MediaRoot{}); err == nil {
		t.Fatal("pinned remote https attachment must fail")
	}
}

func TestImageFileAttachmentBecomesBase64(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47} // tiny png stub
	path := filepath.Join(t.TempDir(), "x.png")
	os.WriteFile(path, img, 0o644)

	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "look")
	a, _ := valueobject.NewAttachment("file://"+path, "image/png")
	m.AddAttachment(a)
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText, enum.ModalityImage})
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := captured["messages"].([]any)
	blocks := msgs[0].(map[string]any)["content"].([]any)
	imgBlock := blocks[1].(map[string]any)
	if imgBlock["type"] != "image" {
		t.Fatalf("block: %+v", imgBlock)
	}
	src := imgBlock["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] == "" {
		t.Fatalf("source: %+v", src)
	}
}

func TestAudioAttachmentOmitted(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "listen")
	a, _ := valueobject.NewAttachment("data:audio/mp3;base64,AAAA", "audio/mp3")
	m.AddAttachment(a)
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText, enum.ModalityAudio})
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := captured["messages"].([]any)
	blocks := msgs[0].(map[string]any)["content"].([]any)
	last := blocks[len(blocks)-1].(map[string]any)
	if last["type"] != "text" || !strings.Contains(last["text"].(string), "cannot carry") {
		t.Fatalf("audio must degrade to a text placeholder: %+v", last)
	}
}

func TestEffortAndEscapeHatch(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("claude-x", "test")
	model.SetEffort(enum.EffortXHigh)
	model.SetOptionalContent("messages.thinking", `{"type":"enabled","budget_tokens":4096}`)
	model.SetOptionalContent("completion.reasoning_effort", `"high"`) // other adapter's prefix — must not leak
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if captured["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Fatalf("output_config: %+v", captured["output_config"])
	}
	// The escape hatch lands verbatim — overriding the adapter's own
	// adaptive default for this id.
	th, ok := captured["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" || th["budget_tokens"] != float64(4096) {
		t.Fatalf("thinking escape hatch: %+v", captured["thinking"])
	}
	if _, leaked := captured["reasoning_effort"]; leaked {
		t.Fatal("completion.-prefixed param leaked onto messages wire")
	}
}

// Anthropic needs the explicit opt-in or reasoning never reaches the
// user — open-source reasoning models emit by default, claude does not.
// The generation split keeps the contract each era accepts: enabled
// budgets for ≤4.5, adaptive+summarized for current ids.
func TestThinkingOptIn(t *testing.T) {
	cases := []struct {
		name       string
		modelID    string
		effort     enum.Effort
		maxOut     *int
		want       map[string]any // nil → no thinking key on the wire
		wantEffort string         // "" → no output_config assertion
	}{
		{"adaptive-era asks for the summary by default", "claude-haiku-5-5", "", nil,
			map[string]any{"type": "adaptive", "display": "summarized"}, ""},
		{"adaptive-era ignores effort for the shape", "claude-haiku-5-5", enum.EffortMax, nil,
			map[string]any{"type": "adaptive", "display": "summarized"}, "max"},
		{"4.5-era rides the user's effort", "claude-sonnet-4-5", enum.EffortHigh, nil,
			map[string]any{"type": "enabled", "budget_tokens": float64(8192)}, "high"},
		{"4.5-era date suffix is not a minor", "claude-sonnet-4-5-20250929", enum.EffortMedium, nil,
			map[string]any{"type": "enabled", "budget_tokens": float64(4096)}, ""},
		{"missing minor reads as .0 — still enabled-era", "claude-sonnet-4-20250514", enum.EffortLow, nil,
			map[string]any{"type": "enabled", "budget_tokens": float64(2048)}, ""},
		{"namespaced prefix id qualifies", "anthropic/claude-sonnet-4-5", enum.EffortLow, nil,
			map[string]any{"type": "enabled", "budget_tokens": float64(2048)}, ""},
		{"minimal clamps to low on the wire", "claude-sonnet-4-5", enum.EffortMinimal, nil,
			map[string]any{"type": "enabled", "budget_tokens": float64(1024)}, "low"},
		{"3.7 is where thinking arrived", "claude-3-7-sonnet-20250219", enum.EffortMedium, nil,
			map[string]any{"type": "enabled", "budget_tokens": float64(4096)}, ""},
		{"4.5-era silent without effort", "claude-sonnet-4-5", "", nil, nil, ""},
		{"3.5 predates thinking — the field would 400", "claude-3-5-sonnet-20241022", enum.EffortHigh, nil, nil, ""},
		{"3.5 dot-form parses the same", "claude-3.5-sonnet", enum.EffortHigh, nil, nil, ""},
		{"claude 2 never thinks", "claude-2.1", enum.EffortHigh, nil, nil, ""},
		{"4.6 dropped the enabled contract", "claude-sonnet-4-6", enum.EffortHigh, nil,
			map[string]any{"type": "adaptive", "display": "summarized"}, ""},
		{"none stays silent — no safe disabled wire", "claude-haiku-5-5", enum.EffortNone, nil, nil, ""},
		{"non-claude family keeps the escape hatch", "qwen3-coder", enum.EffortHigh, nil, nil, ""},
		{"a claude-ish name without the marker stays out", "notaclaude-v2", "", nil, nil, ""},
		{"unparseable id reads as current", "claude-x", "", nil,
			map[string]any{"type": "adaptive", "display": "summarized"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured map[string]any
			svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
				captured = decodeReq(t, r)
				json.NewEncoder(w).Encode(map[string]any{
					"id": "m", "role": "assistant",
					"content":     []any{map[string]any{"type": "text", "text": "ok"}},
					"stop_reason": "end_turn",
				})
			})
			conv := newConv(t)
			m, _ := entity.NewMessage(enum.RoleUser, "hi")
			conv.Append(m)
			model, _ := entity.NewModel(tc.modelID, "test")
			if tc.effort != "" {
				model.SetEffort(tc.effort)
			}
			if tc.maxOut != nil {
				model.SetMaxOutputTokens(tc.maxOut)
			}
			if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
				t.Fatal(err)
			}
			got, sent := captured["thinking"].(map[string]any)
			if tc.want == nil {
				if sent {
					t.Fatalf("thinking must stay silent: %+v", got)
				}
				return
			}
			if !sent {
				t.Fatalf("thinking opt-in missing: %+v", captured)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("thinking.%s = %v, want %v (body %+v)", k, got[k], v, got)
				}
			}
			if tc.wantEffort != "" {
				oc, _ := captured["output_config"].(map[string]any)
				if oc["effort"] != tc.wantEffort {
					t.Fatalf("output_config.effort = %v, want %s", oc["effort"], tc.wantEffort)
				}
			}
		})
	}
}

// A budget that can't fit under max_tokens skips thinking rather than
// silently shrinking the effort the user asked for.
func TestThinkingBudgetNeedsHeadroom(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "role": "assistant",
			"content":     []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("claude-sonnet-4-5", "test")
	model.SetEffort(enum.EffortHigh) // 8192 budget vs a 2048 cap
	cap := 2048
	model.SetMaxOutputTokens(&cap)
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if _, sent := captured["thinking"]; sent {
		t.Fatalf("budget >= max_tokens must not send: %+v", captured["thinking"])
	}
}

func TestConsecutiveUserTurnsMerge(t *testing.T) {
	// An assistant turn with calls emits a tool_result user turn; a
	// following user message must fold into it — Anthropic rejects two
	// consecutive same-role turns.
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
		})
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "what time")
	conv.Append(m)
	asst, _ := entity.NewMessage(enum.RoleAssistant, "checking")
	tc, _ := entity.NewToolCall("toolu_1", "get_time", `{"tz":"UTC"}`, enum.TooltypeOrigin)
	asst.AddToolCall(tc)
	if err := asst.SetToolOutput("toolu_1", "12:00"); err != nil {
		t.Fatal(err)
	}
	conv.Append(asst)
	follow, _ := entity.NewMessage(enum.RoleUser, "thanks")
	conv.Append(follow)
	model, _ := entity.NewModel("claude-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want user, assistant, one merged user turn — got %+v", msgs)
	}
	merged := msgs[2].(map[string]any)
	if merged["role"] != "user" {
		t.Fatalf("merged turn role: %+v", merged)
	}
	blocks, _ := merged["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("want tool_result + text blocks, got %+v", blocks)
	}
	if b := blocks[0].(map[string]any); b["type"] != "tool_result" || b["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool_result block: %+v", b)
	}
	if b := blocks[1].(map[string]any); b["type"] != "text" || b["text"] != "thanks" {
		t.Fatalf("user text block: %+v", b)
	}
}

func TestContentBlocksKeepsUnknownContent(t *testing.T) {
	// A content shape the switch predates must survive the merge as its
	// own block — returning nil would drop the turn's content silently.
	unknown := map[string]any{"type": "future_block", "data": "x"}
	got := contentBlocks(unknown)
	if len(got) != 1 || got[0].(map[string]any)["type"] != "future_block" {
		t.Fatalf("unknown content must ride as a block: %+v", got)
	}
	if got := contentBlocks(nil); got != nil {
		t.Fatalf("nil content has nothing to merge: %+v", got)
	}
}

func TestSanitizeCompactedTailDropsReasoningOnly(t *testing.T) {
	plain, _ := entity.NewMessage(enum.RoleUser, "plain")
	reasoning, _ := entity.NewMessage(enum.RoleAssistant, "answer")
	raw := `[{"type":"thinking","thinking":"secret","signature":"SIG"},` +
		`{"type":"redacted_thinking","data":"ENC"},` +
		`{"type":"text","text":"answer"}]`
	reasoning.SetOptionalContent(blocksKey, raw)
	onlyReasoning, _ := entity.NewMessage(enum.RoleAssistant, "")
	onlyReasoning.SetOptionalContent(blocksKey,
		`[{"type":"thinking","thinking":"secret","signature":"SIG"}]`)

	svc := &Service{}
	got := svc.SanitizeCompactedTail([]entity.Message{plain, reasoning, onlyReasoning})
	if len(got) != 3 {
		t.Fatalf("sanitizer changed length: %d", len(got))
	}
	if got[0].ID() != plain.ID() || got[0].OptionalContent()[blocksKey] != "" {
		t.Fatalf("plain message changed: %+v", got[0])
	}
	if got[1].ID() != reasoning.ID() {
		t.Fatalf("sanitizer reordered messages: %+v", got)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(got[1].OptionalContent()[blocksKey]), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "answer" {
		t.Fatalf("kept blocks = %+v", blocks)
	}
	if _, ok := got[2].OptionalContent()[blocksKey]; ok {
		t.Fatalf("reasoning-only message kept blocks: %+v", got[2].OptionalContent())
	}
	// The input must stay untouched: later turns still replay exactly what
	// the provider sent.
	if reasoning.OptionalContent()[blocksKey] != raw {
		t.Fatal("sanitizer mutated the input message")
	}
}

func TestBetaHeaderValueFormats(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`["beta-a","beta-b"]`, "beta-a, beta-b"},
		{`"beta-a"`, "beta-a"},
		{"beta-a, beta-b", "beta-a, beta-b"},
		{"beta-a  beta-a\nbeta-b", "beta-a, beta-b"},
		{``, ""},
		{`[]`, ""},
	}
	for _, tc := range cases {
		model, _ := entity.NewModel("m", "test")
		if tc.raw != "" {
			model.SetOptionalContent(betaKey, tc.raw)
		}
		if got := betaHeaderValue(model); got != tc.want {
			t.Errorf("betaHeaderValue(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// The system-role workspace pin encodes as the completed-call pair it
// records — an assistant turn of tool_use blocks immediately answered by
// a user turn of tool_result blocks, with no empty text block.
func TestSystemHydrationEmitsToolPair(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-x",
			"content":     []any{map[string]any{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
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

	model, _ := entity.NewModel("claude-x", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	msgs, _ := captured["messages"].([]any)
	var use, result map[string]any
	for _, raw := range msgs {
		m := raw.(map[string]any)
		blocks, _ := m["content"].([]any)
		for _, rawB := range blocks {
			b := rawB.(map[string]any)
			switch b["type"] {
			case "tool_use":
				if b["name"] == "workspace" {
					use = b
					if m["role"] != "assistant" {
						t.Fatalf("tool_use on %v turn", m["role"])
					}
				}
			case "tool_result":
				if b["tool_use_id"] == use["id"] {
					result = b
					if m["role"] != "user" {
						t.Fatalf("tool_result on %v turn", m["role"])
					}
				}
			case "text":
				if b["text"] == "" {
					t.Fatalf("empty text block emitted for pin turn")
				}
			}
		}
	}
	if use == nil || result == nil {
		t.Fatalf("pin pair missing: %+v", msgs)
	}
	if !strings.Contains(result["content"].(string), "workspace changed to /srv/ws") {
		t.Fatalf("pin output = %v", result["content"])
	}
}
