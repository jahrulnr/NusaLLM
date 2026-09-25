package enum

// Role identifies the author of a conversation message: system, user, developer, or assistant.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleDeveloper Role = "developer"
	RoleAssistant Role = "assistant"
)

func (r Role) String() string {
	return string(r)
}

func ParseRole(v string) (Role, bool) {
	switch r := Role(v); r {
	case RoleSystem, RoleUser, RoleDeveloper, RoleAssistant:
		return r, true
	}
	return "", false
}
