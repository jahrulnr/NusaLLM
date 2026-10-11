package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
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

type fakeWriter struct {
	deleted       []string
	outcome       appgateway.UpsertOutcome
	err           error
	importOutcome appgateway.UpsertOutcome
	importErr     error
}

func (f *fakeWriter) Upsert(context.Context, appgateway.GatewayInput) (appgateway.UpsertOutcome, error) {
	return f.outcome, f.err
}

func (f *fakeWriter) ImportModels(context.Context, string) (appgateway.UpsertOutcome, error) {
	return f.importOutcome, f.importErr
}

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

func doJSON(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
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

// PUT answers the projection plus the import outcome — `added` is
// always present, `catalogError` absent when the merge succeeded.
func TestPutGatewayCarriesImportOutcome(t *testing.T) {
	r := router(Deps{
		Reads:  &fakeReader{infos: []appgateway.GatewayInfo{{ID: "or", Endpoint: "https://x"}}},
		Writes: &fakeWriter{outcome: appgateway.UpsertOutcome{Added: []string{"m2"}}},
	})
	rec := doJSON(r, "PUT", "/api/gateways/or",
		`{"name":"OR","style":"completion","endpoint":"https://x","models":[{"id":"m1"}]}`)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	added, ok := doc["added"].([]any)
	if !ok || len(added) != 1 || added[0] != "m2" {
		t.Fatalf("added = %v, want [m2]", doc["added"])
	}
	if _, present := doc["catalogError"]; present {
		t.Fatal("catalogError must be omitted when empty")
	}
}

// A catalog failure still answers 200 — the record saved — with the
// failure text in catalogError and the stored catalog in models.
func TestPutGatewayCatalogErrorStill200(t *testing.T) {
	stored, err := entity.NewModel("m1", "")
	if err != nil {
		t.Fatal(err)
	}
	r := router(Deps{
		Reads: &fakeReader{infos: []appgateway.GatewayInfo{
			{ID: "or", Endpoint: "https://x", Models: []entity.Model{stored}},
		}},
		Writes: &fakeWriter{outcome: appgateway.UpsertOutcome{CatalogError: "provider unreachable"}},
	})
	rec := doJSON(r, "PUT", "/api/gateways/or",
		`{"name":"OR","style":"completion","endpoint":"https://x","models":[{"id":"m1"}]}`)
	if rec.Code != 200 {
		t.Fatalf("catalog failure must stay 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["catalogError"] != "provider unreachable" {
		t.Fatalf("catalogError = %v", doc["catalogError"])
	}
	if added, ok := doc["added"].([]any); !ok || len(added) != 0 {
		t.Fatalf("added must serialize as an empty array: %v", doc["added"])
	}
	if models, ok := doc["models"].([]any); !ok || len(models) != 1 {
		t.Fatalf("stored models must ride along: %v", doc["models"])
	}
}

// The refresh endpoint answers the same write shape — here the merged
// id.
func TestImportModelsRoute(t *testing.T) {
	r := router(Deps{
		Reads:  &fakeReader{infos: []appgateway.GatewayInfo{{ID: "or", Endpoint: "https://x"}}},
		Writes: &fakeWriter{importOutcome: appgateway.UpsertOutcome{Added: []string{"m3"}}},
	})
	rec := do(r, "POST", "/api/gateways/or/import-models")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if added, ok := doc["added"].([]any); !ok || len(added) != 1 || added[0] != "m3" {
		t.Fatalf("added = %v, want [m3]", doc["added"])
	}
}

// An absent gateway is a plain 404 — the import outcome never applies.
func TestImportModelsMissingGateway404(t *testing.T) {
	r := router(Deps{
		Reads:  &fakeReader{},
		Writes: &fakeWriter{importErr: apperror.NewNotFoundError("gateway nope", nil)},
	})
	rec := do(r, "POST", "/api/gateways/nope/import-models")
	if rec.Code != 404 {
		t.Fatalf("missing gateway: want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}
