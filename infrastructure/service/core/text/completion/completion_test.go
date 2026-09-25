package textcompletion

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/llm-base/infrastructure/domain/aggregate"
	"github.com/jahrulnr/llm-base/infrastructure/domain/entity"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

// newService spins an httptest server bound to a fake gateway.
func newService(t *testing.T, stream bool, handler http.HandlerFunc) (*Service, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	gw, err := aggregate.NewGateway("test", "t", srv.URL, enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("sk-test")
	if stream {
		gw.EnableStream()
	} else {
		gw.DisableStream()
	}
	return New(&gw, Options{}), srv
}

func newConv(t *testing.T) *aggregate.Conversation {
	t.Helper()
	c, err := aggregate.NewConversation("c1")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func events() (*[]event.StreamEvent, event.LLMEventHandler) {
	var got []event.StreamEvent
	return &got, func(e event.StreamEvent) { got = append(got, e) }
}

func TestUnaryRoundtrip(t *testing.T) {
	var captured RequestBody
	svc, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Error("missing auth header")
		}
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("x-request-id", "req-1")
		json.NewEncoder(w).Encode(map[string]any{
			"model": "m", "choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "hello there"},
				"finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 50,
				"total_tokens": 1050, "prompt_tokens_details": map[string]any{"cached_tokens": 900}},
		})
	})

	conv := newConv(t)
	conv.SetSystemPrompt("be brief")
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	tool, _ := valueobject.NewToolSchema("get_time", "current time", json.RawMessage(`{"type":"object"}`), true, enum.ToolPermissionAgent)
	hidden, _ := valueobject.NewToolSchema("sys", "", nil, true, enum.ToolPermissionSystem)
	conv.SetTools([]valueobject.ToolSchema{tool, hidden})

	model, _ := entity.NewModel("m", "test")
	model.SetTemperature(0.5)

	ev, h := events()
	if err := svc.Send(t.Context(), conv, model, h); err != nil {
		t.Fatal(err)
	}

	// Wire request shape.
	if captured.Model != "m" || captured.Temperature == nil || *captured.Temperature != 0.5 {
		t.Fatalf("params: %+v", captured)
	}
	if captured.Messages[0].Role != "system" || captured.Messages[1].Role != "user" {
		t.Fatalf("message order: %+v", captured.Messages)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Function.Name != "get_time" {
		t.Fatal("system-permission tool leaked onto wire")
	}

	// Domain effects.
	last := conv.Last()
	if last == nil || last.Content() != "hello there" || last.FinishReason() != enum.FinishReasonStop {
		t.Fatalf("message: %+v", last)
	}
	if conv.Usage().TotalInput() != 1000 || conv.Usage().InputTokens() != 100 {
		t.Fatalf("usage not normalized: %+v", conv.Usage())
	}
	// Uniform event contract: delta + finished.
	if len(*ev) != 2 || (*ev)[0].Kind() != enum.StreamEventDelta || (*ev)[1].Kind() != enum.StreamEventFinished {
		t.Fatalf("events: %+v", *ev)
	}
}

func TestImageFileAttachmentBecomesDataURI(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47} // tiny png stub
	path := filepath.Join(t.TempDir(), "x.png")
	os.WriteFile(path, img, 0o644)

	var captured RequestBody
	svc, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	})

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "see this")
	a, _ := valueobject.NewAttachment("file://"+path, "image/png")
	m.AddAttachment(a)
	conv.Append(m)

	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, model, nil); err != nil {
		t.Fatal(err)
	}
	parts, ok := captured.Messages[0].Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected text+image parts, got %v", captured.Messages[0].Content)
	}
	imgPart := parts[1].(map[string]any)
	url := imgPart["image_url"].(map[string]any)["url"].(string)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
	if url != want {
		t.Fatalf("image_url = %q", url)
	}
}

func TestToolCallsRoundtrip(t *testing.T) {
	var captured RequestBody
	svc, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls",
				"message": map[string]any{"role": "assistant", "content": nil,
					"tool_calls": []any{map[string]any{"id": "call_1", "type": "function",
						"function": map[string]any{"name": "get_time", "arguments": `{"tz":"utc"}`}}}}}},
		})
	})

	conv := newConv(t)
	u, _ := entity.NewMessage(enum.RoleUser, "time?")
	conv.Append(u)
	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, model, nil); err != nil {
		t.Fatal(err)
	}
	last := conv.Last()
	if len(last.ToolCalls()) != 1 || last.ToolCalls()[0].Name() != "get_time" ||
		last.ToolCalls()[0].Input() != `{"tz":"utc"}` ||
		last.ToolCalls()[0].Type() != enum.TooltypeOrigin {
		t.Fatalf("tool call: %+v", last.ToolCalls())
	}

	// Execute + re-send: wire must carry assistant tool_calls + tool message.
	last.SetToolOutput("call_1", "12:00")
	if err := svc.Send(t.Context(), conv, model, nil); err != nil {
		t.Fatal(err)
	}
	var sawAssistant, sawTool bool
	for _, m := range captured.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call_1" {
			sawAssistant = true
		}
		if m.Role == "tool" && m.ToolCallID == "call_1" {
			sawTool = true
		}
	}
	if !sawAssistant || !sawTool {
		t.Fatalf("tool roundtrip broken: %+v", captured.Messages)
	}
}

func TestStreamAssemblesMessage(t *testing.T) {
	svc, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})

	conv := newConv(t)
	u, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(u)
	model, _ := entity.NewModel("m", "test")

	ev, h := events()
	if err := svc.Send(t.Context(), conv, model, h); err != nil {
		t.Fatal(err)
	}
	last := conv.Last()
	if last.Content() != "Hello" || last.FinishReason() != enum.FinishReasonToolCalls {
		t.Fatalf("assembled: %+v", last)
	}
	if len(last.ToolCalls()) != 1 || last.ToolCalls()[0].ID() != "c1" {
		t.Fatalf("streamed call: %+v", last.ToolCalls())
	}
	if conv.Usage().TotalInput() != 10 {
		t.Fatal("stream usage lost")
	}
	var deltas, finished int
	for _, e := range *ev {
		switch e.Kind() {
		case enum.StreamEventDelta:
			deltas++
		case enum.StreamEventFinished:
			finished++
		}
	}
	if deltas != 2 || finished != 1 {
		t.Fatalf("events: %+v", *ev)
	}
}

func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{429, `{"error":{"message":"slow down","type":"rate_limit_exceeded"}}`, 429},
		{401, `{"error":{"message":"bad key"}}`, 401},
		{400, `{"error":{"message":"bad","code":"context_length_exceeded"}}`, 413}, // remap: compact, not retry
		{500, `{"error":{"message":"oops"}}`, 500},
	} {
		svc, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		})
		conv := newConv(t)
		model, _ := entity.NewModel("m", "test")
		err := svc.Send(t.Context(), conv, model, nil)
		if got := apperror.As(err).StatusCode(); got != tc.want {
			t.Fatalf("status %d → %d, want %d", tc.status, got, tc.want)
		}
	}
}
