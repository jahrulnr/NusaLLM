package tool

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// fakes satisfy the consumer-defined interface, not the app.
type fakeCatalog struct {
	schemas []valueobject.ToolSchema
	room    string
}

func (f *fakeCatalog) ToolSchemas(_ context.Context, room string) ([]valueobject.ToolSchema, error) {
	f.room = room
	return f.schemas, nil
}

func router(d Deps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), d)
	return r
}

func TestNilCatalogLeavesRouteUnregistered(t *testing.T) {
	r := router(Deps{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tools", nil))
	if rec.Code != 404 {
		t.Fatalf("GET /api/tools: want 404, got %d", rec.Code)
	}
}

func TestListTools(t *testing.T) {
	ts, err := valueobject.NewToolSchema("read_file", "Read a file",
		json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		true, enum.ToolPermissionAgent)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &fakeCatalog{schemas: []valueobject.ToolSchema{ts}}
	r := router(Deps{Tools: catalog})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tools?conversationId=room-1", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []toolDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 || out.Data[0].Name != "read_file" ||
		!out.Data[0].Enabled || out.Data[0].Permission != "agent" {
		t.Fatalf("unexpected catalog projection: %+v", out.Data)
	}
	if catalog.room != "room-1" {
		t.Fatalf("queried catalog for %q", catalog.room)
	}
}
