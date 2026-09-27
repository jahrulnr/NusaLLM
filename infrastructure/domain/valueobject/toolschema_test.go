package valueobject

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func TestNewToolSchema(t *testing.T) {
	if _, err := NewToolSchema("", "d", nil, true, enum.ToolPermissionGeneral); err == nil {
		t.Fatal("expected error for empty name")
	}
	s, err := NewToolSchema("search", "d", nil, true, enum.ToolPermissionGeneral)
	if err != nil {
		t.Fatalf("NewToolSchema: %v", err)
	}
	if s.Name() != "search" || !s.Enabled() {
		t.Fatalf("unexpected schema: %+v", s)
	}
}

// CallableBy* must fail closed for disabled schemas and unknown permissions.
func TestToolSchemaCallable(t *testing.T) {
	cases := []struct {
		name       string
		enabled    bool
		permission enum.ToolPermission
		agent      bool
		system     bool
	}{
		{"disabled general", false, enum.ToolPermissionGeneral, false, false},
		{"system", true, enum.ToolPermissionSystem, false, true},
		{"agent", true, enum.ToolPermissionAgent, true, false},
		{"general", true, enum.ToolPermissionGeneral, true, true},
		{"unknown permission", true, enum.ToolPermission("bogus"), false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := NewToolSchema("t", "", nil, c.enabled, c.permission)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.CallableByAgent(); got != c.agent {
				t.Fatalf("CallableByAgent = %v, want %v", got, c.agent)
			}
			if got := s.CallableBySystem(); got != c.system {
				t.Fatalf("CallableBySystem = %v, want %v", got, c.system)
			}
		})
	}
}
