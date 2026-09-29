package textgeneratecontent

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
	gw, err := aggregate.NewGateway("test", "t", srv.URL+"/v1beta", enum.StyleGenerateContent)
	if err != nil {
		t.Fatal(err)
	}
	gw.SetApiKey("gkey-test")
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

func okBody(parts []any) map[string]any {
	return map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5},
	}
}

func TestUnaryRoundtrip(t *testing.T) {
	var captured map[string]any
	var gotPath string
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("x-goog-api-key") != "gkey-test" {
			t.Error("missing x-goog-api-key header")
		}
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": "hello there"}}},
				"finishReason": "STOP",
			}},
			"usageMetadata": map[string]any{
				"promptTokenCount": 1000, "candidatesTokenCount": 40, "thoughtsTokenCount": 10,
				"cachedContentTokenCount": 900,
			},
		})
	})

	conv := newConv(t)
	conv.SetSystemPrompt("be brief")
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	tool, _ := valueobject.NewToolSchema("get_time", "current time",
		json.RawMessage(`{"type":"object","$schema":"http://x","additionalProperties":false,"properties":{"tz":{"type":"string","$ref":"#/x"}},"required":["tz","ghost"]}`),
		true, enum.ToolPermissionAgent)
	hidden, _ := valueobject.NewToolSchema("sys", "", nil, true, enum.ToolPermissionSystem)
	conv.SetTools([]valueobject.ToolSchema{tool, hidden})

	model, _ := entity.NewModel("gemini-3-flash", "test")
	half := 0.5
	model.SetTemperature(&half)
	model.SetEffort(enum.EffortLow)

	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}

	// Wire request shape.
	if gotPath != "/v1beta/models/gemini-3-flash:generateContent" {
		t.Fatalf("path: %s", gotPath)
	}
	si, _ := captured["systemInstruction"].(map[string]any)
	if si == nil || si["parts"].([]any)[0].(map[string]any)["text"] != "be brief" {
		t.Fatalf("systemInstruction: %+v", captured["systemInstruction"])
	}
	contents, _ := captured["contents"].([]any)
	if len(contents) != 1 || contents[0].(map[string]any)["role"] != "user" {
		t.Fatalf("contents: %+v", contents)
	}
	gc, _ := captured["generationConfig"].(map[string]any)
	if gc["maxOutputTokens"] != float64(defaultMaxTokens) || gc["temperature"] != 0.5 {
		t.Fatalf("generationConfig: %+v", gc)
	}
	// gemini-3 in the id → thinkingLevel only, never thinkingBudget.
	tc, _ := gc["thinkingConfig"].(map[string]any)
	if tc == nil || tc["thinkingLevel"] != "low" || tc["includeThoughts"] != true || tc["thinkingBudget"] != nil {
		t.Fatalf("thinkingConfig: %+v", gc["thinkingConfig"])
	}
	// Schema sanitized to the subset: banned keys stripped, stray
	// required pruned.
	tools, _ := captured["tools"].([]any)
	decls := tools[0].(map[string]any)["functionDeclarations"].([]any)
	if len(decls) != 1 || decls[0].(map[string]any)["name"] != "get_time" {
		t.Fatal("system-permission tool leaked onto wire")
	}
	params := decls[0].(map[string]any)["parameters"].(map[string]any)
	if params["$schema"] != nil || params["additionalProperties"] != nil {
		t.Fatalf("schema not sanitized: %+v", params)
	}
	req, _ := params["required"].([]any)
	if len(req) != 1 || req[0] != "tz" {
		t.Fatalf("required not pruned: %+v", params["required"])
	}

	// Domain effects.
	last := conv.Last()
	if last == nil || last.Content() != "hello there" || last.FinishReason() != enum.FinishReasonStop {
		t.Fatalf("message: %+v", last)
	}
	// Inclusive: prompt 1000, cached 900 → 100 uncached; output =
	// candidates + thoughts = 50.
	if conv.Usage().InputTokens() != 100 || conv.Usage().CacheReadTokens() != 900 || conv.Usage().OutputTokens() != 50 {
		t.Fatalf("usage not normalized: %+v", conv.Usage())
	}
	if len(*ev) != 2 || (*ev)[0].Kind() != enum.TurnEventDelta || (*ev)[1].Kind() != enum.TurnEventFinished {
		t.Fatalf("events: %+v", *ev)
	}
}

