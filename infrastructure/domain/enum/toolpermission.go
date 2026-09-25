package enum

// ToolPermission controls which actor may invoke a ToolSchema.
type ToolPermission string

const (
	// ToolPermissionSystem marks a schema usable only by the application;
	// it must not be offered to the agent.
	ToolPermissionSystem ToolPermission = "system"
	// ToolPermissionAgent marks a schema usable only by the agent.
	ToolPermissionAgent ToolPermission = "agent"
	// ToolPermissionGeneral marks a schema usable by both.
	ToolPermissionGeneral ToolPermission = "general"
)

func (p ToolPermission) String() string {
	return string(p)
}

func ParseToolPermission(v string) (ToolPermission, bool) {
	switch p := ToolPermission(v); p {
	case ToolPermissionSystem, ToolPermissionAgent, ToolPermissionGeneral:
		return p, true
	}
	return "", false
}
