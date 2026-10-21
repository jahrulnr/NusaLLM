package workspace

import (
	"context"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// The schema must sit outside the agent's reach: the recorded pin and the
// provider's tool list are decided by the same permission flags.
func TestWorkspaceToolSchemaIsSystemOnly(t *testing.T) {
	tool, err := NewWorkspaceTool()
	if err != nil {
		t.Fatal(err)
	}
	s := tool.Schema()
	if s.Name() != "workspace" {
		t.Fatalf("schema name = %q, want workspace", s.Name())
	}
	if s.Permission() != enum.ToolPermissionSystem {
		t.Fatalf("permission = %v, want system", s.Permission())
	}
	if s.CallableByAgent() {
		t.Fatal("workspace must not be agent-callable")
	}
	if !s.CallableBySystem() {
		t.Fatal("workspace must be system-callable")
	}
}

// Execute reports the turn-scoped binding — identical to the pin's
// recorded output so history and a live call agree.
func TestWorkspaceToolExecuteReportsBoundDir(t *testing.T) {
	tool, err := NewWorkspaceTool()
	if err != nil {
		t.Fatal(err)
	}
	ctx := dservice.WithWorkspaceDir(context.Background(), "/tmp/proj")
	res, err := tool.Execute(ctx, entity.ToolCall{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "workspace changed to /tmp/proj" {
		t.Fatalf("output = %q", res.Text)
	}
	res, err = tool.Execute(context.Background(), entity.ToolCall{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "workspace changed to (unset)" {
		t.Fatalf("unbound output = %q", res.Text)
	}
}
