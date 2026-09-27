package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// --- write fakes ---

type fakeConvWrites struct {
	renamed   map[string]string
	deleted   []string
	compacted []string
	ctxErr    error // ctx state observed inside the job call
	err       error
}

func (f *fakeConvWrites) Rename(_ context.Context, id, title string) error {
	if f.renamed == nil {
		f.renamed = map[string]string{}
	}
	f.renamed[id] = title
	return f.err
}

func (f *fakeConvWrites) Delete(ctx context.Context, id string) error {
	f.ctxErr = ctx.Err()
	f.deleted = append(f.deleted, id)
	return f.err
}

func (f *fakeConvWrites) Compact(ctx context.Context, id string) error {
	f.ctxErr = ctx.Err()
	f.compacted = append(f.compacted, id)
	return f.err
}

type fakeGwWrites struct {
	lastIn  appgateway.GatewayInput
	deleted []string
	err     error
}

func (f *fakeGwWrites) Upsert(_ context.Context, in appgateway.GatewayInput) error {
	f.lastIn = in
	return f.err
}

func (f *fakeGwWrites) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.err
}

type fakeSettingsWriter struct {
	key, value string
	doc        []byte
	err        error
}

func (f *fakeSettingsWriter) Set(key, rawValue string) ([]byte, error) {
	f.key, f.value = key, rawValue
	return f.doc, f.err
}

// doBody issues a mutation request with a body; the Origin header is
// exact by default so handler behavior — not the guard — is under test.
func doBody(t *testing.T, h *stdhttp.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+testAuthority+path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Origin", "http://"+testAuthority)
	rec := httptest.NewRecorder()
	(*h).ServeHTTP(rec, req)
	return rec
}

// --- body envelope rules ---

func TestBodyEnvelopeRules(t *testing.T) {
	fcw := &fakeConvWrites{}
	h := newTestHandler(t, Deps{ConversationWrites: fcw})

	cases := []struct {
		name, method, path, ct, body string
	}{
		{"wrong content type", "PATCH", "/api/conversations/c_1", "text/plain", `{"title":"x"}`},
		{"no content type", "PATCH", "/api/conversations/c_1", "", `{"title":"x"}`},
		{"malformed json", "PATCH", "/api/conversations/c_1", "application/json", `{"title":`},
		{"unknown field", "PATCH", "/api/conversations/c_1", "application/json", `{"title":"x","extra":1}`},
		{"two documents", "PATCH", "/api/conversations/c_1", "application/json", `{"title":"x"}{"title":"y"}`},
		{"missing title", "PATCH", "/api/conversations/c_1", "application/json", `{}`},
		{"bad id", "PATCH", "/api/conversations/..", "application/json", `{"title":"x"}`},
	}
	for _, tc := range cases {
		rec := doBody(t, h, tc.method, tc.path, tc.ct, tc.body)
		if rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d (%s)", tc.name, rec.Code, rec.Body.String())
		}
		if decodeBody(t, rec)["code"] != "invalid_argument" {
			t.Fatalf("%s: want invalid_argument, got %s", tc.name, rec.Body.String())
		}
	}
	if len(fcw.renamed) != 0 {
		t.Fatal("rejected requests must not reach the use case")
	}

	// Over the 1 MiB body cap → 413.
	big := `{"title":"` + strings.Repeat("x", 1<<20) + `"}`
	rec := doBody(t, h, "PATCH", "/api/conversations/c_1", "application/json", big)
	if rec.Code != 413 || decodeBody(t, rec)["code"] != "input_too_large" {
		t.Fatalf("want 413 input_too_large, got %d %s", rec.Code, rec.Body.String())
	}
}

// --- conversation mutations ---

func TestRenameConversation(t *testing.T) {
	fc := &fakeConversations{infos: []drepo.ConversationInfo{mkInfo("c_1")}}
	fcw := &fakeConvWrites{}
	h := newTestHandler(t, Deps{Conversations: fc, ConversationWrites: fcw})

	rec := doBody(t, h, "PATCH", "/api/conversations/c_1", "application/json", `{"title":"New Title"}`)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fcw.renamed["c_1"] != "New Title" {
		t.Fatalf("rename did not reach the use case: %v", fcw.renamed)
	}
	doc := decodeBody(t, rec)
	if doc["id"] != "c_1" || doc["title"] != "t-c_1" {
		t.Fatalf("want sanitized refreshed conversation, got %v", doc)
	}
}

