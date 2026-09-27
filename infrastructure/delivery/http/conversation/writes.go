package conversation

import (
	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

func (s *server) rename(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	var body struct {
		Title *string `json:"title"`
	}
	if !transport.DecodeJSON(c, &body) {
		return
	}
	if body.Title == nil {
		transport.WriteProblem(c, 400, "invalid_argument", "title is required")
		return
	}
	ctx := c.Request.Context()
	if err := s.writes.Rename(ctx, id, *body.Title); err != nil {
		transport.RespondError(c, err)
		return
	}
	info, err := s.reads.Info(ctx, id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, toConversation(info))
}

func (s *server) delete(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	// Server-lifecycle context: an abandoned request must not cancel an
	// accepted delete; server shutdown cancels it instead.
	if err := s.writes.Delete(s.jobs, id); err != nil {
		transport.RespondError(c, err)
		return
	}
	c.Status(204)
}

func (s *server) compact(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	if err := s.writes.Compact(s.jobs, id); err != nil {
		transport.RespondError(c, err)
		return
	}
	c.Status(204)
}
