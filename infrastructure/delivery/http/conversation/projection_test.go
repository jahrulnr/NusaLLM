package conversation

// T-19 — every response that returns a room carries the same projection as
// the detail read: a client may treat a write response as the room's new
// state, including the live turn and the compaction verdict.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

type projectionReader struct{ info drepo.ConversationInfo }

func (r projectionReader) Info(context.Context, string) (drepo.ConversationInfo, error) {
	return r.info, nil
}
func (r projectionReader) CanCompact(context.Context, string) (bool, error) { return true, nil }
func (r projectionReader) List(context.Context) ([]drepo.ConversationInfo, error) {
	return []drepo.ConversationInfo{r.info}, nil
}
func (r projectionReader) History(context.Context, string, int, int) ([]entity.Message, error) {
	return nil, nil
}
func (r projectionReader) ContextFooterStats(context.Context, string, string, string) (appconversation.ContextFooterStats, error) {
	return appconversation.ContextFooterStats{}, nil
}

type projectionWriter struct{}

func (projectionWriter) Rename(context.Context, string, string) error { return nil }
func (projectionWriter) Delete(context.Context, string) error         { return nil }
func (projectionWriter) Compact(context.Context, string) error        { return nil }

type projectionLive struct{}

func (projectionLive) ActiveTurn(string) (string, string, bool) {
	return "t_live", "send", true
}

func projectionRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	info := drepo.ConversationInfo{
		ID:           "c_1",
		Title:        "room",
		Route:        "gw1",
		ModelID:      "m1",
		Workspace:    "/ws",
		MessageCount: 3,
		ActiveCount:  3,
		Usage:        valueobject.Usage{},
	}
	r := gin.New()
	Register(r.Group("/api"), Deps{
		Reads:     projectionReader{info: info},
		Writes:    projectionWriter{},
		LiveTurns: projectionLive{},
	})
	return r
}

func doJSON(t *testing.T, r *gin.Engine, method, path, body string) map[string]any {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s body: %v", method, path, err)
	}
	return out
}

// The detail read and a write reply for the same room must be the same
// document while a turn runs — otherwise a client that trusts the write
// response clears the active-turn indicator.
func TestWriteResponseMatchesDetailProjection(t *testing.T) {
	r := projectionRouter(t)
	detail := doJSON(t, r, http.MethodGet, "/api/conversations/c_1", "")
	renamed := doJSON(t, r, http.MethodPatch, "/api/conversations/c_1", `{"title":"renamed"}`)
	compacted := doJSON(t, r, http.MethodPost, "/api/conversations/c_1/compactions", "")

	if detail["activeTurnId"] != "t_live" || detail["activeTurnKind"] != "send" {
		t.Fatalf("detail lost the live turn: %v", detail)
	}
	if detail["canCompact"] != true {
		t.Fatalf("detail lost canCompact: %v", detail)
	}
	if !reflect.DeepEqual(renamed, detail) {
		t.Fatalf("PATCH reply differs from the detail read:\n patch=%v\n detail=%v", renamed, detail)
	}
	if !reflect.DeepEqual(compacted, detail) {
		t.Fatalf("compact reply differs from the detail read:\n compact=%v\n detail=%v", compacted, detail)
	}
}

// The sidebar's list items carry the same projection as the detail read.
func TestListItemsMatchDetailProjection(t *testing.T) {
	r := projectionRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/conversations", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("items = %d", len(page.Data))
	}
	detail := doJSON(t, r, http.MethodGet, "/api/conversations/c_1", "")
	if !reflect.DeepEqual(page.Data[0], detail) {
		t.Fatalf("list item differs from the detail read:\n item=%v\n detail=%v", page.Data[0], detail)
	}
}
