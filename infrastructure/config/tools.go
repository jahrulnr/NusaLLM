package config

// ToolsConfig adds executable directories to the agent's terminal PATH.
// Relative entries resolve against the active workspace, not the server cwd.
type ToolsConfig struct {
	Paths []string `json:"paths"`
}

func (c *ToolsConfig) Normalize() {
	if c.Paths == nil {
		c.Paths = []string{}
	}
}
