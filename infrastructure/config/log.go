package config

// LogConfig bounds the provider diagnostic log — payloads carry full
// request/response bodies including conversation content, so recording
// them is an explicit opt-in, and rotation keeps the store bounded.
type LogConfig struct {
	// Bodies records raw request/response/stream payloads. Off keeps only
	// the event kinds and counts — enough to debug sequencing without
	// persisting conversation content.
	Bodies bool `json:"bodies" default:"false"`
	// MaxLines rotates active.jsonl once it holds this many records.
	MaxLines int `json:"max_lines" default:"2000"`
	// MaxFiles caps sealed segments — oldest are pruned past the cap.
	MaxFiles int `json:"max_files" default:"8"`
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
}
