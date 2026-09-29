package config

// LogConfig bounds the provider diagnostic log.
type LogConfig struct {
	// MaxLines rotates active.jsonl once it holds this many records.
	MaxLines int `json:"max_lines" default:"2000"`
	// MaxFiles caps sealed segments — oldest are pruned past the cap.
	MaxFiles int `json:"max_files" default:"9"`
}

// Normalize clamps invalid values back to unconfigured, then fills zeros
// from the `default` tags — a negative cap is a typo, not "unbounded".
func (c *LogConfig) Normalize() {
	if c.MaxLines < 0 {
		c.MaxLines = 0
	}
	if c.MaxFiles < 0 {
		c.MaxFiles = 0
	}
	ApplyDefaults(c)
	if c.MaxLines > 2000 {
		c.MaxLines = 2000
	}
	if c.MaxFiles > 9 {
		c.MaxFiles = 9
	}
}
