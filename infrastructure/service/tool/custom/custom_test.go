package custom

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	toolexec "github.com/jahrulnr/nusallm/infrastructure/service/tool/terminal"
)

func TestCustomPathsDescribeTheActualTerminalEnvironment(t *testing.T) {
	env := toolexec.LocalEnvironment{Home: t.TempDir(), Storage: t.TempDir()}
	tool, err := NewTool(env)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Schema().CallableByAgent() || !tool.Schema().CallableBySystem() {
		t.Fatal("custom paths must be system-only hydration")
	}
	workspace := t.TempDir()
	res, err := tool.Execute(dservice.WithWorkspaceDir(t.Context(), workspace), entity.ToolCall{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Paths  []string `json:"paths"`
		Python string   `json:"python_command"`
	}
	if err := json.Unmarshal([]byte(res.Text), &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Paths, env.ToolPaths(workspace)) || payload.Paths[0] != filepath.Join(workspace, ".agents/tools/bin") || payload.Python != "python3" {
		t.Fatalf("metadata=%+v", payload)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Execute(ctx, entity.ToolCall{}, nil); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := NewTool(nil); err == nil {
		t.Fatal("nil environment accepted")
	}
}
