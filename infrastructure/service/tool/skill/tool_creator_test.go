//go:build linux

package skill

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	reposkill "github.com/jahrulnr/nusallm/infrastructure/repository/skill"
	toolexec "github.com/jahrulnr/nusallm/infrastructure/service/tool/terminal"
)

func TestToolCreatorInitializesValidatesAndRunsThroughTerminal(t *testing.T) {
	python := "/usr/bin/python3"
	if _, err := os.Stat(python); err != nil {
		t.Skip("system Python 3 is not installed")
	}
	t.Setenv("PATH", "/usr/bin:/bin")

	workspace := filepath.Join(t.TempDir(), "workspace with spaces")
	home, storage, builtin := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := reposkill.InstallBuiltins(builtin, Builtins()); err != nil {
		t.Fatal(err)
	}
	creator := filepath.Join(builtin, "tool-creator", "scripts")
	initialize := filepath.Join(creator, "init_tool.py")
	validate := filepath.Join(creator, "validate_tool.py")

	run := func(script string, args ...string) ([]byte, error) {
		t.Helper()
		command := exec.Command(python, append([]string{script}, args...)...)
		command.Dir = workspace
		return command.CombinedOutput()
	}
	if output, err := run(initialize, "shared-report", "--scope", "global"); err != nil {
		t.Fatalf("initialize global tool: %v\n%s", err, output)
	}
	if output, err := run(validate, "shared-report", "--scope", "global"); err != nil {
		t.Fatalf("validate global tool: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "tools", "bin", "shared-report")); err != nil {
		t.Fatalf("global executable missing: %v", err)
	}
	if output, err := run(initialize, "json-echo"); err != nil {
		t.Fatalf("initialize: %v\n%s", err, output)
	}

	toolBin := filepath.Join(workspace, ".agents", "tools", "bin")
	skillDir := filepath.Join(workspace, ".agents", "skills", "json-echo")
	executable := filepath.Join(toolBin, "json-echo")
	implementation := filepath.Join(toolBin, "json-echo.tool", "main.py")
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if output, err := run(validate, "json-echo"); err != nil {
		t.Fatalf("validate generated tool: %v\n%s", err, output)
	}
	beforeExecutable, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	beforeImplementation, err := os.ReadFile(implementation)
	if err != nil {
		t.Fatal(err)
	}
	beforeSkill, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := run(initialize, "json-echo"); err == nil || !strings.Contains(string(output), "without overwriting") {
		t.Fatalf("existing tool was not rejected: err=%v output=%s", err, output)
	}
	for _, file := range []struct {
		path string
		want []byte
	}{
		{executable, beforeExecutable},
		{implementation, beforeImplementation},
		{skillFile, beforeSkill},
	} {
		got, err := os.ReadFile(file.path)
		if err != nil || !bytes.Equal(got, file.want) {
			t.Fatalf("existing path %s changed: err=%v", file.path, err)
		}
	}

	originalSkill := beforeSkill
	brokenMetadata := strings.Replace(string(originalSkill), "name: json-echo", "name: wrong-name", 1)
	if err := os.WriteFile(skillFile, []byte(brokenMetadata), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := run(validate, "json-echo"); err == nil || !strings.Contains(string(output), "name must be") {
		t.Fatalf("invalid skill metadata was not reported: err=%v output=%s", err, output)
	}
	foldedDescription := strings.Replace(
		string(originalSkill),
		`description: "Use the json-echo executable for its documented custom workflow."`,
		"description: >-\n  Use the json-echo executable\n  for its documented custom workflow.",
		1,
	)
	if foldedDescription == string(originalSkill) {
		t.Fatal("generated description changed; folded metadata probe needs an update")
	}
	if err := os.WriteFile(skillFile, []byte(foldedDescription), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := run(validate, "json-echo"); err != nil {
		t.Fatalf("valid folded skill description was rejected: %v\n%s", err, output)
	}
	brokenReference := strings.Replace(string(originalSkill), "references/usage.md", "references/missing.md", 1)
	if err := os.WriteFile(skillFile, []byte(brokenReference), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := run(validate, "json-echo"); err == nil || !strings.Contains(string(output), "missing link target") {
		t.Fatalf("broken skill reference was not reported: err=%v output=%s", err, output)
	}
	if err := os.WriteFile(skillFile, originalSkill, 0644); err != nil {
		t.Fatal(err)
	}

	resource := filepath.Join(toolBin, "json-echo.tool", "resources", "marker.txt")
	if err := os.WriteFile(resource, []byte("from-tool-directory\n"), 0644); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(implementation)
	if err != nil {
		t.Fatal(err)
	}
	old := "    return payload\n"
	replacement := "    return {\"resource\": (RESOURCE_DIR / \"marker.txt\").read_text(encoding=\"utf-8\").strip(), \"input\": payload}\n"
	if !strings.Contains(string(source), old) {
		t.Fatal("implementation template changed; resource probe needs an update")
	}
	source = []byte(strings.Replace(string(source), old, replacement, 1))
	if err := os.WriteFile(implementation, source, 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := run(validate, "json-echo"); err != nil {
		t.Fatalf("validate customized tool: %v\n%s", err, output)
	}

	runner := toolexec.NewLocalRunner(toolexec.LocalEnvironment{Home: home, Storage: storage})
	var output bytes.Buffer
	exit, err := runner.Run(t.Context(), "json-echo", workspace, strings.NewReader(`{"answer":42}`), &output)
	if err != nil || exit.Code != 0 {
		t.Fatalf("terminal invocation exit=%+v err=%v output=%s", exit, err, output.String())
	}
	var result struct {
		Resource string         `json:"resource"`
		Input    map[string]int `json:"input"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("terminal output is not JSON: %v: %s", err, output.String())
	}
	if result.Resource != "from-tool-directory" || result.Input["answer"] != 42 {
		t.Fatalf("terminal result = %+v", result)
	}

	output.Reset()
	exit, err = runner.Run(t.Context(), "json-echo", workspace, strings.NewReader("{"), &output)
	if err != nil || exit.Code != 2 || !strings.Contains(output.String(), "invalid JSON input") {
		t.Fatalf("invalid input exit=%+v err=%v output=%s", exit, err, output.String())
	}
}
