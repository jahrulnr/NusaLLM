// Package http is the WebUI's HTTP delivery composition root — a
// loopback-only Gin router that mounts the per-boundary packages under
// this tree (conversation, gateway, setting, tool, system) behind the
// shared Host/Origin security envelope. Phases 1-2 of
// docs/api/contract.md are live; the WebSocket arrives in a later phase.
//
// The transport serializes projections, never domain entities: key
// material, raw key refs, optionalContent, tool I/O, attachment
// locations, and error causes stay server-side.
package http

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/setting"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/system"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/tool"
)

// Deps wires the boundary use cases plus the canonical listen authority
// (host:port) the security model validates against. Writer fields are
// optional: a nil writer leaves its routes unregistered.
type Deps struct {
	Conversations      conversation.Reader
	ConversationWrites conversation.Writer
	Gateways           gateway.Reader
	GatewayWrites      gateway.Writer
	Settings           setting.Reader
	SettingsWriter     setting.Writer
	Tools              tool.Catalog
	// Jobs is the server-lifecycle context for accepted background work
	// (delete, compact) — decoupled from any single request so a client
	// disconnect can't cancel accepted work; nil means Background.
	Jobs context.Context
	// Authority is the exact Host clients must present — e.g.
	// "127.0.0.1:8080". It comes from the bound listener, not from any
	// configuration the API can change.
	Authority string
	// WS upgrades GET /api/ws — phase 3 wires the hub handler here.
	WS gin.HandlerFunc
}

// NewHandler builds the contract router. All responses beyond the
// envelope are JSON projections; errors render as RFC 9457 problem+json.
// Callers that care about gin's mode set it at their own entry point —
// a constructor must not mutate process-global state.
func NewHandler(d Deps) *gin.Engine {
	r := gin.New()
	// No proxy trust: the listener is loopback and RemoteIP is unused.
	_ = r.SetTrustedProxies(nil)
	// Redirects would answer an unmatched path before the guard runs —
	// send them through NoRoute instead, which the guard still covers.
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.Use(recoverProblem(), guard(d.Authority))

	api := r.Group("/api")
	system.Register(api)
	conversation.Register(api, conversation.Deps{
		Reads:  d.Conversations,
		Writes: d.ConversationWrites,
		Jobs:   d.Jobs,
	})
	gateway.Register(api, gateway.Deps{Reads: d.Gateways, Writes: d.GatewayWrites})
	setting.Register(api, setting.Deps{Reads: d.Settings, Writes: d.SettingsWriter})
	tool.Register(api, tool.Deps{Tools: d.Tools})
	if d.WS != nil {
		api.GET("/ws", d.WS)
	}

	r.NoRoute(notFound)
	return r
}

func notFound(c *gin.Context) {
	transport.WriteProblem(c, 404, "not_found", "no such route")
}
