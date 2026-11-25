package conversation

import (
	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// Pagination bounds from the contract: list pages default to 30 (50 for
// history windows), capped at 100.
const (
	defaultListLimit    = 30
	defaultWindowStart  = -50
	defaultWindowLength = 50
	maxLimit            = 100
)

type conversationPage struct {
	Data []conversationDTO  `json:"data"`
	Page transport.PageMeta `json:"page"`
}

type messagePage struct {
	EffectiveStart int          `json:"effectiveStart"`
	NextStart      *int         `json:"nextStart"`
	Total          int          `json:"total"`
	HasMore        bool         `json:"hasMore"`
	Messages       []messageDTO `json:"messages"`
}

func (s *server) list(c *gin.Context) {
	offset, limit, ok := transport.ParsePageParams(c, defaultListLimit, maxLimit)
	if !ok {
		return
	}
	infos, err := s.reads.List(c.Request.Context())
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	page := transport.SlicePage(infos, offset, limit)
	out := conversationPage{
		Data: make([]conversationDTO, 0, len(page)),
		Page: transport.PageMeta{Offset: offset, Limit: limit, Total: len(infos),
			HasMore: offset+len(page) < len(infos)},
	}
	for _, i := range page {
		// The same projection as the detail read: a sidebar can mark an
		// active room and offer compaction without per-room detail reads.
		dto, err := s.project(c.Request.Context(), i)
		if err != nil {
			transport.RespondError(c, err)
			return
		}
		out.Data = append(out.Data, dto)
	}
	c.JSON(200, out)
}
