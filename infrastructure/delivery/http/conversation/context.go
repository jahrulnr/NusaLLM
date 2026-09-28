package conversation

import (
	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// context answers the composer footer's "~used/total Tokens" line. The
// conversation is optional — a fresh, never-persisted room still gets
// the model's capacity half when route+modelId name a known model.
func (s *server) context(c *gin.Context) {
	stats, err := s.reads.ContextFooterStats(
		c.Request.Context(),
		c.Query("conversationId"),
		c.Query("route"),
		c.Query("modelId"),
	)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, gin.H{
		"estimate": stats.Estimate,
		"total":    stats.Total,
	})
}
