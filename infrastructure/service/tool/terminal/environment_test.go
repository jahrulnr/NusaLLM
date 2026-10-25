package toolexec

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestToolPathsResolveWorkspaceAndPreserveParentEnvironment(t *testing.T) {
	home, storage, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	e := LocalEnvironment{Home: home, Storage: storage, Paths: []string{"local tools", "~/shared/bin", "", "local tools"}}
	want := []string{filepath.Join(workspace, ".agents/tools/bin"), filepath.Join(home, ".agents/tools/bin"), filepath.Join(workspace, "local tools"), filepath.Join(home, "shared/bin"), filepath.Join(storage, "tools/bin")}
	if got := e.ToolPaths(workspace); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths=%v, want %v", got, want)
	}
	t.Setenv("PATH", "/parent/bin")
	t.Setenv("NUSALLM_RUNTIME_DIR", "/stale")
	t.Setenv("CUSTOM_VALUE", "kept")
	values := map[string]string{}
	for _, kv := range e.Environ(workspace) {
		key, value, _ := strings.Cut(kv, "=")
		if _, seen := values[key]; seen {
			t.Fatalf("duplicate env key %q", key)
		}
		values[key] = value
	}
	path := strings.Join(append(append(want, filepath.Join(storage, "runtimes/bin")), "/parent/bin"), string(os.PathListSeparator))
	if values["PATH"] != path || values["CUSTOM_VALUE"] != "kept" || values["NUSALLM_RUNTIME_DIR"] != filepath.Join(storage, "runtimes") {
		t.Fatalf("environment=%v", values)
	}
	if os.Getenv("PATH") != "/parent/bin" {
		t.Fatal("daemon PATH was mutated")
	}
	other := e.ToolPaths(t.TempDir())
	if other[0] == want[0] || other[2] == want[2] {
		t.Fatal("workspace-specific paths stuck to previous room")
	}
}

func TestToolPathsSkipUnresolvableHomeAndStorage(t *testing.T) {
	workspace := t.TempDir()
	e := LocalEnvironment{Paths: []string{"~", "~/shared/bin"}}
	got := e.ToolPaths(workspace)
	want := []string{filepath.Join(workspace, ".agents/tools/bin")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths=%v, want %v", got, want)
	}
	for _, kv := range e.Environ(workspace) {
		key, _, _ := strings.Cut(kv, "=")
		if key == "NUSALLM_RUNTIME_DIR" {
			t.Fatal("empty storage must not emit a relative runtime dir")
		}
	}
}
