package fs

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gin-gonic/gin"
	repofiletree "github.com/jahrulnr/nusallm/infrastructure/repository/filetree"
)

func TestBrowseTruncated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	for i := range maxEntries + 3 {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir%05d", i)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	r := gin.New()
	Register(r.Group("/api"), repofiletree.NewLocal())

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

// The problem code must come from the contract's closed enum —
// "forbidden" is not a member (the catalog's 403 code is
// forbidden_origin). An unreadable directory emits the undocumented
// string today.
func TestBrowseUnreadableDirUsesCatalogCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits differ on Windows")
	}
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o700) }() // let TempDir clean up

	r := gin.New()
	Register(r.Group("/api"), repofiletree.NewLocal())
	req := httptest.NewRequest("GET", "/api/fs/browse?path="+locked, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	catalog := map[string]bool{
		"invalid_argument": true, "forbidden_origin": true, "not_found": true,
		"interrupted": true, "gone": true, "input_too_large": true,
		"queue_full": true, "internal": true, "upstream_error": true,
		"unavailable": true, "upstream_timeout": true,
	}
	if !catalog[fmt.Sprint(doc["code"])] {
		t.Fatalf("code %q is not in the contract's closed enum", doc["code"])
	}
}

// A Stat that fails for permission reasons is not a missing directory —
// today every Stat error reports 404 "no such directory", hiding the
// real cause from the picker.
func TestBrowseStatPermissionIsNotNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), permTree{})
	req := httptest.NewRequest("GET", "/api/fs/browse?path=/locked", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == 404 {
		t.Fatal("permission-denied stat reported as not_found")
	}
}

// permTree denies every path — the shape of a directory the picker can
// see but not open.
type permTree struct{}

func (permTree) Stat(string) (fs.FileInfo, error)      { return nil, fs.ErrPermission }
func (permTree) ReadDir(string) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }
func (permTree) HomeDir() (string, error)              { return "/home/x", nil }
