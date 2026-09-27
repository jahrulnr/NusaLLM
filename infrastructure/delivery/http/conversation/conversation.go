// Package conversation is the WebUI conversation boundary — the HTTP
// surface over the appconversation use cases: list/detail/history reads,
// rename/delete/compact mutations, and the message DTO projections that
// keep tool I/O, attachment locations, and optionalContent off the wire.
package conversation

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

// Reader is the read surface this boundary needs from the conversation
// broker — projections and ranged history.
// *appconversation.ConversationApp satisfies it.
type Reader interface {
	Info(ctx context.Context, id string) (drepo.ConversationInfo, error)
	List(ctx context.Context) ([]drepo.ConversationInfo, error)
	History(ctx context.Context, id string, start, end int) ([]entity.Message, error)
}

// Writer is the mutation surface: Rename is synchronous; Delete and
// Compact are accepted jobs that must outlive the HTTP request — they
// run under Deps.Jobs, the server-lifecycle context, so an abandoned
// request never half-applies them and server shutdown still cancels them.
// *appconversation.ConversationApp satisfies it.
type Writer interface {
	Rename(ctx context.Context, id, title string) error
	Delete(ctx context.Context, id string) error
	Compact(ctx context.Context, id string) error
}

// Deps wires the use cases; Writes is optional — nil leaves the mutation
// routes unregistered.
type Deps struct {
	Reads  Reader
	Writes Writer
	// Jobs is the server-lifecycle context for accepted background work
	// (delete, compact) — decoupled from any single request so a client
	// disconnect can't cancel accepted work; nil means Background.
	Jobs context.Context
}

// Register mounts this boundary's routes under /api. The router root
// calls it; this package never imports the root.
func Register(api *gin.RouterGroup, d Deps) {
	jobs := d.Jobs
	if jobs == nil {
		jobs = context.Background()
	}
	s := &server{reads: d.Reads, writes: d.Writes, jobs: jobs}
	api.GET("/conversations", s.list)
	api.GET("/conversations/:id", s.get)
	api.GET("/conversations/:id/messages", s.messages)
	if d.Writes != nil {
		api.PATCH("/conversations/:id", s.rename)
		api.DELETE("/conversations/:id", s.delete)
		api.POST("/conversations/:id/compactions", s.compact)
	}
}

type server struct {
	reads  Reader
	writes Writer
	jobs   context.Context
}