func TestToolCallRoundtrip(t *testing.T) {
	var captured2 map[string]any
	call := 0
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{"role": "model", "parts": []any{
						map[string]any{"text": "checking"},
						map[string]any{"functionCall": map[string]any{"id": "fc-1", "name": "get_time", "args": map[string]any{"tz": "UTC"}}, "thoughtSignature": "SIG-CALL"},
					}},
					"finishReason": "STOP",
				}},
			})
			return
		}
		captured2 = decodeReq(t, r)
		json.NewEncoder(w).Encode(okBody([]any{map[string]any{"text": "it is noon"}}))
	})

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "what time")
	conv.Append(m)
	model, _ := entity.NewModel("gemini-3-flash", "test")

	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	last := conv.Last()
	if last == nil || last.FinishReason() != enum.FinishReasonToolCalls {
		t.Fatalf("finish: %+v", last)
	}
	tcs := last.ToolCalls()
	if len(tcs) != 1 || tcs[0].ID() != "fc-1" || tcs[0].Name() != "get_time" {
		t.Fatalf("tool calls: %+v", tcs)
	}
	if !json.Valid([]byte(tcs[0].Input())) || !strings.Contains(tcs[0].Input(), `"tz":"UTC"`) {
		t.Fatalf("input not raw JSON: %s", tcs[0].Input())
	}
	if tcs[0].OptionalContent()[callIDKey] != "fc-1" {
		t.Fatal("provider call id marker not stored")
	}

	// Execute the tool and send again — replay must carry the SIGNED
	// functionCall verbatim + a functionResponse echoing the id.
	if err := last.SetToolOutput("fc-1", "12:00"); err != nil {
		t.Fatal(err)
	}
	m2, _ := entity.NewMessage(enum.RoleUser, "thanks?")
	conv.Append(m2)
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}

	contents, _ := captured2["contents"].([]any)
	var modelTurn, resultTurn map[string]any
	for _, raw := range contents {
		c := raw.(map[string]any)
		parts, _ := c["parts"].([]any)
		for _, p := range parts {
			pp := p.(map[string]any)
			if pp["functionCall"] != nil {
				modelTurn = c
			}
			if pp["functionResponse"] != nil {
				resultTurn = c
			}
		}
	}
	if modelTurn == nil || modelTurn["role"] != "model" {
		t.Fatalf("model turn missing: %+v", contents)
	}
	var fcPart map[string]any
	for _, p := range modelTurn["parts"].([]any) {
		if pp := p.(map[string]any); pp["functionCall"] != nil {
			fcPart = pp
		}
	}
	if fcPart["thoughtSignature"] != "SIG-CALL" {
		t.Fatalf("signed functionCall must replay verbatim: %+v", fcPart)
	}
	fc := fcPart["functionCall"].(map[string]any)
	if fc["id"] != "fc-1" || fc["name"] != "get_time" {
		t.Fatalf("functionCall replay: %+v", fc)
	}
	if resultTurn == nil || resultTurn["role"] != "user" {
		t.Fatal("functionResponse turn missing")
	}
	fr := resultTurn["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["id"] != "fc-1" || fr["name"] != "get_time" {
		t.Fatalf("functionResponse id echo: %+v", fr)
	}
	result := fr["response"].(map[string]any)["result"].(string)
	if !strings.HasPrefix(result, `<untrusted_tool latency="`) ||
		!strings.Contains(result, "12:00") ||
		!strings.HasSuffix(result, "</untrusted_tool>") {
		t.Fatalf("functionResponse payload: %+v", fr["response"])
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestUserTurnKeepsInjectedToolEvidence(t *testing.T) {
	msg, _ := entity.NewMessage(enum.RoleUser, "question")
	call, _ := entity.NewToolCall("injected", "context", "{}", enum.TooltypeInjection)
	call.SetOutput("extra context")
	msg.AddToolCall(call)
	contents := encodeMessage(msg, entity.Model{}, wire.MediaRoot{})
	if len(contents) != 1 || contents[0].Role != "user" {
		t.Fatalf("user turn missing: %+v", contents)
	}
	text := contents[0].Parts[0].(PartBody).Text
	if !strings.Contains(text, "extra context") || !strings.Contains(text, "<untrusted_tool") {
		t.Fatalf("injected evidence was dropped from user turn: %q", text)
	}
}

func TestSignatureReplayAndMerge(t *testing.T) {
	// Template: signed thought part → verbatim; text marker → refill;
	// signed text → verbatim. Plus two parallel unsigned calls whose
	// results must merge into ONE user turn (same-role rule).
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(okBody([]any{map[string]any{"text": "ok"}}))
	})
	conv := newConv(t)
	asst, _ := entity.NewMessage(enum.RoleAssistant, "answer")
	tc1, _ := entity.NewToolCall("c1", "exec", `{"cmd":"a"}`, enum.TooltypeOrigin)
	tc2, _ := entity.NewToolCall("c2", "exec", `{"cmd":"b"}`, enum.TooltypeOrigin)
	tc1.SetOutput("out-a")
	tc2.SetOutput("out-b")
	asst.AddToolCall(tc1)
	asst.AddToolCall(tc2)
	asst.SetOptionalContent(partsKey, `[
		{"text":"hmm","thought":true,"thoughtSignature":"SIG-T"},
		{"refill":"text"},
		{"refill":"functionCall"},
		{"functionCall":{"name":"exec","args":{"cmd":"b"}},"thoughtSignature":"SIG-B"}
	]`)
	conv.Append(asst)
	m, _ := entity.NewMessage(enum.RoleUser, "next")
	conv.Append(m)
	model, _ := entity.NewModel("gemini-3-flash", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}

	contents, _ := captured["contents"].([]any)
	// model turn + ONE user turn (functionResponses + the "next" text
	// merged by the same-role rule).
	if len(contents) != 2 {
		t.Fatalf("contents: %+v", contents)
	}
	modelTurn := contents[0].(map[string]any)
	if modelTurn["role"] != "model" {
		t.Fatalf("first turn: %+v", contents)
	}
	parts := modelTurn["parts"].([]any)
	if len(parts) != 4 {
		t.Fatalf("model parts: %+v", parts)
	}
	if parts[0].(map[string]any)["thoughtSignature"] != "SIG-T" {
		t.Fatalf("thought part replay: %+v", parts[0])
	}
	if parts[1].(map[string]any)["text"] != "answer" {
		t.Fatalf("text refill: %+v", parts[1])
	}
	if parts[2].(map[string]any)["functionCall"] == nil {
		t.Fatalf("call refill: %+v", parts[2])
	}
	// Signed functionCall replays verbatim AND consumed tc2's slot —
	// so the refilled call must be tc1's (cmd a) and signed is cmd b.
	if !strings.Contains(mustJSON(parts[2]), `"cmd":"a"`) {
		t.Fatalf("unsigned call must refill first domain call: %+v", parts[2])
	}
	if parts[3].(map[string]any)["thoughtSignature"] != "SIG-B" {
		t.Fatalf("signed call replay: %+v", parts[3])
	}
	// functionResponses + the following user msg merge into ONE user turn.
	userTurn := contents[1].(map[string]any)
	if userTurn["role"] != "user" {
		t.Fatalf("result turn: %+v", userTurn)
	}
	var respCount int
	var sawNextText bool
	for _, p := range userTurn["parts"].([]any) {
		pp := p.(map[string]any)
		if pp["functionResponse"] != nil {
			respCount++
		}
		if pp["text"] == "next" {
			sawNextText = true
		}
	}
	if respCount != 2 || !sawNextText {
		t.Fatalf("merged user turn: %+v", userTurn)
	}
}

