package toolweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jahrulnr/searchwire"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func omniResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func toolWithClient(t *testing.T, def searchwire.ToolDef, client *http.Client) service.Tool {
	t.Helper()
	base, err := New(def, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := base.(*Tool)
	tool.httpClient = client
	return tool
}

func omniCtx() context.Context {
	return service.WithWebProvider(context.Background(), service.WebProvider{
		Kind:     "omniroute",
		Endpoint: "http://router.local:20128/v1",
		APIKey:   "omni_key",
	})
}

func TestOmniRouteSearchPreferredWhenBound(t *testing.T) {
	var sawURL, sawAuth string
	var body map[string]any
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sawURL, sawAuth = r.URL.String(), r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return omniResponse(`{"provider":"context7","results":[{"title":"Go","url":"https://go.dev","snippet":"site"}],"answer":null}`), nil
	})}
	tool := toolWithClient(t, stubDef("search", `{"query":"fallback","results":[]}`, nil), client)
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"go","limit":5}`, enum.TooltypeOrigin)
	res, err := tool.Execute(omniCtx(), call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sawURL != "http://router.local:20128/v1/search" || sawAuth != "Bearer omni_key" {
		t.Fatalf("request = %s auth=%s", sawURL, sawAuth)
	}
	if body["query"] != "go" || body["max_results"] != float64(5) {
		t.Fatalf("body = %#v", body)
	}
	if !strings.Contains(res.Text, `"Title": "Go"`) && !strings.Contains(res.Text, `"Title":"Go"`) {
		t.Fatalf("text = %s", res.Text)
	}
}

func TestOmniRouteFetchPreferredWhenBound(t *testing.T) {
	var sawURL string
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		sawURL = r.URL.String()
		return omniResponse(`{"provider":"jina-reader","url":"https://go.dev","title":"Go","content":{"format":"markdown","text":"page text","length":9}}`), nil
	})}
	tool := toolWithClient(t, stubDef("fetch", `{"text":"fallback"}`, nil), client)
	call, _ := entity.NewToolCall("c1", "web_fetch", `{"url":"https://go.dev","max_bytes":1000}`, enum.TooltypeOrigin)
	res, err := tool.Execute(omniCtx(), call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sawURL != "http://router.local:20128/v1/web/fetch" {
		t.Fatalf("url = %s", sawURL)
	}
	if !strings.Contains(res.Text, "page text") {
		t.Fatalf("text = %s", res.Text)
	}
}

func TestOmniRouteFailureFallsBackToSearchwire(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Status:     "400 Bad Request",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"No credentials configured for any web-fetch provider."}}`)),
		}, nil
	})}
	var fallbackRan bool
	def := stubDef("fetch", "", nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		fallbackRan = true
		return json.RawMessage(`{"text":"searchwire text"}`), nil
	}
	tool := toolWithClient(t, def, client)
	call, _ := entity.NewToolCall("c1", "web_fetch", `{"url":"https://go.dev"}`, enum.TooltypeOrigin)
	res, err := tool.Execute(omniCtx(), call, nil)
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
	if doc["text"] != "searchwire text" || doc["backend"] != "searchwire" {
		t.Fatalf("doc = %#v", doc)
	}
	if _, ok := doc["backendFallback"].(string); !ok {
		t.Fatalf("expected backendFallback note, doc = %#v", doc)
	}
}

func TestNonOmniRouteProviderUsesSearchwire(t *testing.T) {
	var ran bool
	def := stubDef("search", `{"query":"x","results":[]}`, nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		ran = true
		return json.RawMessage(`{"query":"x","results":[]}`), nil
	}
	tool := toolWithClient(t, def, nil)
	ctx := service.WithWebProvider(context.Background(), service.WebProvider{
		Kind:     "openrouter",
		Endpoint: "https://openrouter.ai/api/v1",
		APIKey:   "k",
	})
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"x"}`, enum.TooltypeOrigin)
	if _, err := tool.Execute(ctx, call, nil); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("searchwire fallback did not run for openrouter")
	}
}
