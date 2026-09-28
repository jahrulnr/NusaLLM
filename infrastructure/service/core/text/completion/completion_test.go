package textcompletion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestUnaryRoundtrip(t *testing.T) {
	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
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
	half := 0.5
	model.SetTemperature(&half)

	ev, h := events()
	if err := svc.Send(t.Context(), conv, gw, model, h); err != nil {
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
	if len(*ev) != 2 || (*ev)[0].Kind() != enum.TurnEventDelta || (*ev)[1].Kind() != enum.TurnEventFinished {
		t.Fatalf("events: %+v", *ev)
	}
}

func TestImageFileAttachmentBecomesDataURI(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47} // tiny png stub
	path := filepath.Join(t.TempDir(), "x.png")
	os.WriteFile(path, img, 0o644)

	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "see this")
	a, _ := valueobject.NewAttachment("file://"+path, "image/png")
	m.AddAttachment(a)
	conv.Append(m)

	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
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
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
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
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
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
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
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

// Tool output attachments leave history as a synthetic user message right
// after the tool message — the tool role itself stays text-only.
func TestToolOutputAttachmentsBecomeUserParts(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47}
	path := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(path, img, 0o644)

	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("call_1", "read_media", `{"path":"shot.png"}`, enum.TooltypeOrigin)
	m.AddToolCall(tc)
	m.SetToolOutput("call_1", "loaded image shot.png")
	att, _ := valueobject.NewAttachment("file://"+path, "image/png")
	if err := m.SetToolOutputAttachments("call_1", []valueobject.Attachment{att}); err != nil {
		t.Fatal(err)
	}
	conv.Append(m)

	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if len(captured.Messages) != 3 {
		t.Fatalf("want assistant+tool+user, got %+v", captured.Messages)
	}
	tool := captured.Messages[1]
	if tool.Role != "tool" || tool.ToolCallID != "call_1" {
		t.Fatalf("tool message malformed: %+v", tool)
	}
	media := captured.Messages[2]
	if media.Role != "user" {
		t.Fatalf("attachment carrier must be a user message, got %+v", media)
	}
	parts, ok := media.Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected label+image parts, got %v", media.Content)
	}
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
	if url != want {
		t.Fatalf("image_url = %q", url)
	}
}

// A model that never declared image input gets an honest placeholder
// instead of a dead payload — the gate lives at send time, not record time.
func TestToolOutputAttachmentGatedByModality(t *testing.T) {
	root := t.TempDir()
	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	})
	svc.fileRoot = root

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("call_1", "read_media", `{}`, enum.TooltypeOrigin)
	m.AddToolCall(tc)
	m.SetToolOutput("call_1", "loaded")
	missing, _ := valueobject.NewAttachment("file://"+filepath.Join(root, "nonexistent.png"), "image/png")
	m.SetToolOutputAttachments("call_1", []valueobject.Attachment{missing})
	conv.Append(m)

	// text-only model: the attachment must degrade to a text placeholder.
	model, _ := entity.NewModel("m", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText})
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	media := captured.Messages[2] // assistant + tool + media carrier
	parts, _ := media.Content.([]any)
	text, _ := parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(text, "omitted") {
		t.Fatalf("expected omitted placeholder, got %+v", parts[1])
	}

	// vision-capable model but the file is gone: placeholder, not a
	// failed request. Empty modality list fails open.
	model, _ = entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	media = captured.Messages[2]
	parts, _ = media.Content.([]any)
	text, _ = parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(text, "unavailable") {
		t.Fatalf("expected unavailable placeholder, got %+v", parts[1])
	}
}

// With multiple origin calls the tool results must stay one contiguous
// block — a media-bearing earlier call pushes its synthetic user message
// after the whole run, never between two tool messages. Strict providers
// reject interleaved orders, and a persisted poisoned history would fail
// every subsequent send.
func TestToolOutputAttachmentsKeepToolBlockContiguous(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47}
	path := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(path, img, 0o644)

	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc1, _ := entity.NewToolCall("call_1", "read_media", `{"path":"shot.png"}`, enum.TooltypeOrigin)
	tc2, _ := entity.NewToolCall("call_2", "read_file", `{"path":"x.txt"}`, enum.TooltypeOrigin)
	m.AddToolCall(tc1)
	m.AddToolCall(tc2)
	m.SetToolOutput("call_1", "loaded image")
	m.SetToolOutput("call_2", "file content")
	att, _ := valueobject.NewAttachment("file://"+path, "image/png")
	m.SetToolOutputAttachments("call_1", []valueobject.Attachment{att})
	conv.Append(m)

	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if len(captured.Messages) != 4 {
		t.Fatalf("want assistant+tool+tool+user, got %+v", captured.Messages)
	}
	if captured.Messages[1].Role != "tool" || captured.Messages[1].ToolCallID != "call_1" ||
		captured.Messages[2].Role != "tool" || captured.Messages[2].ToolCallID != "call_2" ||
		captured.Messages[3].Role != "user" {
		t.Fatalf("tool block must stay contiguous, got %+v", captured.Messages)
	}
	parts, _ := captured.Messages[3].Content.([]any)
	label, _ := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(label, "call_1") {
		t.Fatalf("detached carrier must name its call, got %q", label)
	}
}