const streamFixture = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"let me ","thought":true}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"think","thought":true,"thoughtSignature":"SIG-TH"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}

data: {"candidates":[{"content":{"role":"model","parts":[{"text":" there","thoughtSignature":"SIG-A"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":15,"cachedContentTokenCount":40}}

`

func TestStreamRoundtrip(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("stream path: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(streamFixture))
	})

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gemini-3-flash", "test")

	ev, h := events()
	if err := svc.Send(context.Background(), conv, gw, model, h); err != nil {
		t.Fatal(err)
	}

	last := conv.Last()
	if last == nil || last.Content() != "Hello there" || last.FinishReason() != enum.FinishReasonStop {
		t.Fatalf("message: %+v", last)
	}
	if conv.Usage().InputTokens() != 60 || conv.Usage().OutputTokens() != 15 || conv.Usage().CacheReadTokens() != 40 {
		t.Fatalf("usage: %+v", conv.Usage())
	}
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

	// Template: thought part merged + signed; text merged with adopted sig.
	var template []map[string]any
	raw := last.OptionalContent()[partsKey]
	if raw == "" {
		t.Fatal("part template not stored")
	}
	json.Unmarshal([]byte(raw), &template)
	if len(template) != 2 || template[0]["thought"] != true || template[0]["thoughtSignature"] != "SIG-TH" {
		t.Fatalf("template: %s", raw)
	}
	if template[0]["text"] != "let me think" || template[1]["thoughtSignature"] != "SIG-A" {
		t.Fatalf("merged parts: %s", raw)
	}
}

func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{429, `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`, 429},
		{401, `{"error":{"code":401,"message":"bad key","status":"UNAUTHENTICATED"}}`, 401},
		{400, `{"error":{"code":400,"message":"input exceeds the maximum number of tokens","status":"INVALID_ARGUMENT"}}`, 413},
		{500, `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`, 500},
	} {
		svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		})
		conv := newConv(t)
		m, _ := entity.NewMessage(enum.RoleUser, "hi")
		conv.Append(m)
		model, _ := entity.NewModel("gemini-3-flash", "test")
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
		// Nothing — the connection died before the first chunk.
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gemini-3-flash", "test")
	err := svc.Send(context.Background(), conv, gw, model, nil)
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("want truncated stream error, got %v", err)
	}
}

func TestStreamMidError(t *testing.T) {
	svc, gw, _ := newService(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"error\":{\"code\":429,\"message\":\"quota\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gemini-3-flash", "test")
	err := svc.Send(context.Background(), conv, gw, model, nil)
	ae, ok := err.(apperror.IAppError)
	if !ok || ae.StatusCode() != 429 {
		t.Fatalf("want 429, got %v", err)
	}
}

func TestEncodeAttachmentRejectsPinnedRemoteURL(t *testing.T) {
	a, _ := valueobject.NewAttachment("https://example.com/x.png", "image/png")
	a.SetSHA256(strings.Repeat("a", 64))
	if _, err := encodeAttachment(a, wire.MediaRoot{}); err == nil {
		t.Fatal("pinned remote https attachment must fail")
	}
}

func TestImageFileAttachmentBecomesInlineData(t *testing.T) {
	img := []byte{0x89, 0x50, 0x4e, 0x47} // tiny png stub
	path := filepath.Join(t.TempDir(), "x.png")
	os.WriteFile(path, img, 0o644)

	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(okBody([]any{map[string]any{"text": "ok"}}))
	})
	svc.fileRoot = filepath.Dir(path)

	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "look")
	a, _ := valueobject.NewAttachment("file://"+path, "image/png")
	m.AddAttachment(a)
	conv.Append(m)
	model, _ := entity.NewModel("gemini-3-flash", "test")
	model.SetInputModalities([]enum.Modality{enum.ModalityText, enum.ModalityImage})
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	contents, _ := captured["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	imgPart := parts[1].(map[string]any)
	blob := imgPart["inlineData"].(map[string]any)
	if blob["mimeType"] != "image/png" || blob["data"] != base64.StdEncoding.EncodeToString(img) {
		t.Fatalf("inlineData: %+v", blob)
	}
}

func TestModelPathStripsPrefix(t *testing.T) {
	var gotPath string
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(okBody([]any{map[string]any{"text": "ok"}}))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("google/gemini-3.6-flash", "test")
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1beta/models/gemini-3.6-flash:generateContent" {
		t.Fatalf("path: %s", gotPath)
	}
}

func TestThinkingConfigFamilies(t *testing.T) {
	for _, tc := range []struct {
		modelID   string
		wantLevel string // "" → level must be absent
		wantNone  bool   // thinkingConfig omitted entirely
	}{
		{"gemini-3-flash", "low", false},
		{"gemini-3-pro", "high", false}, // xhigh clamps to high
		{"gemini-2.5-pro", "", false},   // includeThoughts only
		{"gemma-3-27b", "", true},       // rejects thinkingConfig
	} {
		var captured map[string]any
		svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
			captured = decodeReq(t, r)
			json.NewEncoder(w).Encode(okBody([]any{map[string]any{"text": "ok"}}))
		})
		conv := newConv(t)
		m, _ := entity.NewMessage(enum.RoleUser, "hi")
		conv.Append(m)
		model, _ := entity.NewModel(tc.modelID, "test")
		eff := enum.EffortLow
		if tc.wantLevel == "high" {
			eff = enum.EffortXHigh
		}
		model.SetEffort(eff)
		if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
			t.Fatal(err)
		}
		gc, _ := captured["generationConfig"].(map[string]any)
		th, hasTh := gc["thinkingConfig"].(map[string]any)
		if tc.wantNone {
			if hasTh {
				t.Fatalf("%s: thinkingConfig must be omitted: %+v", tc.modelID, th)
			}
			continue
		}
		if !hasTh || th["includeThoughts"] != true {
			t.Fatalf("%s: thinkingConfig: %+v", tc.modelID, gc["thinkingConfig"])
		}
		if tc.wantLevel == "" {
			if th["thinkingLevel"] != nil || th["thinkingBudget"] != nil {
				t.Fatalf("%s: 2.5 must carry neither level nor budget: %+v", tc.modelID, th)
			}
		} else if th["thinkingLevel"] != tc.wantLevel {
			t.Fatalf("%s: thinkingLevel: %+v", tc.modelID, th)
		}
	}
}

func TestEscapeHatch(t *testing.T) {
	var captured map[string]any
	svc, gw, _ := newService(t, false, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeReq(t, r)
		json.NewEncoder(w).Encode(okBody([]any{map[string]any{"text": "ok"}}))
	})
	conv := newConv(t)
	m, _ := entity.NewMessage(enum.RoleUser, "hi")
	conv.Append(m)
	model, _ := entity.NewModel("gemini-3-flash", "test")
	model.SetOptionalContent("generatecontent.toolConfig", `{"functionCallingConfig":{"mode":"ANY"}}`)
	if err := svc.Send(context.Background(), conv, gw, model, nil); err != nil {
		t.Fatal(err)
	}
	tc, _ := captured["toolConfig"].(map[string]any)
	fcc, _ := tc["functionCallingConfig"].(map[string]any)
	if fcc["mode"] != "ANY" {
		t.Fatalf("escape hatch: %+v", captured["toolConfig"])
	}
}
