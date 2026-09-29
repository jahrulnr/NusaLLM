package setting

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakes satisfy the consumer-defined interfaces, not the app.
type fakeReader struct{ raw []byte }

func (f *fakeReader) Show() ([]byte, error) { return f.raw, nil }

type fakeWriter struct{}

func (f *fakeWriter) Set(key, rawValue string) ([]byte, error) { return nil, nil }

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

func TestNilReadsLeavesReadRouteUnregistered(t *testing.T) {
	r := router(Deps{Writes: &fakeWriter{}})
	if rec := do(r, "GET", "/api/settings"); rec.Code != 404 {
		t.Fatalf("GET /api/settings: want 404, got %d", rec.Code)
	}
}

func TestNilWritesLeavesWriteRouteUnregistered(t *testing.T) {
	// The path exists for GET; PATCH still stays unregistered.
	r := router(Deps{Reads: &fakeReader{raw: []byte(`{}`)}})
	if rec := do(r, "PATCH", "/api/settings"); rec.Code != 404 {
		t.Fatalf("PATCH /api/settings: want 404, got %d", rec.Code)
	}
}

func TestGetSettings(t *testing.T) {
	raw := []byte(`{"conversation":{"mailbox_cap":64,"secret_thing":"x"},"storage":{"dir":"/root/x"}}`)
	r := router(Deps{Reads: &fakeReader{raw: raw}})
	rec := do(r, "GET", "/api/settings")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var doc map[string]map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["conversation"]["mailbox_cap"]) != "64" {
		t.Fatalf("whitelisted leaf missing: %s", rec.Body.String())
	}
	if _, ok := doc["storage"]; ok {
		t.Fatalf("unlisted section crossed: %s", rec.Body.String())
	}
}
