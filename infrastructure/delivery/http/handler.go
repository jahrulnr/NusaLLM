// Package http is the WebUI's HTTP delivery composition root — a
// loopback-only Gin router that mounts the per-boundary packages under
// this tree (conversation, gateway, setting, tool, system, webui)
// behind the shared Host/Origin security envelope. The HTTP contract
// and the WebSocket turn channel are live; the embedded WebUI assets
// mount here too so browser, API, and socket share one origin.
//
// The transport serializes projections, never domain entities: key
// material, raw key refs, provider replay artifacts, attachment sha256
// pins, and error causes stay server-side. Normalized message parts,
// tool I/O, and attachment uri/name are owner-visible content.
package http

import (
	"context"
	"io/fs"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/conversation"
	fsboundary "github.com/jahrulnr/nusallm/infrastructure/delivery/http/fs"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/setting"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/system"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/tool"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/webui"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// Deps wires the boundary use cases plus the accepted request authorities
// (host:port) the security model validates against. Reader and writer
// fields are optional: a nil surface leaves its routes unregistered.
type Deps struct {
	Conversations      conversation.Reader
	ConversationWrites conversation.Writer
	Gateways           gateway.Reader
	GatewayWrites      gateway.Writer
	Settings           setting.Reader
	SettingsWriter     setting.Writer
	Tools              tool.Catalog
	FileTree           drepo.FileTree
	// Jobs is the server-lifecycle context for accepted background work
	// (delete, compact) — decoupled from any single request so a client
	// disconnect can't cancel accepted work; nil means Background.
	Jobs context.Context
	// LiveTurns is the hub's in-progress transport turn, projected onto
	// conversation detail. Nil omits the field.
	LiveTurns conversation.LiveTurn
	// Authorities are configured host:port pairs accepted in Host and
	// Origin headers. The listener address remains a separate loopback bind.
	Authorities []string
	// WS upgrades GET /api/ws into the conversation hub socket.
	WS gin.HandlerFunc
	// WebAssets is the embedded WebUI filesystem served on this origin;
	// nil keeps the binary API-only.
	WebAssets fs.FS
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
	r.Use(recoverProblem(), guard(d.Authorities))

	api := r.Group("/api")
	system.Register(api)
	conversation.Register(api, conversation.Deps{
		Reads:     d.Conversations,
		Writes:    d.ConversationWrites,
		Jobs:      d.Jobs,
		LiveTurns: d.LiveTurns,
	})
	gateway.Register(api, gateway.Deps{Reads: d.Gateways, Writes: d.GatewayWrites})
	setting.Register(api, setting.Deps{Reads: d.Settings, Writes: d.SettingsWriter})
	tool.Register(api, tool.Deps{Tools: d.Tools})
	if d.FileTree != nil {
		fsboundary.Register(api, d.FileTree)
	}
	if d.WS != nil {
		api.GET("/ws", d.WS)
	}
	webui.Register(r, d.WebAssets)

	r.NoRoute(notFound)
	return r
}

func notFound(c *gin.Context) {
	transport.WriteProblem(c, 404, "not_found", "no such route")
}