func TestDeleteConversationIs204AndRunsUnderJobsCtx(t *testing.T) {
	fcw := &fakeConvWrites{}
	h := newTestHandler(t, Deps{
		Conversations:      &fakeConversations{},
		ConversationWrites: fcw,
		Jobs:               context.Background(),
	})

	req := httptest.NewRequest("DELETE", "http://"+testAuthority+"/api/conversations/c_1", nil)
	req.Header.Set("Origin", "http://"+testAuthority)
	// A request-scoped context that is already abandoned: the job ctx
	// must be the server lifecycle, so ctx.Err() inside Delete is nil.
	deadCtx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	(*h).ServeHTTP(rec, req.WithContext(deadCtx))

	if rec.Code != 204 {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(fcw.deleted) != 1 || fcw.deleted[0] != "c_1" {
		t.Fatalf("delete did not reach use case: %v", fcw.deleted)
	}
	if fcw.ctxErr != nil {
		t.Fatalf("abandoned request canceled an accepted job: %v", fcw.ctxErr)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 must have no body, got %q", rec.Body.String())
	}
}

func TestCompactConversation204(t *testing.T) {
	fcw := &fakeConvWrites{}
	h := newTestHandler(t, Deps{ConversationWrites: fcw, Jobs: context.Background()})
	req := httptest.NewRequest("POST", "http://"+testAuthority+"/api/conversations/c_1/compactions", nil)
	req.Header.Set("Origin", "http://"+testAuthority)
	rec := httptest.NewRecorder()
	(*h).ServeHTTP(rec, req)
	if rec.Code != 204 || len(fcw.compacted) != 1 {
		t.Fatalf("want 204 + one compaction, got %d %v", rec.Code, fcw.compacted)
	}
}

// --- gateway mutations ---

func TestPutGatewayBodyRules(t *testing.T) {
	fgw := &fakeGwWrites{}
	fg := &fakeGateways{infos: []appgateway.GatewayInfo{{ID: "openrouter", Name: "OR", Endpoint: "https://x"}}}
	h := newTestHandler(t, Deps{Gateways: fg, GatewayWrites: fgw})
	valid := `{"name":"OR","style":"completion","endpoint":"https://x","models":[{"id":"gpt-5"}]}`

	for _, tc := range []struct{ name, body string }{
		{"body id rejected", `{"id":"openrouter","name":"OR","style":"completion","endpoint":"https://x"}`},
		{"optionalContent rejected", `{"name":"OR","style":"completion","endpoint":"https://x","models":[{"id":"gpt-5","optionalContent":{"k":"v"}}]}`},
		{"apiKey null rejected", `{"name":"OR","style":"completion","endpoint":"https://x","apiKey":null}`},
		{"unknown top field", `{"name":"OR","style":"completion","endpoint":"https://x","zzz":1}`},
	} {
		rec := doBody(t, h, "PUT", "/api/gateways/openrouter", "application/json", tc.body)
		if rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}

	rec := doBody(t, h, "PUT", "/api/gateways/openrouter", "application/json", valid)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	in := fgw.lastIn
	if in.ID != "openrouter" || in.Name != "OR" || in.Endpoint != "https://x" ||
		in.Style.String() != "completion" || len(in.Models) != 1 || in.Models[0].ID != "gpt-5" {
		t.Fatalf("input not mapped: %+v", in)
	}
	if in.ApiKey != nil {
		t.Fatal("absent apiKey must stay nil (keep)")
	}
	if in.Models[0].OptionalContent != nil {
		t.Fatal("optionalContent must never arrive from the wire")
	}
	doc := decodeBody(t, rec)
	if doc["id"] != "openrouter" || doc["keyState"] != "unset" {
		t.Fatalf("want sanitized gateway, got %v", doc)
	}
}

func TestPutGatewayApiKeyTriState(t *testing.T) {
	fgw := &fakeGwWrites{}
	fg := &fakeGateways{infos: []appgateway.GatewayInfo{{ID: "g", Endpoint: "https://x"}}}
	h := newTestHandler(t, Deps{Gateways: fg, GatewayWrites: fgw})
	base := `{"name":"G","style":"completion","endpoint":"https://x","apiKey":%s}`

	for _, tc := range []struct {
		json string
		want *string
	}{
		{`""`, ptr("")},               // clear
		{`"sk-live"`, ptr("sk-live")}, // set
	} {
		rec := doBody(t, h, "PUT", "/api/gateways/g", "application/json", fmt.Sprintf(base, tc.json))
		if rec.Code != 200 {
			t.Fatalf("apiKey %s: want 200, got %d %s", tc.json, rec.Code, rec.Body.String())
		}
		if fgw.lastIn.ApiKey == nil || *fgw.lastIn.ApiKey != *tc.want {
			t.Fatalf("apiKey %s: want %v, got %v", tc.json, *tc.want, fgw.lastIn.ApiKey)
		}
	}
	// absent → keep
	rec := doBody(t, h, "PUT", "/api/gateways/g", "application/json",
		`{"name":"G","style":"completion","endpoint":"https://x"}`)
	if rec.Code != 200 || fgw.lastIn.ApiKey != nil {
		t.Fatalf("absent apiKey must keep (nil), got code %d", rec.Code)
	}
}

func ptr(s string) *string { return &s }

func TestDeleteGateway204(t *testing.T) {
	fgw := &fakeGwWrites{}
	h := newTestHandler(t, Deps{GatewayWrites: fgw})
	rec := doBody(t, h, "DELETE", "/api/gateways/g", "", "")
	if rec.Code != 204 || len(fgw.deleted) != 1 || fgw.deleted[0] != "g" {
		t.Fatalf("want 204 + delete, got %d %v", rec.Code, fgw.deleted)
	}
}

// --- settings mutation ---

func TestPatchSettingsAllowlist(t *testing.T) {
	doc := []byte(`{"conversation":{"mailbox_cap":128,"max_turn_steps":512,"compact_budget_pct":90,"compact_keep_tail":4},"service":{"header_timeout":"30s","idle_timeout":"90s"},"log":{"bodies":false,"max_lines":2000,"max_files":8},"storage":{"dir":"/root/secret-path"}}`)
	fsw := &fakeSettingsWriter{doc: doc}
	h := newTestHandler(t, Deps{SettingsWriter: fsw, Settings: &fakeSettings{raw: doc}})

	rec := doBody(t, h, "PATCH", "/api/settings", "application/json",
		`{"key":"service.idle_timeout","value":"2m"}`)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if fsw.key != "service.idle_timeout" || fsw.value != `"2m"` {
		t.Fatalf("Set called with %q %q", fsw.key, fsw.value)
	}
	out := decodeBody(t, rec)
	if out["storage"] != nil {
		t.Fatal("response must not contain the storage section")
	}

	for _, tc := range []struct{ name, body string }{
		{"non-whitelisted leaf", `{"key":"storage.dir","value":"/x"}`},
		{"invented leaf", `{"key":"conversation.secret","value":1}`},
		{"null value", `{"key":"log.bodies","value":null}`},
		{"no section", `{"key":"bodies","value":true}`},
		{"missing key", `{"value":true}`},
		{"missing value", `{"key":"log.bodies"}`},
	} {
		fsw.key = ""
		rec := doBody(t, h, "PATCH", "/api/settings", "application/json", tc.body)
		if rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d", tc.name, rec.Code)
		}
		if fsw.key != "" {
			t.Fatalf("%s: rejected key reached Set", tc.name)
		}
	}
}

