package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// guard is the contract's security envelope: the server is loopback-only
// with no auth, so Host and Origin checks are what keep other websites
// from driving the API through a victim's browser. Host must match a
// configured authority on every request; unsafe methods also require an
// Origin matching that authority. Safe methods need no Origin: the server
// sends no CORS allow-headers, so a cross-origin page could not read
// responses anyway.
func guard(authorities []string) gin.HandlerFunc {
	allowedHosts := make(map[string]struct{}, len(authorities))
	allowedOrigins := make(map[string]struct{}, len(authorities))
	for _, authority := range authorities {
		allowedHosts[strings.ToLower(authority)] = struct{}{}
		allowedOrigins[strings.ToLower("http://"+authority)] = struct{}{}
	}
	return func(c *gin.Context) {
		if _, ok := allowedHosts[strings.ToLower(c.Request.Host)]; !ok {
			transport.WriteProblem(c, http.StatusForbidden, "forbidden_origin",
				"Host header does not match a configured server host")
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if _, ok := allowedOrigins[strings.ToLower(c.GetHeader("Origin"))]; !ok {
				transport.WriteProblem(c, http.StatusForbidden, "forbidden_origin",
					"unsafe methods require an Origin matching a configured server host")
				return
			}
		}
		c.Next()
	}
}

// recoverProblem renders a panic as a contract-shaped 500 instead of
// gin's plain-text recovery — every error response is problem+json.
func recoverProblem() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				transport.WriteProblem(c, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		c.Next()
	}
}
