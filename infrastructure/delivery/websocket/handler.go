// Package websocket is the HTTP upgrade boundary for the WebUI socket:
// it validates the request exactly once more at this edge (Host is
// already guarded globally; Origin is NOT — GET rides exempt from the
// unsafe-method Origin check, so this handler enforces it explicitly
// both before upgrade and inside Gorilla's CheckOrigin), upgrades, then
// hands the socket to the conversation hub. No command or turn logic
// lives here.
package websocket

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	dtransport "github.com/jahrulnr/nusallm/infrastructure/delivery/transport"
	wsconversation "github.com/jahrulnr/nusallm/infrastructure/delivery/websocket/conversation"
)

// Handler adapts GET /api/ws onto the conversation hub.
type Handler struct {
	hub         *wsconversation.Hub
	authorities []string
	upgrader    websocket.Upgrader
}

// NewHandler wires the upgrade adapter: Origin must match one configured
// authority — checked twice deliberately: once here so a rejection renders
// the contract's problem+json, and once inside Gorilla's own CheckOrigin so
// the upgrader fails closed on its own too.
func NewHandler(hub *wsconversation.Hub, authorities []string) *Handler {
	h := &Handler{hub: hub, authorities: append([]string(nil), authorities...)}
	h.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return h.originAllowed(r.Header.Get("Origin"))
		},
	}
	return h
}

// Serve is the gin handler for GET /api/ws.
func (h *Handler) Serve(c *gin.Context) {
	if !h.originAllowed(c.Request.Header.Get("Origin")) {
		p := dtransport.NewProblem(http.StatusForbidden, "forbidden_origin",
			"websocket upgrade requires an exact Origin")
		c.Header("Content-Type", "application/problem+json")
		c.Abort()
		c.JSON(http.StatusForbidden, p)
		return
	}
	// Socket admission is bounded — reject before the upgrade so the
	// client gets a real 503 problem, not a hanging handshake. Attach
	// rechecks under the hub lock for the race window.
	if h.hub.SocketFull() {
		p := dtransport.NewProblem(http.StatusServiceUnavailable, "unavailable",
			"too many open sockets")
		c.Header("Content-Type", "application/problem+json")
		c.Abort()
		c.JSON(http.StatusServiceUnavailable, p)
		return
	}
	ws, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Gorilla already wrote the rejection response.
		return
	}
	h.hub.Attach(ws)
}

func (h *Handler) originAllowed(origin string) bool {
	origin = strings.ToLower(origin)
	for _, authority := range h.authorities {
		if origin == strings.ToLower("http://"+authority) {
			return true
		}
	}
	return false
}
