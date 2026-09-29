package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

const testAuthority = "127.0.0.1:8080"

// TestMode silences gin's debug route banners in test output; the mode
// is a test-process choice here, not a NewHandler side effect.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// --- fakes: the consumer-defined interfaces, not the apps ---

type fakeConversations struct {
	infos   []drepo.ConversationInfo
	history []entity.Message
	tools   []valueobject.ToolSchema
	err     error
}

func (f *fakeConversations) Info(_ context.Context, id string) (drepo.ConversationInfo, error) {
	for _, i := range f.infos {
		if i.ID == id {
			return i, nil
		}
	}
	return drepo.ConversationInfo{}, apperror.New(404, "conversation not found", errors.New("no row"))
}

func (f *fakeConversations) CanCompact(_ context.Context, id string) (bool, error) {
	_, err := f.Info(context.Background(), id)
	return false, err
}

func (f *fakeConversations) List(context.Context) ([]drepo.ConversationInfo, error) {
	return f.infos, f.err
}

func (f *fakeConversations) History(_ context.Context, id string, start, end int) ([]entity.Message, error) {
	if _, err := f.Info(context.Background(), id); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	if start > len(f.history) {
		start = len(f.history)
	}
	if end > len(f.history) {
		end = len(f.history)
	}
	return f.history[start:end], nil
}

func (f *fakeConversations) ToolSchemas(context.Context, string) ([]valueobject.ToolSchema, error) {
	return f.tools, nil
}

func (f *fakeConversations) ContextFooterStats(context.Context, string, string, string) (appconversation.ContextFooterStats, error) {
	return appconversation.ContextFooterStats{}, nil
}

type fakeGateways struct {
	infos []appgateway.GatewayInfo
}

func (f *fakeGateways) DiscoverModels(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *fakeGateways) DiscoverProviders(context.Context, string, string) ([]dservice.UpstreamProvider, error) {
	return nil, nil
}

func (f *fakeGateways) List(context.Context) ([]appgateway.GatewayInfo, error) {
	return f.infos, nil
}

func (f *fakeGateways) Get(_ context.Context, id string) (appgateway.GatewayInfo, error) {
	for _, i := range f.infos {
		if i.ID == id {
			return i, nil
		}
	}
	return appgateway.GatewayInfo{}, apperror.New(404, "gateway not found", nil)
}

type fakeSettings struct{ raw []byte }

func (f *fakeSettings) Show() ([]byte, error) { return f.raw, nil }

// --- harness ---

func newTestHandler(t *testing.T, d Deps) *stdhttp.Handler {
	t.Helper()
	if len(d.Authorities) == 0 {
		d.Authorities = []string{testAuthority}
	}
	h := stdhttp.Handler(NewHandler(d))
	return &h
}

func do(t *testing.T, h *stdhttp.Handler, method, path, host, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+testAuthority+path, nil)
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	(*h).ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not JSON: %v — body: %s", err, rec.Body.String())
	}
	return doc
}

// --- security envelope ---

func TestHostMismatchFailsClosed(t *testing.T) {
	h := newTestHandler(t, Deps{})
	rec := do(t, h, "GET", "/api/health", "evil.example", "")
	if rec.Code != 403 {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("want problem+json, got %q", ct)
	}
	doc := decodeBody(t, rec)
	if doc["code"] != "forbidden_origin" {
		t.Fatalf("want forbidden_origin, got %v", doc["code"])
	}
}

func TestUnsafeMethodsRequireExactOrigin(t *testing.T) {
	h := newTestHandler(t, Deps{})
	// No route exists for POST /api/health — the point is the guard runs
	// before routing and fails closed on Origin.
	for _, origin := range []string{"", "https://evil.example", "http://127.0.0.1:9999"} {
		rec := do(t, h, "POST", "/api/health", "", origin)
		if rec.Code != 403 {
			t.Fatalf("origin %q: want 403, got %d", origin, rec.Code)
		}
		if decodeBody(t, rec)["code"] != "forbidden_origin" {
			t.Fatalf("origin %q: want forbidden_origin problem", origin)
		}
	}
	// Exact origin passes the guard; the unhandled route answers 404.
	rec := do(t, h, "POST", "/api/health", "", "http://"+testAuthority)
	if rec.Code != 404 {
		t.Fatalf("exact origin should pass the guard, got %d", rec.Code)
	}
}

