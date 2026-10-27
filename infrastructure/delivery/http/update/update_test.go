package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	appupdate "github.com/jahrulnr/nusallm/application/update"
)

type fakeChecker struct {
	status, check, install, progress atomic.Int32
	info                             appupdate.Info
	installErr                       error
	prog                             appupdate.Progress
}

func (f *fakeChecker) Status(context.Context) appupdate.Info {
	f.status.Add(1)
	return f.info
}

func (f *fakeChecker) Check(context.Context) appupdate.Info {
	f.check.Add(1)
	return f.info
}

func (f *fakeChecker) Install() (appupdate.Progress, error) {
	f.install.Add(1)
	return f.prog, f.installErr
}

func (f *fakeChecker) Progress() appupdate.Progress {
	f.progress.Add(1)
	return f.prog
}

func engine(t *testing.T, c Checker) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), Deps{Checks: c})
	return r
}

func serve(t *testing.T, r http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return got
}

func TestStatusProjectsAvailableRelease(t *testing.T) {
	at := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	fc := &fakeChecker{info: appupdate.Info{
		Current: "0.0.0", Enabled: true, Installable: true, Latest: "0.1.0", Available: true,
		PublishedAt: at, URL: "https://github.test/r/0.1.0", Notes: "### Added",
		InstallCommand: "curl install | bash", CheckedAt: at,
	}}
	rec := serve(t, engine(t, fc), http.MethodGet, "/api/update")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got := decode(t, rec)
	want := map[string]any{
		"current": "0.0.0", "enabled": true, "installable": true, "latest": "0.1.0", "available": true,
		"releaseUrl": "https://github.test/r/0.1.0", "notes": "### Added",
		"installCommand": "curl install | bash", "error": "",
		"publishedAt": "2026-10-28T01:00:00Z", "checkedAt": "2026-10-28T01:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v (body %s)", k, got[k], v, rec.Body.String())
		}
	}
	if fc.status.Load() != 1 || fc.check.Load() != 0 {
		t.Fatalf("GET called Status %d / Check %d", fc.status.Load(), fc.check.Load())
	}
}

func TestUnknownTimesAreNullNotEpoch(t *testing.T) {
	fc := &fakeChecker{info: appupdate.Info{Current: "dev"}}
	got := decode(t, serve(t, engine(t, fc), http.MethodGet, "/api/update"))
	if got["publishedAt"] != nil || got["checkedAt"] != nil {
		t.Fatalf("unknown timestamps not null: %v", got)
	}
	if got["enabled"] != false || got["current"] != "dev" || got["installable"] != false {
		t.Fatalf("dev build projection: %v", got)
	}
}

func TestFailedLookupIsDataNotAnError(t *testing.T) {
	fc := &fakeChecker{info: appupdate.Info{Current: "0.0.0", Enabled: true, Error: "release check: GitHub answered 503"}}
	if rec := serve(t, engine(t, fc), http.MethodGet, "/api/update"); rec.Code != http.StatusOK {
		t.Fatalf("a failed lookup answered %d, want 200 with the error field", rec.Code)
	}
}

func TestCheckRoutesToForcedLookup(t *testing.T) {
	fc := &fakeChecker{info: appupdate.Info{Current: "0.0.0", Enabled: true}}
	if rec := serve(t, engine(t, fc), http.MethodPost, "/api/update/check"); rec.Code != http.StatusOK {
		t.Fatalf("POST check status %d", rec.Code)
	}
	if fc.check.Load() != 1 || fc.status.Load() != 0 {
		t.Fatalf("POST called Status %d / Check %d", fc.status.Load(), fc.check.Load())
	}
}

func TestInstallAnswersAcceptedWithFirstState(t *testing.T) {
	fc := &fakeChecker{prog: appupdate.Progress{
		State: appupdate.StateRunning, Phase: appupdate.PhaseDownload, Version: "0.1.0", BytesTotal: 52,
	}}
	rec := serve(t, engine(t, fc), http.MethodPost, "/api/update/install")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("install status %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	got := decode(t, rec)
	if got["state"] != "running" || got["phase"] != "download" || got["version"] != "0.1.0" || got["bytesTotal"] != float64(52) {
		t.Fatalf("first state = %v", got)
	}
}

func TestInstallRefusalsCarryTheirCode(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{appupdate.ErrBusy, http.StatusConflict, "update_busy"},
		{appupdate.ErrDisabled, http.StatusConflict, "update_disabled"},
		{appupdate.ErrNoUpdate, http.StatusConflict, "update_not_available"},
		{appupdate.ErrNoRelease, http.StatusConflict, "update_unknown"},
		{errors.New("disk full"), http.StatusInternalServerError, "update_failed"},
	} {
		fc := &fakeChecker{installErr: tc.err}
		rec := serve(t, engine(t, fc), http.MethodPost, "/api/update/install")
		if rec.Code != tc.status {
			t.Fatalf("%v: status %d, want %d", tc.err, rec.Code, tc.status)
		}
		if got := decode(t, rec); got["code"] != tc.code {
			t.Fatalf("%v: code %v, want %s", tc.err, got["code"], tc.code)
		}
	}
}

func TestProgressReadsTheRunningInstall(t *testing.T) {
	fc := &fakeChecker{prog: appupdate.Progress{
		State: appupdate.StateFailed, Phase: appupdate.PhaseVerify, Version: "0.1.0",
		BytesDone: 52, BytesTotal: 52, Error: "checksum mismatch for nusallm-linux-amd64",
	}}
	rec := serve(t, engine(t, fc), http.MethodGet, "/api/update/install")
	if rec.Code != http.StatusOK {
		t.Fatalf("progress status %d", rec.Code)
	}
	got := decode(t, rec)
	if got["state"] != "failed" || got["phase"] != "verify" || got["error"] != "checksum mismatch for nusallm-linux-amd64" {
		t.Fatalf("progress = %v", got)
	}
	if fc.progress.Load() != 1 || fc.install.Load() != 0 {
		t.Fatalf("GET progress called Progress %d / Install %d", fc.progress.Load(), fc.install.Load())
	}
}

func TestNilCheckerRegistersNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r.Group("/api"), Deps{})
	for _, path := range []string{"/api/update", "/api/update/install"} {
		if rec := serve(t, r, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s with no checker = %d, want 404", path, rec.Code)
		}
	}
}
