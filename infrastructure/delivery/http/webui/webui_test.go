package webui_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"

	httpdelivery "github.com/jahrulnr/nusallm/infrastructure/delivery/http"
)

const authority = "127.0.0.1:8080"

func assets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<nusa-app></nusa-app>")},
		"app.js":           {Data: []byte("import './x.js';")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"styles/theme.css": {Data: []byte("body{}")},
		"widgets/x/x.js":   {Data: []byte("export {};")},
		"widgets/x/x.css":  {Data: []byte(":host{}")},
	}
}

func router(t *testing.T) *gin.Engine {
	t.Helper()
	fsys := assets()
	r := httpdelivery.NewHandler(httpdelivery.Deps{Authority: authority, WebAssets: fsys})
	return r
}

func request(r *gin.Engine, method, target, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestIndexServesWithSecurityHeaders(t *testing.T) {
	w := request(router(t), "GET", "/", authority)
	if w.Code != 200 {
		t.Fatalf("GET / = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type %q", ct)
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'", "script-src 'self'", "style-src 'self'",
		"connect-src 'self' ws://" + authority, "frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q in %q", want, csp)
		}
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Error("no-cache missing")
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("referrer-policy missing")
	}
}

func TestWidgetAssetsServeAndHead(t *testing.T) {
	r := router(t)
	for _, p := range []string{"/app.js", "/favicon.svg", "/styles/theme.css", "/widgets/x/x.js", "/widgets/x/x.css"} {
		w := request(r, "GET", p, authority)
		if w.Code != 200 {
			t.Errorf("GET %s = %d", p, w.Code)
		}
		w = request(r, "HEAD", p, authority)
		if w.Code != 200 || w.Body.Len() != 0 {
			t.Errorf("HEAD %s = %d len=%d", p, w.Code, w.Body.Len())
		}
	}
	if ct := request(r, "GET", "/widgets/x/x.js", authority).Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("js content-type %q", ct)
	}
}

func TestAssetRespectsHostGuard(t *testing.T) {
	w := request(router(t), "GET", "/", "evil.example")
	if w.Code != 403 {
		t.Fatalf("GET / with foreign Host = %d", w.Code)
	}
}

func TestUnknownPathsAreProblem404(t *testing.T) {
	r := router(t)
	for _, p := range []string{"/api/unknown", "/nope", "/widgets/missing.js"} {
		w := request(r, "GET", p, authority)
		if w.Code != 404 {
			t.Errorf("GET %s = %d", p, w.Code)
			continue
		}
		if !strings.Contains(w.Header().Get("Content-Type"), "problem+json") {
			t.Errorf("GET %s content-type %q — not an SPA fallback, problem expected", p, w.Header().Get("Content-Type"))
		}
	}
}

func TestNilAssetsRegistersNothing(t *testing.T) {
	r := httpdelivery.NewHandler(httpdelivery.Deps{Authority: authority})
	w := request(r, "GET", "/", authority)
	if w.Code != 404 {
		t.Fatalf("GET / without assets = %d", w.Code)
	}
}
