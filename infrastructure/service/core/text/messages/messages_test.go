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
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
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
	return New(coretext.Options{}), &gw, srv
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
	if tr["tool_use_id"] != "toolu_1" || !strings.Contains(tr["content"].(string), "12:00") {
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
			if bb["type"] == "tool_result" && strings.Contains(bb["content"].(string), "interrupted") {
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
	if _, err := encodeAttachment(a, ""); err == nil {
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
	model.SetOptionalContent("messages.thinking", `{"type":"adaptive","display":"summarized"}`)
	model.SetOptionalContent("completion.reasoning_effort", `"high"`) // other adapter's prefix — must not leak
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if captured["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Fatalf("output_config: %+v", captured["output_config"])
	}
	th, ok := captured["thinking"].(map[string]any)
	if !ok || th["type"] != "adaptive" || th["display"] != "summarized" {
		t.Fatalf("thinking escape hatch: %+v", captured["thinking"])
	}
	if _, leaked := captured["reasoning_effort"]; leaked {
		t.Fatal("completion.-prefixed param leaked onto messages wire")
	}
}
