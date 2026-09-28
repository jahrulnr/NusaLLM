package conversation

import (
	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

func (s *server) get(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	info, err := s.reads.Info(c.Request.Context(), id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	available, err := s.reads.CanCompact(c.Request.Context(), id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	out := toConversation(info)
	out.CanCompact = &available
	c.JSON(200, out)
}

// messages resolves ?start against the current history length —
// negative counts back from the end — then reads the absolute window
// through the broker. History may move between the two reads; the
// response carries the resolved indexes so the client stays honest
// rather than pretending a snapshot.
func (s *server) messages(c *gin.Context) {
	start, limit, ok := transport.ParseWindow(c, defaultWindowStart, defaultWindowLength, maxLimit)
	if !ok {
		return
	}
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	info, err := s.reads.Info(ctx, id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	total := info.MessageCount
	effective := start
	if effective < 0 {
		effective += total
	}
	if effective < 0 {
		effective = 0
	}
	if effective > total {
		effective = total
	}
	end := effective + limit
	if end > total {
		end = total
	}
	msgs, err := s.reads.History(ctx, id, effective, end)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	out := messagePage{
		EffectiveStart: effective,
		Total:          total,
		HasMore:        effective+len(msgs) < total,
		Messages:       make([]messageDTO, 0, len(msgs)),
	}
	if out.HasMore {
		next := effective + len(msgs)
		out.NextStart = &next
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, toMessage(m))
	}
	c.JSON(200, out)
}
