package reposkill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func writeSkill(t *testing.T, root, dir, text string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoveryPrecedenceRefreshAndRead(t *testing.T) {
	home, workspace, builtin := t.TempDir(), t.TempDir(), t.TempDir()
	global := filepath.Join(home, ".agents", "skills")
	local := filepath.Join(workspace, ".agents", "skills")
	writeSkill(t, builtin, "base", "---\nname: base\ndescription: Built in\n---\nbase body")
	writeSkill(t, builtin, "shared", "---\nname: shared\ndescription: Built in shared\n---\n")
	writeSkill(t, global, "shared", "---\nname: shared\ndescription: Global\n---\nglobal body")
	text := "---\r\nname: shared\r\ndescription: >-\r\n  Workspace skill\r\n  with folded YAML.\r\nmetadata:\r\n  short-description: useful\r\n---\r\nraw body\r\n"
	path := writeSkill(t, local, "shared", text)
	store := NewLocal(home, builtin)
	catalog, err := store.List(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 2 || len(catalog.Diagnostics) != 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	if s := catalog.Skills[1]; s.Name != "shared" || s.Source != "workspace" || s.Path != path || s.Description != "Workspace skill with folded YAML." {
		t.Fatalf("skill = %+v", s)
	}
	skill, raw, err := store.Read(t.Context(), workspace, "shared")
	if err != nil || raw != text || skill.Path != path {
		t.Fatalf("read = %+v %q %v", skill, raw, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	skill, raw, err = store.Read(t.Context(), workspace, "shared")
	if err != nil || skill.Source != "global" || !strings.Contains(raw, "global body") {
		t.Fatalf("refresh = %+v %q %v", skill, raw, err)
	}
	if _, _, err := store.Read(t.Context(), workspace, "unknown"); err == nil {
		t.Fatal("unknown skill accepted")
	}
}

func TestMissingRootsMalformedMetadataAndCancellation(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	store := NewLocal(home, filepath.Join(t.TempDir(), "missing"))
	catalog, err := store.List(t.Context(), workspace)
	if err != nil || len(catalog.Skills) != 0 || len(catalog.Diagnostics) != 0 {
		t.Fatalf("missing roots = %+v %v", catalog, err)
	}
	root := filepath.Join(workspace, ".agents", "skills")
	for name, text := range map[string]string{
		"no-header":      "# plain markdown",
		"no-description": "---\nname: incomplete\n---\nbody",
		"bad-yaml":       "---\nname: [\n---\nbody",
		"unclosed":       "---\nname: open\ndescription: open",
	} {
		writeSkill(t, root, name, text)
	}
	catalog, err = store.List(t.Context(), workspace)
	if err != nil || len(catalog.Skills) != 0 || len(catalog.Diagnostics) != 4 {
		t.Fatalf("malformed = %+v %v", catalog, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.List(ctx, workspace); err != context.Canceled {
		t.Fatalf("cancel = %v", err)
	}
}

func TestInstallBuiltinsAndSymlinkDiscovery(t *testing.T) {
	home, builtin := t.TempDir(), t.TempDir()
	assets := fstest.MapFS{"creator/SKILL.md": {Data: []byte("---\nname: creator\ndescription: Create things\n---\nbody")}}
	if err := InstallBuiltins(builtin, assets); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(builtin, "removed", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(builtin, ".skill-123"), []byte("temp"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := InstallBuiltins(builtin, assets); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("unshipped builtin file was not pruned")
	}
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Fatal("emptied builtin directory was not pruned")
	}
	if _, err := os.Stat(filepath.Join(builtin, ".skill-123")); err != nil {
		t.Fatal("in-flight publish temp was pruned")
	}
	root := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(builtin, "creator"), filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	catalog, err := NewLocal(home, builtin).List(t.Context(), "")
	if err != nil || len(catalog.Skills) != 1 || catalog.Skills[0].Source != "global" {
		t.Fatalf("symlink = %+v %v", catalog, err)
	}
}
