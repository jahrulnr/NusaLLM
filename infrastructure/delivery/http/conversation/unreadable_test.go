package conversation

// T-34 — a record whose tool call cannot be decoded must not fail the read:
// the call projects as an unreadable placeholder and the response is still
// 200 with the rest of the transcript.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

// historyReader answers one assistant message carrying an unreadable call
// next to a healthy one — the shape the repository now produces.
type historyReader struct{ projectionReader }

func (h historyReader) History(context.Context, string, int, int) ([]entity.Message, error) {
	msg, err := entity.NewMessage(enum.RoleAssistant, "worked on it")
	if err != nil {
		return nil, err
	}
	bad := entity.NewUnreadableToolCall("call_bad", "exec", "{}")
	bad.SetStoredOutput("boom", "")
	ok, _ := entity.NewToolCall("call_ok", "read_file", `{"path":"a"}`, enum.TooltypeOrigin)
	ok.SetOutput("contents")
	if err := msg.AddToolCall(bad); err != nil {
		return nil, err
	}
	if err := msg.AddToolCall(ok); err != nil {
		return nil, err
	}
	return []entity.Message{msg}, nil
}

func TestUnreadableCallStillRenders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), Deps{
		Reads:  historyReader{projectionReader{}},
		Writes: rejectingWriter{},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/conversations/c_1/messages", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Messages []struct {
			ToolCalls []map[string]any `json:"toolCalls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v (%s)", err, rec.Body.String())
	}
	if len(body.Messages) != 1 || len(body.Messages[0].ToolCalls) != 2 {
		t.Fatalf("calls = %+v, want both projected", body.Messages)
	}
	bad := body.Messages[0].ToolCalls[0]
	if bad["type"] != "unknown" || bad["status"] != "unknown" {
		t.Fatalf("unreadable call = %+v, want unknown/unknown", bad)
	}
	if bad["output"] != "boom" {
		t.Fatalf("unreadable call lost its text: %+v", bad)
	}
	good := body.Messages[0].ToolCalls[1]
	if good["type"] != "origin" || good["status"] != "unknown" {
		// No verdict recorded on this one either: the projection says so
		// rather than inventing ok.
		t.Fatalf("healthy record = %+v, want origin/unknown", good)
	}
}
