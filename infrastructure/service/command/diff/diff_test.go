package cmddiff

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

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

// /diff caps its output by bytes. A caps boundary inside a multibyte
// rune leaves an invalid UTF-8 tail that a terminal/UI cannot render.
func TestDiffTruncationKeepsValidUTF8(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.test")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "big.txt")
	run("commit", "-qm", "base")
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("界", 30000)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	session := &diffSession{dir: dir}
	err := New(session).Execute(context.Background(), "", dservice.CommandEmitter(func(s string) { out.WriteString(s) }))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(out.String()) {
		t.Fatalf("/diff output is invalid UTF-8 after truncation (len=%d)", out.Len())
	}
}

// /diff must work in a freshly initialized workspace before the first
// commit. `git diff HEAD` has no object to compare in that state, and the
// failure text differs across Git versions ("invalid object name",
// "bad revision 'HEAD'", "ambiguous argument"), so the command probes the
// ref instead of parsing that text.
func TestDiffWorksBeforeFirstCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "notes.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	cmd := New(&diffSession{dir: dir})
	if err := cmd.Execute(context.Background(), "", dservice.CommandEmitter(func(s string) { out.WriteString(s) })); err != nil {
		t.Fatalf("/diff in a repo without HEAD: %v", err)
	}
	if !strings.Contains(out.String(), "+second") {
		t.Fatalf("/diff before the first commit omitted the change:\n%s", out.String())
	}
}
