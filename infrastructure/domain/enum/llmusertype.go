package enum

// LLMUserType marks the provenance of a user-role message: origin is real
// end-user input, system is a system-generated payload carried by a user
// message (e.g. compaction output).
type LLMUserType string

const (
	// origin for real user message
	LLMUserTypeOrigin LLMUserType = "origin"
	// system for system message (example: compaction need placed at user message)
	LLMUserTypeSystem LLMUserType = "system"
)

func (t LLMUserType) String() string {
	return string(t)
}

func ParseLLMUserType(v string) (LLMUserType, bool) {
	switch t := LLMUserType(v); t {
	case LLMUserTypeOrigin, LLMUserTypeSystem:
		return t, true
	}
	return "", false
}
