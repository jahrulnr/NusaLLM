package cmddiff

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// diffSession embeds the interface so the test only has to provide the
// two methods /diff really touches.
type diffSession struct {
	appcommand.SlashSession
	dir string
}

func (s *diffSession) Path() string { return s.dir }
func (s *diffSession) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// /diff must work in a freshly initialized workspace before the first
// commit. `git diff HEAD` has no object to compare in that state, and the
// fallback only recognizes one exact Git error ("invalid object name");
// the actual "bad revision 'HEAD'" falls through as a command failure.
func TestDiffWorksBeforeFirstCommit(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "notes.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := New(&diffSession{dir: dir})
	var out strings.Builder
	if err := cmd.Execute(context.Background(), "", func(chunk string) { out.WriteString(chunk) }); err != nil {
		t.Fatalf("/diff in a repo without HEAD: %v", err)
	}
	if !strings.Contains(out.String(), "+second") {
		t.Fatalf("/diff omitted the working-tree change:\n%s", out.String())
	}
}
