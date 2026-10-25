//go:build linux

package toolexec

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

func TestCustomExecutableAvailableToExecPipesAndPTY(t *testing.T) {
	home, storage, workspace := t.TempDir(), t.TempDir(), t.TempDir()
	bin := filepath.Join(workspace, ".agents/tools/bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "custom-fixture"), []byte("#!/bin/sh\nprintf 'custom:%s:%s' \"$1\" \"$PWD\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	env := LocalEnvironment{Home: home, Storage: storage}
	var out bytes.Buffer
	exit, err := NewLocalRunner(env).Run(t.Context(), "custom-fixture 'space argument'", workspace, nil, &out)
	want := "custom:space argument:" + workspace
	if err != nil || exit.Code != 0 || out.String() != want {
		t.Fatalf("exec=%+v %q %v", exit, out.String(), err)
	}
	for _, tty := range []bool{false, true} {
		store := NewSessionStore(env)
		session, err := store.Open(t.Context(), "room", dservice.TerminalOpenRequest{Command: "custom-fixture 'space argument'", Dir: workspace, TTY: tty})
		if err != nil {
			t.Fatal(err)
		}
		defer store.TerminateAll()
		poll := session.Collect(t.Context(), time.Second, nil)
		if !strings.Contains(poll.Output, want) {
			t.Fatalf("tty=%v output=%q", tty, poll.Output)
		}
	}
}
