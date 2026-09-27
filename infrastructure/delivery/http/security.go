package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// guard is the contract's security envelope: the server is loopback-only
// with no auth, so Host and Origin checks are what keep other websites
// from driving the API through a victim's browser. Host must equal the
// canonical listen authority on every request (blocks DNS-rebinding);
// unsafe methods additionally require an Origin exactly equal to the
// server's own origin — absent is rejected, non-browser callers must
// send it. Safe methods need no Origin: the server sends no CORS
// allow-headers, so a cross-origin page could not read responses anyway.
func guard(authority string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Host != authority {
			transport.WriteProblem(c, http.StatusForbidden, "forbidden_origin",
				"Host header does not match the listen address")
			return
		}
		switch c.Request.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if c.GetHeader("Origin") != "http://"+authority {
				transport.WriteProblem(c, http.StatusForbidden, "forbidden_origin",
					"unsafe methods require Origin: http://"+authority)
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
