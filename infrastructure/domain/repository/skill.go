package repository

import "context"

// Skill is the discovery projection of a SKILL.md frontmatter and its location.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Source      string `json:"source"`
}

type SkillCatalog struct {
	Skills      []Skill  `json:"skills"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// Skills resolves the effective catalog for one workspace: workspace entries
// override global entries, which override builtins of the same declared name.
// Read returns complete source, including frontmatter; supporting paths resolve
// from Path. Missing roots are empty; malformed entries remain diagnostics.
type Skills interface {
	List(ctx context.Context, workspace string) (SkillCatalog, error)
	Read(ctx context.Context, workspace, name string) (Skill, string, error)
}
