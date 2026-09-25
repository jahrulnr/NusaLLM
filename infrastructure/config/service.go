package config

// ServiceConfig tunes the model-service transport — the HTTP deadlines the
// wire adapters apply when no client overrides them.
type ServiceConfig struct {
	// HeaderTimeout bounds the wait for a response's first byte.
	HeaderTimeout Duration `json:"header_timeout" default:"30s"`
	// IdleTimeout is the per-chunk deadline on a streaming body — slow
	// reasoning models are fine, dead connections are not.
	IdleTimeout Duration `json:"idle_timeout" default:"90s"`
}

// Normalize clamps invalid values back to unconfigured, then fills zeros
// from the `default` tags — a negative deadline would fire instantly, so
// it is treated as unconfigured, not "fire now".
func (c *ServiceConfig) Normalize() {
	if c.HeaderTimeout < 0 {
		c.HeaderTimeout = 0
	}
	if c.IdleTimeout < 0 {
		c.IdleTimeout = 0
	}
	ApplyDefaults(c)
}
