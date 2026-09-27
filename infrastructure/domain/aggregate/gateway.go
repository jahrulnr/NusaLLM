package aggregate

import (
	"errors"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Gateway is the aggregate root for a configured endpoint surface: identity,
// wire style, endpoint, credential, and the catalog of models it can serve.
// A gateway may translate paradigms internally — e.g. OpenRouter accepts an
// OpenAI-style request and routes it to a messages-style backend — so the
// same model id can be spoken to through different styles depending on the
// gateway chosen.
type Gateway struct {
	id       string
	name     string // optional
	style    enum.Style
	endpoint string
	apiKey   string // optional secret material
	models   []entity.Model
	stream   bool
}

func NewGateway(id, name, endpoint string, style enum.Style) (Gateway, error) {
	g := Gateway{id: id, name: name, endpoint: endpoint, style: style, stream: true}
	return g, g.validate()
}

func (g Gateway) validate() error {
	if g.id == "" { // gateways are configured, named things — never minted
		return errors.New("id is required")
	}
	if _, err := valueobject.NewID("gw", g.id); err != nil {
		return err
	}
	if g.endpoint == "" {
		return errors.New("endpoint is required")
	}
	if g.style == "" {
		return errors.New("style is required")
	}
	return nil
}

func (g *Gateway) SetApiKey(key string) {
	g.apiKey = key
}

// SetModels replaces the catalog of models this gateway can serve.
func (g *Gateway) SetModels(models []entity.Model) {
	g.models = models
}

func (g *Gateway) EnableStream()  { g.stream = true }
func (g *Gateway) DisableStream() { g.stream = false }

func (g Gateway) ID() string             { return g.id }
func (g Gateway) Name() string           { return g.name }
func (g Gateway) Style() enum.Style      { return g.style }
func (g Gateway) Endpoint() string       { return g.endpoint }
func (g Gateway) ApiKey() string         { return g.apiKey }
func (g Gateway) Models() []entity.Model { return g.models }

// Model returns the catalog entry matching id — the resume path resolves
// Conversation.ModelID() through it.
func (g Gateway) Model(id string) (entity.Model, bool) {
	for _, m := range g.models {
		if m.ID() == id {
			return m, true
		}
	}
	return entity.Model{}, false
}
func (g Gateway) StreamMode() bool { return g.stream }