func TestGetNeedsNoOrigin(t *testing.T) {
	h := newTestHandler(t, Deps{})
	rec := do(t, h, "GET", "/api/health", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// No CORS headers are ever emitted.
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS allow-origin must not be emitted")
	}
	// A trailing-slash request is not a silent 301 — it routes through
	// the guarded 404 like any unmatched path.
	rec = do(t, h, "GET", "/api/health/", "", "")
	if rec.Code != 404 || decodeBody(t, rec)["code"] != "not_found" {
		t.Fatalf("want 404 problem for trailing slash, got %d", rec.Code)
	}
}

// --- reads ---

func TestHealth(t *testing.T) {
	h := newTestHandler(t, Deps{})
	rec := do(t, h, "GET", "/api/health", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if doc := decodeBody(t, rec); doc["status"] != "ok" {
		t.Fatalf("want status ok, got %v", doc)
	}
}

func mkInfo(id string) drepo.ConversationInfo {
	return drepo.ConversationInfo{ID: id, Title: "t-" + id, Route: "openrouter",
		ModelID: "openai/gpt-5", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		MessageCount: 4, ActiveCount: 4}
}

func TestConversationListPaging(t *testing.T) {
	fc := &fakeConversations{infos: []drepo.ConversationInfo{
		mkInfo("c_1"), mkInfo("c_2"), mkInfo("c_3"), mkInfo("c_4"), mkInfo("c_5"),
	}}
	h := newTestHandler(t, Deps{Conversations: fc})

	rec := do(t, h, "GET", "/api/conversations?offset=1&limit=2", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	doc := decodeBody(t, rec)
	data := doc["data"].([]any)
	if len(data) != 2 || data[0].(map[string]any)["id"] != "c_2" {
		t.Fatalf("unexpected page: %v", data)
	}
	page := doc["page"].(map[string]any)
	if page["offset"] != 1.0 || page["limit"] != 2.0 || page["total"] != 5.0 || page["hasMore"] != true {
		t.Fatalf("bad page meta: %v", page)
	}

	// Last page: hasMore false.
	rec = do(t, h, "GET", "/api/conversations?offset=4&limit=2", "", "")
	doc = decodeBody(t, rec)
	if doc["page"].(map[string]any)["hasMore"] != false || len(doc["data"].([]any)) != 1 {
		t.Fatalf("tail page wrong: %v", doc)
	}

	for _, q := range []string{"offset=-1", "limit=0", "limit=101", "limit=abc", "offset=x"} {
		rec = do(t, h, "GET", "/api/conversations?"+q, "", "")
		if rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d", q, rec.Code)
		}
		if decodeBody(t, rec)["code"] != "invalid_argument" {
			t.Fatalf("%s: want invalid_argument", q)
		}
	}
}

func TestConversationDetail404(t *testing.T) {
	h := newTestHandler(t, Deps{Conversations: &fakeConversations{}})
	rec := do(t, h, "GET", "/api/conversations/c_missing", "", "")
	if rec.Code != 404 {
		t.Fatalf("want 404, got %d", rec.Code)
	}
	doc := decodeBody(t, rec)
	if doc["code"] != "not_found" || doc["status"] != 404.0 {
		t.Fatalf("bad problem body: %v", doc)
	}
	if strings.Contains(rec.Body.String(), "no row") {
		t.Fatal("internal cause leaked into problem detail")
	}
}

func mkMessages(t *testing.T) []entity.Message {
	t.Helper()
	user, err := entity.NewMessage(enum.RoleUser, "look at cache.go")
	if err != nil {
		t.Fatal(err)
	}
	user.SetUserType(enum.LLMUserTypeOrigin)
	att, err := valueobject.NewAttachment("file:///home/user/secret/keys.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	att.SetName("../../secret/keys.png")
	att.SetSHA256("deadbeef")
	user.AddAttachment(att)
	user.SetOptionalContent("gemini.thought_signature", "SHOULD-NOT-SERIALIZE")

	assistant, err := entity.NewMessage(enum.RoleAssistant, "found it")
	if err != nil {
		t.Fatal(err)
	}
	assistant.SetFinishReason(enum.FinishReasonToolCalls)
	assistant.SetParts([]entity.MessagePart{
		{Kind: entity.MessagePartText, Text: "found it"},
		{Kind: entity.MessagePartReasoning, Text: "PRIVATE-REASONING"},
		{Kind: entity.MessagePartToolCall, ToolCallID: "call_1"},
		{Kind: entity.MessagePartToolCall, ToolCallID: "call_2"},
	})
	tc, err := entity.NewToolCall("call_1", "read_file", `{"path":"/etc/passwd"}`, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	assistant.AddToolCall(tc)
	tc2, err := entity.NewToolCall("call_2", "run_shell", `{"cmd":"ls -la"}`, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	tc2.SetCallAt(time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC))
	tc2.SetStoredOutput("total 0", "12ms")
	outAtt, err := valueobject.NewAttachment("file:///home/user/secret/out.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	outAtt.SetSHA256("cafebabe")
	tc2.SetOutputAttachments([]valueobject.Attachment{outAtt})
	assistant.AddToolCall(tc2)
	return []entity.Message{user, assistant}
}

func TestMessagesWindowAndSanitization(t *testing.T) {
	hist := mkMessages(t)
	fc := &fakeConversations{
		infos:   []drepo.ConversationInfo{{ID: "c_1", MessageCount: len(hist)}},
		history: hist,
	}
	h := newTestHandler(t, Deps{Conversations: fc})

	// Negative start counts back from the end.
	rec := do(t, h, "GET", "/api/conversations/c_1/messages?start=-1", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	doc := decodeBody(t, rec)
	if doc["effectiveStart"] != 1.0 || doc["total"] != 2.0 ||
		doc["hasMore"] != false || doc["nextStart"] != nil {
		t.Fatalf("bad window meta: %v", doc)
	}
	msgs := doc["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "assistant" {
		t.Fatalf("unexpected messages: %v", msgs)
	}
	calls := msgs[0].(map[string]any)["toolCalls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls: %v", msgs[0])
	}
	parts := msgs[0].(map[string]any)["parts"].([]any)
	if len(parts) != 4 || parts[0].(map[string]any)["text"] != "found it" ||
		parts[1].(map[string]any)["kind"] != "reasoning" ||
		parts[1].(map[string]any)["text"] != "PRIVATE-REASONING" ||
		parts[2].(map[string]any)["toolCallId"] != "call_1" ||
		parts[3].(map[string]any)["toolCallId"] != "call_2" {
		t.Fatalf("ordered message parts projection wrong: %v", parts)
	}
	pending := calls[0].(map[string]any)
	if pending["id"] != "call_1" || pending["name"] != "read_file" ||
		pending["status"] != "pending" ||
		pending["input"] != `{"path":"/etc/passwd"}` {
		t.Fatalf("pending tool call projection wrong: %v", pending)
	}
	if _, ok := pending["output"]; ok {
		t.Fatalf("pending call must omit output: %v", pending)
	}
	done := calls[1].(map[string]any)
	if done["id"] != "call_2" || done["name"] != "run_shell" ||
		done["status"] != "completed" ||
		done["input"] != `{"cmd":"ls -la"}` ||
		done["output"] != "total 0" ||
		done["calledAt"] != "2026-09-28T01:02:03Z" {
		t.Fatalf("completed tool call projection wrong: %v", done)
	}
	outAtts := done["attachments"].([]any)
	if len(outAtts) != 1 {
		t.Fatalf("tool output attachment missing: %v", done)
	}
	oa := outAtts[0].(map[string]any)
	if oa["uri"] != "file:///home/user/secret/out.png" || oa["mediaType"] != "image/png" ||
		oa["modality"] != "image" || len(oa) != 3 {
		t.Fatalf("tool output attachment projection wrong: %v", oa)
	}

	// Provider replay artifacts and sha256 content pins remain internal;
	// normalized reasoning parts and tool I/O cross to the owner UI.
	rec = do(t, h, "GET", "/api/conversations/c_1/messages?start=0&limit=50", "", "")
	body := rec.Body.String()
	for _, leak := range []string{
		"SHOULD-NOT-SERIALIZE", "deadbeef", "cafebabe",
		"optionalContent", "sha256",
	} {
		if strings.Contains(body, leak) {
			t.Fatalf("response leaks %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "PRIVATE-REASONING") {
		t.Fatalf("provider reasoning part missing from history: %s", body)
	}
	for _, want := range []string{
		`"calledAt"`, "/etc/passwd",
		`"uri":"file:///home/user/secret/keys.png"`,
		`"name":"../../secret/keys.png"`,
		`"uri":"file:///home/user/secret/out.png"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected projection field missing %q: %s", want, body)
		}
	}
	doc = decodeBody(t, rec)
	first := doc["messages"].([]any)[0].(map[string]any)
	if first["userType"] != "origin" || first["content"] != "look at cache.go" {
		t.Fatalf("message projection wrong: %v", first)
	}
	atts := first["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments missing: %v", first)
	}
	att := atts[0].(map[string]any)
	if att["mediaType"] != "image/png" || att["modality"] != "image" ||
		att["uri"] != "file:///home/user/secret/keys.png" ||
		att["name"] != "../../secret/keys.png" || len(att) != 4 {
		t.Fatalf("attachment projection wrong: %v", att)
	}
	if doc["hasMore"] != false || doc["effectiveStart"] != 0.0 {
		t.Fatalf("window meta wrong: %v", doc)
	}
}

func TestMessagesCompletedEmptyToolOutput(t *testing.T) {
	msg, _ := entity.NewMessage(enum.RoleAssistant, "")
	call, _ := entity.NewToolCall("call-empty", "exec", "{}", enum.TooltypeOrigin)
	call.SetOutput("")
	msg.AddToolCall(call)
	h := newTestHandler(t, Deps{Conversations: &fakeConversations{
		infos:   []drepo.ConversationInfo{{ID: "c_1", MessageCount: 1}},
		history: []entity.Message{msg},
	}})
	rec := do(t, h, "GET", "/api/conversations/c_1/messages", "", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	msgs := decodeBody(t, rec)["messages"].([]any)
	result := msgs[0].(map[string]any)["toolCalls"].([]any)[0].(map[string]any)
	if result["status"] != "completed" || result["output"] != "" {
		t.Fatalf("empty completed tool output misclassified: %v", result)
	}
}

func TestMessagesForwardPaging(t *testing.T) {
	hist := mkMessages(t)
	hist = append(hist, hist[0], hist[1]) // 4 total
	fc := &fakeConversations{
		infos:   []drepo.ConversationInfo{{ID: "c_9", MessageCount: len(hist)}},
		history: hist,
	}
	h := newTestHandler(t, Deps{Conversations: fc})

	rec := do(t, h, "GET", "/api/conversations/c_9/messages?start=0&limit=3", "", "")
	doc := decodeBody(t, rec)
	if doc["hasMore"] != true || doc["nextStart"] != 3.0 || doc["effectiveStart"] != 0.0 {
		t.Fatalf("forward paging meta wrong: %v", doc)
	}
	if len(doc["messages"].([]any)) != 3 {
		t.Fatalf("want 3 messages, got %v", doc["messages"])
	}

	// start past the end clamps to an empty page.
	rec = do(t, h, "GET", "/api/conversations/c_9/messages?start=99", "", "")
	doc = decodeBody(t, rec)
	if doc["effectiveStart"] != 4.0 || len(doc["messages"].([]any)) != 0 {
		t.Fatalf("overrun window wrong: %v", doc)
	}
}

// --- gateways ---

func TestGatewayListKeyStates(t *testing.T) {
	model, err := entity.NewModel("openai/gpt-5", "")
	if err != nil {
		t.Fatal(err)
	}
	model.SetOptionalContent("gemini.thought_signature", "HIDDEN")
	fg := &fakeGateways{infos: []appgateway.GatewayInfo{
		{ID: "openrouter", Name: "OpenRouter", Style: enum.StyleCompletion,
			Endpoint: "https://openrouter.ai/api/v1", KeyRef: "env:OPENROUTER_API_KEY",
			KeySet: true, Stream: true, Models: []entity.Model{model}},
		{ID: "localvault", Style: enum.StyleMessages, Endpoint: "https://x",
			KeyRef: "secret:gateway/localvault/api_key", KeySet: true},
		{ID: "inline", Style: enum.StyleResponses, Endpoint: "https://y", KeySet: true},
		{ID: "plain", Style: enum.StyleGenerateContent, Endpoint: "https://z"},
		{ID: "busted", Broken: true},
	}}
	h := newTestHandler(t, Deps{Gateways: fg})

	rec := do(t, h, "GET", "/api/gateways", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"env:OPENROUTER_API_KEY", "secret:gateway", "HIDDEN", "apiKey", "keyRef"} {
		if strings.Contains(body, leak) {
			t.Fatalf("gateway response leaks %q: %s", leak, body)
		}
	}
	doc := decodeBody(t, rec)
	data := doc["data"].([]any)
	want := map[string]string{
		"openrouter": "env", "localvault": "vaulted", "inline": "vaulted",
		"plain": "unset", "busted": "broken",
	}
	for _, raw := range data {
		g := raw.(map[string]any)
		if want[g["id"].(string)] != g["keyState"] {
			t.Fatalf("keyState for %v: want %q got %v", g["id"], want[g["id"].(string)], g["keyState"])
		}
	}
	g0 := data[0].(map[string]any)
	if g0["style"] != "completion" || g0["keySet"] != true || g0["stream"] != true {
		t.Fatalf("gateway projection wrong: %v", g0)
	}
	m := g0["models"].([]any)[0].(map[string]any)
	if m["id"] != "openai/gpt-5" {
		t.Fatalf("model projection wrong: %v", m)
	}
}

func TestGatewayDetail404(t *testing.T) {
	h := newTestHandler(t, Deps{Gateways: &fakeGateways{}})
	rec := do(t, h, "GET", "/api/gateways/nope", "", "")
	if rec.Code != 404 || decodeBody(t, rec)["code"] != "not_found" {
		t.Fatalf("want 404 not_found, got %d %s", rec.Code, rec.Body.String())
	}
}

// --- settings ---

func TestSettingsProjectionDropsStorageAndApp(t *testing.T) {
	raw, err := json.Marshal(config.NewConfig())
	if err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(t, Deps{Settings: &fakeSettings{raw: raw}})
	rec := do(t, h, "GET", "/api/settings", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "storage") || strings.Contains(body, "dir") ||
		strings.Contains(body, "./data") || strings.Contains(body, "App") {
		t.Fatalf("settings leaks internals: %s", body)
	}
	doc := decodeBody(t, rec)
	for _, k := range []string{"conversation", "service", "log"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("settings missing section %q: %v", k, doc)
		}
	}
	if doc["conversation"].(map[string]any)["mailbox_cap"] == nil {
		t.Fatalf("conversation section incomplete: %v", doc["conversation"])
	}
}

// --- tools ---

func TestToolsCatalog(t *testing.T) {
	ts, err := valueobject.NewToolSchema("read_file", "Read a file",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		true, enum.ToolPermissionAgent)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := valueobject.NewToolSchema("secret_tool", "internal",
		json.RawMessage(`{"type":"object"}`), false, enum.ToolPermissionSystem)
	if err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(t, Deps{Tools: &fakeConversations{tools: []valueobject.ToolSchema{ts, disabled}}})

	rec := do(t, h, "GET", "/api/tools", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	doc := decodeBody(t, rec)
	data := doc["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("want 2 tools, got %v", data)
	}
	tool := data[0].(map[string]any)
	if tool["name"] != "read_file" || tool["enabled"] != true || tool["permission"] != "agent" {
		t.Fatalf("tool projection wrong: %v", tool)
	}
	params := tool["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters schema not preserved: %v", params)
	}
	if data[1].(map[string]any)["permission"] != "system" {
		t.Fatal("system tool permission must be visible")
	}
}

// --- error mapping ---

func TestNonAppErrorBecomes500WithoutCause(t *testing.T) {
	fc := &fakeConversations{err: errors.New("sqlite: database is locked at /data/conversations/x.db")}
	h := newTestHandler(t, Deps{Conversations: fc})
	rec := do(t, h, "GET", "/api/conversations", "", "")
	if rec.Code != 500 {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	doc := decodeBody(t, rec)
	if doc["code"] != "internal" {
		t.Fatalf("want internal, got %v", doc)
	}
	if strings.Contains(rec.Body.String(), "sqlite") || strings.Contains(rec.Body.String(), "/data/") {
		t.Fatalf("raw error leaked: %s", rec.Body.String())
	}
}
