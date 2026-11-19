package aggregate

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// Gateway is the aggregate root for a configured endpoint surface: identity,
// wire style, endpoint, credential, and the catalog of models it can serve.
// A gateway may translate paradigms internally — e.g. OpenRouter accepts an
// OpenAI-style request and routes it to a messages-style backend — so the
// same model id can be spoken to through different styles depending on the
// gateway chosen. The declared style is the catalog's default; a Model may
// pin its own style for mixed-protocol endpoints (see Model.WireStyle).
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
	// The endpoint is the base every provider call is built from. A
	// relative or non-http(s) value cannot produce a request — accept it
	// here and every later send fails as an opaque network error.
	u, err := url.Parse(g.endpoint)
	if err != nil || !u.IsAbs() || u.Hostname() == "" ||
		(!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return fmt.Errorf("endpoint %q must be an absolute http(s) URL", g.endpoint)
	}
	// Userinfo is secret material sitting in the base URL. The endpoint
	// is stored and projected to the browser and to logs; credentials
	// belong in ApiKey, which is the field that stays out of that path.
	if u.User != nil {
		return fmt.Errorf("endpoint %q must not carry user info", g.endpoint)
	}
	// Adapters append their own path to this base — a stored query or
	// fragment lands mid-URL on every send ("...?api-version=x/chat/
	// completions"), so a save that looks valid is broken everywhere.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("endpoint %q must not carry a query or fragment", g.endpoint)
	}
	// url.Parse accepts a port outside the TCP range, and Port() returns
	// "" both when the URL has no port and when a colon introduces an
	// empty one (`https://host:`). The empty form is not "no port" — it
	// cannot be dialed — so the colon itself is what decides.
	if port, ok := endpointPort(u.Host); ok {
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return fmt.Errorf("endpoint %q port must be between 1 and 65535", g.endpoint)
		}
	}
	if _, ok := enum.ParseStyle(g.style.String()); !ok {
		return fmt.Errorf("style %q is invalid", g.style)
	}
	return nil
}

// endpointPort reports a port the host explicitly introduced with a
// colon. An IPv6 literal keeps its colons inside brackets, so only the
// suffix after ']' counts. The returned port may be empty — that is the
// trailing-colon form, which is present and invalid.
func endpointPort(host string) (string, bool) {
	if strings.HasPrefix(host, "[") {
		end := strings.LastIndex(host, "]")
		if end < 0 {
			return "", false
		}
		rest := host[end+1:]
		if rest == "" || rest[0] != ':' {
			return "", false
		}
		return rest[1:], true
	}
	i := strings.LastIndex(host, ":")
	if i < 0 {
		return "", false
	}
	return host[i+1:], true
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