// A pinned attachment whose bytes changed since the tool checked them
// degrades to the same unavailable placeholder as a missing file — never
// a different payload than what was sniffed.
func TestToolOutputAttachmentPinMismatchDegrades(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47}
	path := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(path, img, 0o644)

	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleAssistant, "")
	tc, _ := entity.NewToolCall("call_1", "read_media", `{}`, enum.TooltypeOrigin)
	m.AddToolCall(tc)
	m.SetToolOutput("call_1", "loaded")
	att, _ := valueobject.NewAttachment("file://"+path, "image/png")
	att.SetSHA256("0bad") // any digest ≠ the file's
	m.SetToolOutputAttachments("call_1", []valueobject.Attachment{att})
	conv.Append(m)

	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	parts, _ := captured.Messages[2].Content.([]any)
	text, _ := parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(text, "unavailable") {
		t.Fatalf("pin mismatch must degrade to placeholder, got %+v", parts[1])
	}
	if strings.Contains(text, path) {
		t.Fatal("placeholder must not leak the workspace path")
	}
}

func TestUserAttachmentUnavailableDegradesToPlaceholder(t *testing.T) {
	root := t.TempDir()
	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	})
	svc.fileRoot = root

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "inspect this image")
	missing, _ := valueobject.NewAttachment("file://"+filepath.Join(root, "gone", "shot.png"), "image/png")
	missing.SetName("/secret/project/shot.png")
	m.AddAttachment(missing)
	conv.Append(m)
	model, _ := entity.NewModel("m", "test")

	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatalf("missing attachment must not fail the provider request: %v", err)
	}
	parts, ok := captured.Messages[0].Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("want text plus unavailable placeholder, got %#v", captured.Messages[0].Content)
	}
	text, _ := parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(text, "unavailable") || !strings.Contains(text, "shot.png") ||
		strings.Contains(text, "/gone/") || strings.Contains(text, "/secret/") {
		t.Fatalf("unsafe or missing placeholder: %q", text)
	}
}

func TestUserAttachmentGatedByModality(t *testing.T) {
	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	})

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "inspect this image")
	missing, _ := valueobject.NewAttachment("file:///not-resolved.png", "image/png")
	m.AddAttachment(missing)
	conv.Append(m)
	model, _ := entity.NewModel("m", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText})

	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatalf("unsupported attachment must degrade instead of failing: %v", err)
	}
	parts, ok := captured.Messages[0].Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("want text plus omitted placeholder, got %#v", captured.Messages[0].Content)
	}
	text, _ := parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(text, "omitted") {
		t.Fatalf("want modality placeholder, got %q", text)
	}
}

func TestUserAttachmentOutsideConfiguredRootDegrades(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(outside, img, 0o600); err != nil {
		t.Fatal(err)
	}
	var captured RequestBody
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0,
				"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	})
	svc.fileRoot = root

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "inspect this image")
	att, _ := valueobject.NewAttachment("file://"+outside, "image/png")
	att.SetName("shot.png")
	m.AddAttachment(att)
	conv.Append(m)
	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatalf("out-of-root attachment must degrade rather than abort: %v", err)
	}
	parts, ok := captured.Messages[0].Content.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("want text plus unavailable placeholder, got %#v", captured.Messages[0].Content)
	}
	placeholder, _ := parts[1].(map[string]any)["text"].(string)
	if !strings.Contains(placeholder, "unavailable") || strings.Contains(placeholder, outside) {
		t.Fatalf("out-of-root placeholder leaked data: %q", placeholder)
	}
}

