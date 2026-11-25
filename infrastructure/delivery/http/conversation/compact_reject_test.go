package conversation

// T-31 — a compaction the user cannot make is a request-level refusal: 400
// invalid_argument carrying the reason, never a 500 server fault.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// rejectingWriter is a room store whose compaction refuses the way the job
// does when the room has nothing to fold.
type rejectingWriter struct{ err error }

func (w rejectingWriter) Rename(context.Context, string, string) error { return nil }
func (w rejectingWriter) Delete(context.Context, string) error         { return nil }
func (w rejectingWriter) Compact(context.Context, string) error        { return w.err }

func TestCompactRefusalIs400InvalidArgument(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), Deps{
		Reads:  projectionReader{},
		Writes: rejectingWriter{err: apperror.New(400, "nothing to compact: 1 message(s) with keep-tail 4", errors.New("conversation c_1 has no new foldable history"))},
		// no LiveTurns needed: the write fails before the projection
	})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/c_1/compactions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var problem struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem body: %v (%s)", err, rec.Body.String())
	}
	if problem.Status != 400 || problem.Code != "invalid_argument" {
		t.Fatalf("problem = %+v, want 400 invalid_argument", problem)
	}
	for _, want := range []string{"nothing to compact", "keep-tail 4"} {
		if !strings.Contains(problem.Detail, want) {
			t.Fatalf("detail %q does not carry the reason (%q)", problem.Detail, want)
		}
	}
}