// --- 429 disambiguation ---

func TestRateLimitCodes(t *testing.T) {
	// Local mailbox pressure → queue_full.
	fc := &fakeConversations{err: apperror.New(429, "conversation queue full", nil)}
	h := newTestHandler(t, Deps{Conversations: fc})
	rec := do(t, h, "GET", "/api/conversations", "", "")
	if rec.Code != 429 || decodeBody(t, rec)["code"] != "queue_full" {
		t.Fatalf("local 429: want 429 queue_full, got %d %s", rec.Code, rec.Body.String())
	}
	// Provider rate limit → upstream_error.
	fc.err = apperror.FromStatus(429, "rate limit exceeded", errors.New("retry-after 30"))
	rec = do(t, h, "GET", "/api/conversations", "", "")
	if rec.Code != 429 || decodeBody(t, rec)["code"] != "upstream_error" {
		t.Fatalf("upstream 429: want 429 upstream_error, got %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "retry-after") {
		t.Fatal("upstream cause leaked")
	}
}

// --- paging edge cases ---

func TestPagingEdgeCases(t *testing.T) {
	h := newTestHandler(t, Deps{Conversations: &fakeConversations{infos: []drepo.ConversationInfo{mkInfo("c_1")}}})

	// Max-int offset must not overflow the slice bound computation.
	rec := do(t, h, "GET", "/api/conversations?offset=9223372036854775807&limit=30", "", "")
	if rec.Code != 200 {
		t.Fatalf("max-int offset: want 200, got %d", rec.Code)
	}
	doc := decodeBody(t, rec)
	if len(doc["data"].([]any)) != 0 || doc["page"].(map[string]any)["hasMore"] != false {
		t.Fatalf("max-int offset page wrong: %v", doc["page"])
	}

	// Present-but-empty params are a 400, not a default.
	for _, q := range []string{"?offset=", "?limit=", "?offset=&limit=30", "?start=&limit=5"} {
		path := "/api/conversations" + q
		if strings.HasPrefix(q, "?start") {
			path = "/api/conversations/c_1/messages" + q
		}
		rec = do(t, h, "GET", path, "", "")
		if rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d", path, rec.Code)
		}
	}
}

// --- settings leaf projection ---

func TestSettingsProjectionDropsUnlistedLeaves(t *testing.T) {
	raw := []byte(`{"conversation":{"mailbox_cap":64,"secret_thing":"bearer-x"},"service":{"header_timeout":"30s"},"log":{"bodies":false},"storage":{"dir":"/root/x"}}`)
	h := newTestHandler(t, Deps{Settings: &fakeSettings{raw: raw}})
	rec := do(t, h, "GET", "/api/settings", "", "")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret_thing") || strings.Contains(rec.Body.String(), "bearer-x") {
		t.Fatalf("unlisted leaf crossed the wire: %s", rec.Body.String())
	}
	conv := decodeBody(t, rec)["conversation"].(map[string]any)
	if len(conv) != 1 || conv["mailbox_cap"] != 64.0 {
		t.Fatalf("conversation section must carry whitelisted leaves only: %v", conv)
	}
}
