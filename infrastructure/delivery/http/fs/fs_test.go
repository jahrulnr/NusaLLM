package fs

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBrowseTruncated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	for i := 0; i < maxEntries+3; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir%05d", i)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	r := gin.New()
	Register(r.Group("/api"))

	req := httptest.NewRequest("GET", "/api/fs/browse?path="+root, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out browseDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != maxEntries {
		t.Fatalf("entries = %d, want %d", len(out.Entries), maxEntries)
	}
	if !out.Truncated {
		t.Fatal("want truncated true when listing exceeds cap")
	}
}
