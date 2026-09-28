// Package webui is the WebUI static-asset boundary: it serves the
// embedded frontend on the same loopback origin as the API so the
// browser shares one Host/Origin envelope with HTTP and the socket.
// It owns only file delivery and response headers — no conversation,
// gateway, or settings behavior lives here.
package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// Register mounts the exact asset surfaces onto the engine. A nil
// assets FS registers nothing — the API-only composition stays valid.
// There is deliberately no SPA fallback: unknown paths 404.
func Register(r *gin.Engine, assets fs.FS, authorities []string) {
	if assets == nil {
		return
	}
	serve := func(c *gin.Context) {
		headers(c, authorities)
		name := strings.TrimPrefix(path.Clean("/"+c.Request.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		f, err := assets.Open(name)
		if err != nil {
			transport.WriteProblem(c, http.StatusNotFound, "not_found", "no such asset")
			return
		}
		st, _ := f.Stat()
		_ = f.Close()
		if st == nil || st.IsDir() {
			transport.WriteProblem(c, http.StatusNotFound, "not_found", "no such asset")
			return
		}
		http.FileServer(http.FS(assets)).ServeHTTP(c.Writer, c.Request)
	}
	for _, p := range []string{"/", "/app.js", "/favicon.svg"} {
		r.GET(p, serve)
		r.HEAD(p, serve)
	}
	for _, p := range []string{"/styles/*path", "/lib/*path", "/widgets/*path", "/vendor/*path"} {
		r.GET(p, serve)
		r.HEAD(p, serve)
	}
}

// headers pins the asset responses to this origin: no caching staleness
// for a local tool, no content-type sniffing, no referrer leakage, and
// a CSP that admits only same-origin scripts/styles plus the WS dial.
func headers(c *gin.Context, authorities []string) {
	h := c.Writer.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	connectSrc := []string{"'self'"}
	for _, authority := range authorities {
		connectSrc = append(connectSrc, "ws://"+authority)
	}
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; "+
		"style-src 'self'; img-src 'self' data:; connect-src "+strings.Join(connectSrc, " ")+"; "+
		"object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
}
