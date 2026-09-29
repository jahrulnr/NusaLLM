package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// fakes satisfy the consumer-defined interfaces, not the app.
type fakeReader struct{ infos []appgateway.GatewayInfo }

func (f *fakeReader) List(context.Context) ([]appgateway.GatewayInfo, error) {
	return f.infos, nil
}

func (f *fakeReader) Get(_ context.Context, id string) (appgateway.GatewayInfo, error) {
	for _, i := range f.infos {
		if i.ID == id {
			return i, nil
		}
	}
	return appgateway.GatewayInfo{}, nil
}

func (f *fakeReader) DiscoverModels(context.Context, string) ([]string, error) {
	return nil, nil
}

func (f *fakeReader) DiscoverProviders(context.Context, string, string) ([]dservice.UpstreamProvider, error) {
	return nil, nil
}

type fakeWriter struct{ deleted []string }

func (f *fakeWriter) Upsert(context.Context, appgateway.GatewayInput) error { return nil }

func (f *fakeWriter) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func router(d Deps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), d)
	return r
}

func do(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestNilReadsLeavesReadRoutesUnregistered(t *testing.T) {
	// Writes alone mount nothing readable — NoRoute answers the GETs.
	r := router(Deps{Writes: &fakeWriter{}})
	for _, p := range []string{
		"/api/gateways",
		"/api/gateways/g",
		"/api/gateways/g/available-models",
		"/api/gateways/g/providers",
	} {
		if rec := do(r, "GET", p); rec.Code != 404 {
			t.Fatalf("GET %s: want 404, got %d", p, rec.Code)
		}
	}
}

func TestNilWritesLeavesWriteRoutesUnregistered(t *testing.T) {
	// The path exists for GET; the mutations still stay unregistered.
	r := router(Deps{Reads: &fakeReader{}})
	for _, m := range []string{"PUT", "DELETE"} {
		if rec := do(r, m, "/api/gateways/g"); rec.Code != 404 {
			t.Fatalf("%s /api/gateways/g: want 404, got %d", m, rec.Code)
		}
	}
}

func TestListGateways(t *testing.T) {
	r := router(Deps{Reads: &fakeReader{infos: []appgateway.GatewayInfo{
		{ID: "openrouter", Name: "OpenRouter", Style: enum.StyleCompletion,
			Endpoint: "https://openrouter.ai/api/v1", Stream: true},
	}}})
	rec := do(r, "GET", "/api/gateways")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var page gatewayPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != "openrouter" ||
		page.Data[0].Style != "completion" || page.Data[0].KeyState != "unset" {
		t.Fatalf("unexpected list projection: %+v", page.Data)
	}
}