func TestStreamAssemblesMessage(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
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
	if err := svc.Send(t.Context(), conv, gw, model, h); err != nil {
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
		case enum.TurnEventDelta:
			deltas++
		case enum.TurnEventFinished:
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
		svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		})
		conv := newConv(t)
		model, _ := entity.NewModel("m", "test")
		err := svc.Send(t.Context(), conv, gw, model, nil)
		if got := apperror.As(err).StatusCode(); got != tc.want {
			t.Fatalf("status %d → %d, want %d", tc.status, got, tc.want)
		}
	}
}

func TestStreamSendsIncludeUsage(t *testing.T) {
	var captured RequestBody
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	})
	conv := newConv(t)
	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if captured.StreamOptions == nil || !captured.StreamOptions.IncludeUsage {
		t.Fatal("stream_options.include_usage missing — usage would silently zero")
	}
}

func TestStreamMidStreamError(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
		w.Write([]byte("data: {\"error\":{\"message\":\"slow down\",\"type\":\"rate_limit_error\"}}\n\n"))
	})
	conv := newConv(t)
	model, _ := entity.NewModel("m", "test")
	err := svc.Send(t.Context(), conv, gw, model, nil)
	if got := apperror.As(err).StatusCode(); got != 429 {
		t.Fatalf("mid-stream rate_limit_error → %d, want 429", got)
	}
	if last := conv.Last(); last != nil && last.Role() == enum.RoleAssistant {
		t.Fatal("failed stream must not commit the partial message")
	}
}

func TestStreamHostileIndex(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":-1,\"function\":{\"arguments\":\"x\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":9999999,\"function\":{\"arguments\":\"x\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})
	conv := newConv(t)
	model, _ := entity.NewModel("m", "test")
	if err := svc.Send(t.Context(), conv, gw, model, nil); err != nil { // must not panic/OOM
		t.Fatal(err)
	}
	if len(conv.Last().ToolCalls()) != 0 { // Last() non-nil — turn committed
		t.Fatal("hostile indexes must be dropped, not committed")
	}
}

func TestStreamTruncatedEOF(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		// EOF without finish_reason or [DONE] — a cut connection, not a turn
	})
	conv := newConv(t)
	model, _ := entity.NewModel("m", "test")
	err := svc.Send(t.Context(), conv, gw, model, nil)
	if got := apperror.As(err).StatusCode(); err == nil || got != 502 {
		t.Fatalf("truncated stream → %v, want 502", err)
	}
	if last := conv.Last(); last != nil && last.Role() == enum.RoleAssistant {
		t.Fatal("truncated stream must not commit")
	}
}

func TestStreamCancelIsNotUpstreamError(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold open — cancel must unblock the client
	})
	ctx, cancel := context.WithCancel(t.Context())
	conv := newConv(t)
	model, _ := entity.NewModel("m", "test")
	done := make(chan error, 1)
	go func() { done <- svc.Send(ctx, conv, gw, model, nil) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if got := apperror.As(err).StatusCode(); got != 408 {
			t.Fatalf("cancel → %d, want 408", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not unblock Send")
	}
	if last := conv.Last(); last == nil || last.Role() != enum.RoleAssistant || last.Content() != "x" {
		t.Fatalf("canceled stream dropped the delta: %+v", conv.Last())
	}
}

func TestStreamCancelKeepsCompleteToolDropsPartial(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\",\"content\":\"hello\"}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"exec\",\"arguments\":\"{\\\"command\\\":\\\"somecode.sh\\\"}\"}}]}}]}\n\n"))
		w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"c2\",\"type\":\"function\",\"function\":{\"name\":\"exec\",\"arguments\":\"{\\\"command\\\":\\\"tail\"}}]}}]}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	conv := newConv(t)
	model, _ := entity.NewModel("m", "test")
	done := make(chan error, 1)
	go func() { done <- svc.Send(ctx, conv, gw, model, nil) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if got := apperror.As(err).StatusCode(); got != 408 {
			t.Fatalf("cancel → %d, want 408", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not unblock Send")
	}
	last := conv.Last()
	if last == nil || last.Content() != "hello" {
		t.Fatalf("partial text: %+v", last)
	}
	if got := last.OptionalContent()["completion.reasoning_content"]; got != "think" {
		t.Fatalf("reasoning: %q", got)
	}
	calls := last.ToolCalls()
	if len(calls) != 1 || calls[0].ID() != "c1" || calls[0].Name() != "exec" {
		t.Fatalf("tools: %+v", calls)
	}
	if calls[0].Input() != `{"command":"somecode.sh"}` {
		t.Fatalf("complete command: %s", calls[0].Input())
	}
}
