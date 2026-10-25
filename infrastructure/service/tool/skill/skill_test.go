package skill

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	reposkill "github.com/jahrulnr/nusallm/infrastructure/repository/skill"
)

func TestBuiltinSupportFilesEmbedAndInstall(t *testing.T) {
	paths := []string{
		"skill-creator/SKILL.md",
		"skill-creator/agents/openai.yaml",
		"skill-creator/assets/skill-creator.png",
		"skill-creator/references/openai_yaml.md",
		"skill-creator/scripts/init_skill.py",
		"skill-creator/scripts/generate_openai_yaml.py",
		"skill-creator/scripts/quick_validate.py",
		"skill-creator/scripts/vendor/PyYAML-LICENSE",
		"skill-creator/scripts/vendor/yaml/__init__.py",
		"skill-creator/scripts/vendor/yaml/parser.py",
		"tool-creator/SKILL.md",
		"tool-creator/scripts/init_tool.py",
		"tool-creator/scripts/validate_tool.py",
		"tool-creator/templates/main.py.tmpl",
	}
	builtin := Builtins()
	installed := t.TempDir()
	if err := reposkill.InstallBuiltins(installed, builtin); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		want, err := fs.ReadFile(builtin, path)
		if err != nil {
			t.Fatalf("embedded %s: %v", path, err)
		}
		got, err := os.ReadFile(filepath.Join(installed, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("installed %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("installed %s differs from embedded source", path)
		}
	}
	if err := fs.WalkDir(builtin, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case strings.Contains(path, "__pycache__"), strings.HasSuffix(path, ".pyc"):
			t.Errorf("Python bytecode must not be embedded: %s", path)
		case strings.HasPrefix(path, "skill-creator/scripts/vendor/yaml/") &&
			(strings.HasSuffix(path, ".so") || strings.HasSuffix(path, ".pyd")):
			t.Errorf("native PyYAML extension must not be embedded: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinCatalogAndReadThroughTools(t *testing.T) {
	builtin := t.TempDir()
	if err := reposkill.InstallBuiltins(builtin, Builtins()); err != nil {
		t.Fatal(err)
	}
	store := reposkill.NewLocal(t.TempDir(), builtin)
	catalogTool, err := NewSkillsTool(store)
	if err != nil {
		t.Fatal(err)
	}
	if !catalogTool.Schema().CallableByAgent() || !catalogTool.Schema().CallableBySystem() {
		t.Fatal("catalog must be callable by both the agent and hydration")
	}
	result, err := catalogTool.Execute(t.Context(), entity.ToolCall{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var catalog drepo.SkillCatalog
	if err := json.Unmarshal([]byte(result.Text), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 2 || len(catalog.Diagnostics) != 0 {
		t.Fatalf("builtins = %+v", catalog)
	}
	readTool, err := NewReadSkillTool(store)
	if err != nil {
		t.Fatal(err)
	}
	if !readTool.Schema().CallableByAgent() || readTool.Schema().CallableBySystem() {
		t.Fatal("read_skill must be agent-only")
	}
	for _, skill := range catalog.Skills {
		call, _ := entity.NewToolCall("read_1", "read_skill", `{"name":"`+skill.Name+`"}`, enum.TooltypeOrigin)
		result, err := readTool.Execute(t.Context(), call, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(skill.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(result.Text, string(raw)) || !strings.Contains(result.Text, filepath.Dir(skill.Path)) {
			t.Fatalf("source incomplete: %q", result.Text)
		}
	}
}

func TestReadSkillBindsWorkspaceAndValidatesInput(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	root := filepath.Join(workspace, ".agents", "skills", "local")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: local\ndescription: Local only\n---\nworkspace body"), 0644); err != nil {
		t.Fatal(err)
	}
	tool, _ := NewReadSkillTool(reposkill.NewLocal(home, ""))
	ctx := dservice.WithWorkspaceDir(t.Context(), workspace)
	call, _ := entity.NewToolCall("read_1", "read_skill", `{"name":"local"}`, enum.TooltypeOrigin)
	res, err := tool.Execute(ctx, call, nil)
	if err != nil || !strings.Contains(res.Text, "workspace body") {
		t.Fatalf("bound read = %+v %v", res, err)
	}
	if _, err := tool.Execute(t.Context(), call, nil); err == nil {
		t.Fatal("workspace skill leaked into unbound context")
	}
	for _, raw := range []string{`{`, `{}`, `null`, `{"name":" "}`, `{"name":"missing"}`, `{"name":false}`} {
		bad, _ := entity.NewToolCall("read_2", "read_skill", raw, enum.TooltypeOrigin)
		if _, err := tool.Execute(ctx, bad, nil); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := tool.Execute(ctx, call, nil); err != context.Canceled {
		t.Fatalf("cancel = %v", err)
	}
	if _, err := NewSkillsTool(nil); err == nil {
		t.Fatal("nil discovery repository accepted")
	}
	if _, err := NewReadSkillTool(nil); err == nil {
		t.Fatal("nil read repository accepted")
	}
}
