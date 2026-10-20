//go:build linux

package system

import (
	"errors"
	"strings"
	"testing"
)

// withSelection stages the candidate list and PATH presence — New must
// stay subprocess-free, so both seams are plain vars.
func withSelection(t *testing.T, cands []candidate, onPath func(string) bool) {
	t.Helper()
	prevC, prevL := candidates, lookPath
	t.Cleanup(func() { candidates, lookPath = prevC, prevL })
	candidates = cands
	lookPath = func(name string) (string, error) {
		if onPath(name) {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func TestNewPicksFirstPresentCandidate(t *testing.T) {
	withSelection(t, []candidate{
		{key: "ghost", service: fakeManager{name: "ghost"}, present: binaryOnPath("ghostctl")},
		{key: "systemd", service: systemd{}, present: binaryOnPath("systemctl")},
	}, func(n string) bool {
		return n == "systemctl" // only the systemd binary exists
	})
	if _, ok := New().(systemd); !ok {
		t.Fatalf("New() = %T, want systemd", New())
	}
}

func TestNewHonorsBackendOverride(t *testing.T) {
	withSelection(t, []candidate{
		{key: "first", service: fakeManager{name: "first"}, present: func() bool { return true }},
		{key: "second", service: fakeManager{name: "second"}, present: func() bool { return true }},
	}, func(string) bool { return true })
	t.Setenv(BackendEnv, "second")
	if got := New().(fakeManager).name; got != "second" {
		t.Fatalf("New() = %v, want the named backend", got)
	}
}

func TestNewOverrideMissingIsUnavailable(t *testing.T) {
	withSelection(t, []candidate{
		{key: "first", service: fakeManager{name: "first"}, present: func() bool { return true }},
		{key: "ghost", service: fakeManager{name: "ghost"}, present: func() bool { return false }},
	}, func(string) bool { return true })
	t.Setenv(BackendEnv, "ghost")
	svc := New()
	if err := svc.Available(); err == nil || !strings.Contains(err.Error(), BackendEnv) {
		t.Fatalf("Available() = %v, want the override named in the reason", err)
	}
}

func TestNewUnknownOverrideIsUnavailable(t *testing.T) {
	withSelection(t, []candidate{{key: "systemd", service: systemd{}, present: binaryOnPath("systemctl")}}, func(string) bool { return true })
	t.Setenv(BackendEnv, "bogus")
	svc := New()
	if _, ok := svc.(unavailable); !ok {
		t.Fatalf("New() = %T, want unavailable", svc)
	}
	if err := svc.Available(); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("Available() = %v, want the bad key named", err)
	}
}

func TestNewNothingPresentIsUnavailable(t *testing.T) {
	withSelection(t, []candidate{{key: "systemd", service: systemd{}, present: binaryOnPath("systemctl")}}, func(string) bool { return false })
	if _, ok := New().(unavailable); !ok {
		t.Fatalf("New() = %T, want unavailable", New())
	}
}

func TestUnavailableDegradesVerbs(t *testing.T) {
	svc := unavailable{name: "test", reason: "gone"}
	if svc.Active("x") || svc.ActiveElsewhere("x") {
		t.Fatal("unavailable service reported active")
	}
	if err := svc.Start("x"); err == nil {
		t.Fatal("Start succeeded on an unavailable manager")
	}
	if err := svc.PersistAcrossLogout(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PersistAcrossLogout = %v, want ErrUnsupported", err)
	}
}

// fakeManager is a minimal Service for selection tests.
type fakeManager struct{ name string }

func (f fakeManager) Name() string                      { return f.name }
func (f fakeManager) Available() error                  { return nil }
func (f fakeManager) Register(Spec) error               { return nil }
func (f fakeManager) Deregister(string) (string, error) { return "", nil }
func (f fakeManager) Start(string) error                { return nil }
func (f fakeManager) Stop(string) error                 { return nil }
func (f fakeManager) Status(string) (string, State, error) {
	return "", StateUnknown, nil
}
func (f fakeManager) Active(string) bool          { return false }
func (f fakeManager) ActiveElsewhere(string) bool { return false }
func (f fakeManager) PersistAcrossLogout() error  { return nil }
