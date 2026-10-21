package toolweb

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

func openCodeCtx() context.Context {
	return service.WithWebProvider(context.Background(), service.WebProvider{
		Kind:     "opencode",
		Endpoint: "https://opencode.ai/zen/go/v1",
		APIKey:   "oc_key",
		Model:    "deepseek-v4-flash",
	})
}

func TestOpenCodeSearchUsesResponsesWebSearchTool(t *testing.T) {
	var sawURL, sawAuth string
	var body map[string]any
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sawURL, sawAuth = r.URL.String(), r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return omniResponse(`{"output":[
			{"type":"web_search_call","action":{"type":"search","query":"go generics"}},
			{"type":"web_search_call","action":{"type":"open_page","url":"https://go.dev/blog"}},
			{"type":"message","content":[{"type":"output_text","text":"Generics landed in 1.18.",
				"annotations":[{"type":"url_citation","url":"https://go.dev/doc","title":"Go Doc","start_index":0,"end_index":8}]}]}]}`), nil
	})}
	tool := toolWithClient(t, stubDef("search", `{"query":"fallback","results":[]}`, nil), client)
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"go generics"}`, enum.TooltypeOrigin)
	res, err := tool.Execute(openCodeCtx(), call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sawURL != "https://opencode.ai/zen/go/v1/responses" || sawAuth != "Bearer oc_key" {
		t.Fatalf("request = %s auth=%s", sawURL, sawAuth)
	}
	if body["model"] != "deepseek-v4-flash" || body["input"] != "go generics" {
		t.Fatalf("body = %#v", body)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search" {
		t.Fatalf("tools = %#v", body["tools"])
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Text), &doc); err != nil {
		t.Fatalf("text = %q: %v", res.Text, err)
	}
	if doc["backend"] != "opencode" || doc["answer"] != "Generics landed in 1.18." {
		t.Fatalf("doc = %#v", doc)
	}
	results, _ := doc["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %#v", doc["results"])
	}
	actions, _ := doc["actions"].([]any)
	if len(actions) != 2 {
		t.Fatalf("actions = %#v", doc["actions"])
	}
}

func TestOpenCodeSearchSendsSessionHeader(t *testing.T) {
	// OpenCode refuses requests without x-opencode-session (MissingSessionID
	// 400) — the tool must send the bound conversation id, mirroring the
	// text adapter. Without a bound conversation the header stays absent.
	for _, tc := range []struct {
		name    string
		convID  string
		wantHdr string
	}{
		{"bound conversation sends session", "conv_42", "conv_42"},
		{"no conversation sends nothing", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sawSession string
			client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				sawSession = r.Header.Get("x-opencode-session")
				return omniResponse(`{"output":[
					{"type":"web_search_call","action":{"type":"search","query":"x"}},
					{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`), nil
			})}
			tool := toolWithClient(t, stubDef("search", `{"results":[]}`, nil), client)
			call, _ := entity.NewToolCall("c1", "web_search", `{"query":"x"}`, enum.TooltypeOrigin)
			ctx := openCodeCtx()
			if tc.convID != "" {
				ctx = service.WithConversationID(ctx, tc.convID)
			}
			if _, err := tool.Execute(ctx, call, nil); err != nil {
				t.Fatal(err)
			}
			if sawSession != tc.wantHdr {
				t.Fatalf("x-opencode-session = %q, want %q", sawSession, tc.wantHdr)
			}
		})
	}
}

func TestOpenCodeDroppedToolFallsBackToSearchwire(t *testing.T) {
	// oa-compat lanes silently drop non-function tools: the response is a
	// plain message with no web_search_call. That must fail loudly into the
	// searchwire fallback, not pass an ungrounded answer off as a search.
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return omniResponse(`{"output":[{"type":"message","content":[{"type":"output_text","text":"I think generics exist"}]}]}`), nil
	})}
	var fallbackRan bool
	def := stubDef("search", "", nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		fallbackRan = true
		return json.RawMessage(`{"query":"go generics","results":[]}`), nil
	}
	tool := toolWithClient(t, def, client)
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"go generics"}`, enum.TooltypeOrigin)
	res, err := tool.Execute(openCodeCtx(), call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !fallbackRan {
		t.Fatal("searchwire fallback did not run")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Text), &doc); err != nil {
		t.Fatalf("text = %q: %v", res.Text, err)
	}
	if doc["backend"] != "searchwire" || !strings.Contains(doc["backendFallback"].(string), "no web_search_call") {
		t.Fatalf("doc = %#v", doc)
	}
}

func TestOpenCodeFetchStaysOnSearchwire(t *testing.T) {
	var called bool
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return omniResponse(`{}`), nil
	})}
	var ran bool
	def := stubDef("fetch", "", nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		ran = true
		return json.RawMessage(`{"text":"page"}`), nil
	}
	tool := toolWithClient(t, def, client)
	call, _ := entity.NewToolCall("c1", "web_fetch", `{"url":"https://x"}`, enum.TooltypeOrigin)
	if _, err := tool.Execute(openCodeCtx(), call, nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("opencode has no fetch API — must not call its endpoint")
	}
	if !ran {
		t.Fatal("searchwire fetch did not run")
	}
}

func TestOpenCodeWithoutModelStaysOnSearchwire(t *testing.T) {
	var ran bool
	def := stubDef("search", `{"query":"x","results":[]}`, nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		ran = true
		return json.RawMessage(`{"query":"x","results":[]}`), nil
	}
	tool, _ := New(def, nil)
	ctx := service.WithWebProvider(context.Background(), service.WebProvider{
		Kind:     "opencode",
		Endpoint: "https://opencode.ai/zen/go/v1",
		APIKey:   "k",
	})
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"x"}`, enum.TooltypeOrigin)
	if _, err := tool.Execute(ctx, call, nil); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("empty model must not attempt the responses path")
	}
}
