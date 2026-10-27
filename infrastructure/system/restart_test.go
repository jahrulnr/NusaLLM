package system

import (
	"errors"
	"testing"
)

func TestSystemdRestartQueuesWithoutBlocking(t *testing.T) {
	calls := scriptSystemd(t, true, true)
	if err := (systemd{}).Restart("nusallm"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	want := "systemctl --user --no-block restart nusallm.service"
	if len(*calls) == 0 || (*calls)[len(*calls)-1] != want {
		t.Fatalf("calls = %v, want last %q", *calls, want)
	}
}

func TestSystemdRestartRejectsBadName(t *testing.T) {
	calls := scriptSystemd(t, true, true)
	if err := (systemd{}).Restart("-x"); err == nil {
		t.Fatal("a flag-like name reached systemctl")
	}
	if len(*calls) != 0 {
		t.Fatalf("bad name touched the manager: %v", *calls)
	}
}

// restartFake is a registered service whose state the test sets directly.
type restartFake struct {
	fakeManager
	active, elsewhere bool
	restarted         []string
	restartErr        error
}

func (f *restartFake) Active(string) bool          { return f.active }
func (f *restartFake) ActiveElsewhere(string) bool { return f.elsewhere }
func (f *restartFake) Restart(name string) error {
	f.restarted = append(f.restarted, name)
	return f.restartErr
}

func TestRestarterModeFollowsManagerOwnership(t *testing.T) {
	cases := []struct {
		name              string
		active, elsewhere bool
		want              string
	}{
		{"owned by the manager", true, false, "service"},
		{"manager runs another process", true, true, "process"},
		{"no registered service running", false, false, "process"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &restartFake{active: c.active, elsewhere: c.elsewhere}
			r := NewRestarter(svc, "nusallm", "/home/u/.local/bin/nusallm", []string{"nusallm", "serve"})
			if got := r.Mode(); got != c.want {
				t.Fatalf("Mode() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRestarterServiceModeAsksTheManager(t *testing.T) {
	svc := &restartFake{active: true}
	reexecCalled := false
	r := NewRestarter(svc, "nusallm", "/bin/nusallm", nil)
	r.reexec = func(string, []string) error { reexecCalled = true; return nil }
	if err := r.Restart("service"); err != nil {
		t.Fatalf("Restart(service): %v", err)
	}
	if len(svc.restarted) != 1 || svc.restarted[0] != "nusallm" || reexecCalled {
		t.Fatalf("restarted=%v reexec=%v", svc.restarted, reexecCalled)
	}
}

func TestRestarterProcessModeReplacesInPlace(t *testing.T) {
	svc := &restartFake{}
	var gotPath string
	var gotArgv []string
	r := NewRestarter(svc, "nusallm", "/home/u/.local/bin/nusallm", []string{"nusallm", "serve", "--port", "11998"})
	r.reexec = func(path string, argv []string) error {
		gotPath, gotArgv = path, argv
		return nil
	}
	if err := r.Restart("process"); err != nil {
		t.Fatalf("Restart(process): %v", err)
	}
	if len(svc.restarted) != 0 {
		t.Fatalf("manager restarted a foreground run: %v", svc.restarted)
	}
	if gotPath != "/home/u/.local/bin/nusallm" || len(gotArgv) != 4 || gotArgv[3] != "11998" {
		t.Fatalf("reexec path=%q argv=%v", gotPath, gotArgv)
	}
}

func TestRestarterSurfacesManagerError(t *testing.T) {
	svc := &restartFake{active: true, restartErr: errors.New("bus down")}
	r := NewRestarter(svc, "nusallm", "/bin/nusallm", nil)
	if err := r.Restart("service"); err == nil {
		t.Fatal("a failing manager restart was reported as success")
	}
}
