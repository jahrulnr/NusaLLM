package gateway

import (
	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// Pagination bounds from the contract.
const (
	defaultListLimit = 30
	maxLimit         = 100
)

type gatewayPage struct {
	Data []gatewayDTO       `json:"data"`
	Page transport.PageMeta `json:"page"`
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
	out := gatewayPage{
		Data: make([]gatewayDTO, 0, len(page)),
		Page: transport.PageMeta{Offset: offset, Limit: limit, Total: len(infos),
			HasMore: offset+len(page) < len(infos)},
	}
	for _, i := range page {
		out.Data = append(out.Data, toGateway(i))
	}
	c.JSON(200, out)
}

func (s *server) get(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	info, err := s.reads.Get(c.Request.Context(), id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, toGateway(info))
}

func (s *server) availableModels(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	models, err := s.reads.DiscoverModels(c.Request.Context(), id)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": models})
}

// providers answers the picker's route layer: the upstream backends the
// gateway can serve the named model through. ?model= is required — the
// endpoint set is per-model.
func (s *server) providers(c *gin.Context) {
	id, ok := transport.PathID(c)
	if !ok {
		return
	}
	modelID := c.Query("model")
	if modelID == "" {
		transport.WriteProblem(c, 400, "invalid_argument", "query parameter model is required")
		return
	}
	providers, err := s.reads.DiscoverProviders(c.Request.Context(), id, modelID)
	if err != nil {
		transport.RespondError(c, err)
		return
	}
	type providerDTO struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	out := make([]providerDTO, 0, len(providers))
	for _, p := range providers {
		out = append(out, providerDTO{Name: p.Name, Slug: p.Slug})
	}
	c.JSON(200, gin.H{"data": out})
}
