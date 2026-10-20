//go:build linux

package system

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withExec stubs the host-exec seam — the adapter must never talk to a
// real systemd bus in tests.
func withExec(t *testing.T, fn func(name string, args []string) ([]byte, int, error)) *[]string {
	t.Helper()
	calls := []string{}
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(name string, args ...string) ([]byte, int, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return fn(name, args)
	}
	return &calls
}

// scriptSystemd answers systemctl the way a bus-up host does, active or
// inactive per the flag. bus=false answers every call as if the user
// bus were missing.
func scriptSystemd(t *testing.T, active, bus bool) *[]string {
	t.Helper()
	return withExec(t, func(name string, args []string) ([]byte, int, error) {
		if name == "systemctl" && !bus {
			return []byte("Failed to connect to bus: No such file or directory\n"), 1, nil
		}
		if name == "systemctl" && len(args) > 1 && args[1] == "is-active" {
			if active {
				return []byte("active\n"), 0, nil
			}
			return []byte("inactive\n"), 3, nil
		}
		return nil, 0, nil
	})
}

func specFixture(t *testing.T) (Spec, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	cfg := filepath.Join(home, "cfg", "config.json")
	return Spec{
		Name:        "nusallm",
		Description: "NusaLLM",
		Exec:        "/home/user/.local/bin/nusallm",
		Args:        []string{"serve"},
		WorkingDir:  filepath.Join(home, "data"),
		Env:         map[string]string{"CONFIG_PATH": cfg},
	}, home
}

func TestUnitUsesAbsoluteExecAndPinnedEnv(t *testing.T) {
	spec, home := specFixture(t)
	got, err := systemd{}.render(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := `[Unit]
Description=NusaLLM
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
ExecStart=/home/user/.local/bin/nusallm serve
WorkingDirectory=` + home + `/data
Environment=CONFIG_PATH=` + home + `/cfg/config.json
Restart=on-failure
RestartSec=5
TimeoutStopSec=20
KillMode=control-group

[Install]
WantedBy=default.target
`
	if got != want {
		t.Fatalf("unit =\n%s\nwant\n%s", got, want)
	}
}

func TestUnitQuotesSpacesAndSortsEnv(t *testing.T) {
	spec, _ := specFixture(t)
	spec.Exec = "/home/user name/bin/nusallm"
	spec.Env["A_KEY"] = "has space"
	got, err := systemd{}.render(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`ExecStart="/home/user name/bin/nusallm" serve`,
		`Environment="A_KEY=has space"`,
	} {
		if !strings.Contains(got, line) {
			t.Fatalf("unit missing %q\n%s", line, got)
		}
	}
	if strings.Index(got, "Environment=A_KEY") > strings.Index(got, "Environment=CONFIG_PATH") {
		t.Fatalf("env lines not sorted\n%s", got)
	}
	if _, err := (systemd{}).render(Spec{Exec: "/bin/x\nrm"}); err == nil {
		t.Fatal("line break in Exec was not rejected")
	}
}

func TestRegisterWritesUnitAndEnables(t *testing.T) {
	spec, home := specFixture(t)
	calls := scriptSystemd(t, false, true)
	if err := (systemd{}).Register(spec); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "nusallm.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "ExecStart=/home/user/.local/bin/nusallm serve") {
		t.Fatalf("unit =\n%s", unit)
	}
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{"daemon-reload", "enable --now nusallm.service"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, " restart ") {
		t.Fatalf("first register restarted:\n%s", joined)
	}
}

func TestRegisterRestartsActiveService(t *testing.T) {
	spec, _ := specFixture(t)
	calls := scriptSystemd(t, true, true)
	if err := (systemd{}).Register(spec); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*calls, "\n")
	enable := strings.Index(joined, "enable --now")
	restart := strings.Index(joined, " restart ")
	if enable < 0 || restart < 0 || restart < enable {
		t.Fatalf("want enable then restart:\n%s", joined)
	}
}

func TestRegisterFailsWhenBusDown(t *testing.T) {
	spec, home := specFixture(t)
	scriptSystemd(t, false, false)
	err := (systemd{}).Register(spec)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nusallm.service")); !os.IsNotExist(statErr) {
		t.Fatalf("unit written with bus down: %v", statErr)
	}
}

func TestRegisterRejectsEmptyExec(t *testing.T) {
	spec, _ := specFixture(t)
	spec.Exec = ""
	if err := (systemd{}).Register(spec); err == nil {
		t.Fatal("empty Exec registered")
	}
}

func TestVerbsRejectBadNames(t *testing.T) {
	specFixture(t)
	for _, name := range []string{"", ".", "..", "../evil", "a/b", "-x", "a b", "a;x", "a\nb"} {
		t.Run(name, func(t *testing.T) {
			if _, err := (systemd{}).Deregister(name); err == nil {
				t.Fatal("Deregister accepted a bad name")
			}
			if err := (systemd{}).Start(name); err == nil {
				t.Fatal("Start accepted a bad name")
			}
			if err := (systemd{}).Stop(name); err == nil {
				t.Fatal("Stop accepted a bad name")
			}
			if _, _, err := (systemd{}).Status(name); err == nil {
				t.Fatal("Status accepted a bad name")
			}
			if (systemd{}).Active(name) {
				t.Fatal("Active reported a bad name active")
			}
			if (systemd{}).ActiveElsewhere(name) {
				t.Fatal("ActiveElsewhere reported a bad name active")
			}
		})
	}
}

func TestRegisterRejectsBadSpecFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Spec)
	}{
		{"traversal name", func(s *Spec) { s.Name = "../evil" }},
		{"flag-like name", func(s *Spec) { s.Name = "--system" }},
		{"description line break", func(s *Spec) { s.Description = "ok\nExecStart=/bin/evil" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, home := specFixture(t)
			tc.mutate(&spec)
			scriptSystemd(t, false, true)
			if err := (systemd{}).Register(spec); err == nil {
				t.Fatal("bad spec registered")
			}
			if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user")); !os.IsNotExist(err) {
				t.Fatalf("unit dir created for a rejected spec: %v", err)
			}
		})
	}
}

func TestUnitEscapesSpecifierAndDollar(t *testing.T) {
	spec, _ := specFixture(t)
	spec.Exec = "/opt/100%real/bin/x"
	spec.Args = []string{"serve", "$HOME"}
	spec.Description = "NusaLLM %h $USER"
	spec.Env["HOME_DIR"] = "$HOME/data"
	got, err := systemd{}.render(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`ExecStart=/opt/100%%real/bin/x serve $$HOME`,
		`Environment=HOME_DIR=$$HOME/data`,
		`Description="NusaLLM %%h $USER"`,
	} {
		if !strings.Contains(got, line) {
			t.Fatalf("unit missing %q\n%s", line, got)
		}
	}
}

func TestDeregisterStopsAndRemoves(t *testing.T) {
	spec, home := specFixture(t)
	unitPath := filepath.Join(home, ".config", "systemd", "user", "nusallm.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := scriptSystemd(t, true, true)
	note, err := (systemd{}).Deregister(spec.Name)
	if err != nil || note != "" {
		t.Fatalf("note=%q err=%v", note, err)
	}
	if _, statErr := os.Stat(unitPath); !os.IsNotExist(statErr) {
		t.Fatalf("unit still present: %v", statErr)
	}
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{"disable --now nusallm.service", "daemon-reload"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls missing %q:\n%s", want, joined)
		}
	}
}

func TestDeregisterRemovesFileWhenBusDown(t *testing.T) {
	spec, home := specFixture(t)
	unitPath := filepath.Join(home, ".config", "systemd", "user", "nusallm.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptSystemd(t, false, false)
	note, err := (systemd{}).Deregister(spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "unavailable") {
		t.Fatalf("note = %q, want the degraded path named", note)
	}
	if _, statErr := os.Stat(unitPath); !os.IsNotExist(statErr) {
		t.Fatalf("unit still present: %v", statErr)
	}
}

func TestDeregisterRemovesFileWhenDisableFails(t *testing.T) {
	spec, home := specFixture(t)
	unitPath := filepath.Join(home, ".config", "systemd", "user", "nusallm.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := withExec(t, func(name string, args []string) ([]byte, int, error) {
		if name == "systemctl" && len(args) > 1 && args[1] == "disable" {
			return []byte("Failed to disable unit: Unit file is masked\n"), 5, nil
		}
		return nil, 0, nil
	})
	// A live-bus disable failure still removes the file and reloads —
	// a stale enabled unit would autostart next login.
	note, err := (systemd{}).Deregister(spec.Name)
	if err == nil || !strings.Contains(err.Error(), "disable --now") {
		t.Fatalf("err = %v, want the disable failure reported", err)
	}
	if note != "" {
		t.Fatalf("note = %q, want none — the error carries the report", note)
	}
	if _, statErr := os.Stat(unitPath); !os.IsNotExist(statErr) {
		t.Fatalf("unit still present after failed disable: %v", statErr)
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "daemon-reload") {
		t.Fatalf("daemon-reload skipped:\n%s", strings.Join(*calls, "\n"))
	}
}

func TestDeregisterMissingUnitIsSuccess(t *testing.T) {
	specFixture(t)
	note, err := (systemd{}).Deregister("nusallm")
	if err != nil || note != "" {
		t.Fatalf("note=%q err=%v", note, err)
	}
}

func TestStartResetsFailedCounter(t *testing.T) {
	specFixture(t)
	calls := scriptSystemd(t, false, true)
	if err := (systemd{}).Start("nusallm"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*calls, "\n")
	reset := strings.Index(joined, "reset-failed nusallm.service")
	start := strings.Index(joined, " start nusallm.service")
	if reset < 0 || start < 0 || reset > start {
		t.Fatalf("want reset-failed before start:\n%s", joined)
	}
}

func TestStartFailurePointsAtJournal(t *testing.T) {
	specFixture(t)
	withExec(t, func(name string, args []string) ([]byte, int, error) {
		if name == "systemctl" && len(args) > 1 && args[1] == "start" {
			return []byte("Job for nusallm.service failed\n"), 1, nil
		}
		return nil, 0, nil
	})
	err := (systemd{}).Start("nusallm")
	if err == nil || !strings.Contains(err.Error(), "journalctl --user -e -u nusallm.service") {
		t.Fatalf("err = %v, want the journal hint inside", err)
	}
}

func TestStatusMapsActiveState(t *testing.T) {
	specFixture(t)
	for _, tc := range []struct {
		name        string
		statusOut   string
		statusCode  int
		activeState string
		want        State
	}{
		{"running", "● nusallm.service\n     Active: active (running)\n", 0, "", StateRunning},
		{"stopped", "○ nusallm.service\n     Active: inactive (dead)\n", 3, "inactive\n", StateStopped},
		{"failed", "× nusallm.service\n     Active: failed (Result: exit-code)\n", 3, "failed\n", StateFailed},
		{"unregistered", "Unit nusallm.service could not be found.\n", 4, "inactive\n", StateStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withExec(t, func(name string, args []string) ([]byte, int, error) {
				if name == "systemctl" && len(args) > 1 && args[1] == "show" {
					return []byte(tc.activeState), 0, nil
				}
				return []byte(tc.statusOut), tc.statusCode, nil
			})
			report, state, err := (systemd{}).Status("nusallm")
			if err != nil {
				t.Fatal(err)
			}
			if state != tc.want {
				t.Fatalf("state = %v, want %v", state, tc.want)
			}
			if report == "" {
				t.Fatal("empty report")
			}
		})
	}
}

func TestStatusBusDownIsQueryError(t *testing.T) {
	specFixture(t)
	scriptSystemd(t, false, false)
	_, state, err := (systemd{}).Status("nusallm")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v, want the bus failure named", err)
	}
	if state != StateUnknown {
		t.Fatalf("state = %v, want StateUnknown on a failed query", state)
	}
}

func TestStatusExecFailureIsError(t *testing.T) {
	specFixture(t)
	withExec(t, func(name string, args []string) ([]byte, int, error) {
		return nil, -1, errors.New("no systemctl")
	})
	_, state, err := (systemd{}).Status("nusallm")
	if err == nil || state != StateUnknown {
		t.Fatalf("state=%v err=%v", state, err)
	}
}

func TestActiveElsewhere(t *testing.T) {
	specFixture(t)
	for _, tc := range []struct {
		name   string
		active bool
		env    string
		unitID string
		want   bool
	}{
		{"inactive unit", false, "", "", false},
		{"active unit, not under systemd", true, "", "", true},
		{"active unit, foreign invocation", true, "scope-123", "unit-456", true},
		{"this process is the unit", true, "unit-456", "unit-456", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", tc.env)
			withExec(t, func(name string, args []string) ([]byte, int, error) {
				if name == "systemctl" && len(args) > 1 && args[1] == "is-active" {
					if tc.active {
						return nil, 0, nil
					}
					return []byte("inactive\n"), 3, nil
				}
				if name == "systemctl" && len(args) > 1 && args[1] == "show" {
					return []byte(tc.unitID + "\n"), 0, nil
				}
				return nil, 0, nil
			})
			if got := (systemd{}).ActiveElsewhere("nusallm"); got != tc.want {
				t.Fatalf("ActiveElsewhere() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestActiveElsewhereDegradesWhenShowFails(t *testing.T) {
	specFixture(t)
	// An unverifiable identity must not claim the service holds
	// anything — false degrades to the generic message.
	t.Setenv("INVOCATION_ID", "scope-123")
	withExec(t, func(name string, args []string) ([]byte, int, error) {
		if name == "systemctl" && len(args) > 1 && args[1] == "is-active" {
			return nil, 0, nil
		}
		if name == "systemctl" && len(args) > 1 && args[1] == "show" {
			return nil, -1, errors.New("bus gone")
		}
		return nil, 0, nil
	})
	if (systemd{}).ActiveElsewhere("nusallm") {
		t.Fatal("claimed the service active elsewhere when identity could not be checked")
	}
}

func TestPersistAcrossLogoutErrorNamesRemediation(t *testing.T) {
	specFixture(t)
	withExec(t, func(name string, args []string) ([]byte, int, error) {
		if name == "loginctl" {
			return []byte("Access denied\n"), 1, nil
		}
		return nil, 0, nil
	})
	err := (systemd{}).PersistAcrossLogout()
	if err == nil || !strings.Contains(err.Error(), "sudo loginctl enable-linger") {
		t.Fatalf("err = %v, want the remediation inside", err)
	}
}

func TestSystemctlArgsCarryUserScope(t *testing.T) {
	specFixture(t)
	calls := withExec(t, func(name string, args []string) ([]byte, int, error) {
		return nil, 0, nil
	})
	_ = (systemd{}).Stop("nusallm")
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "systemctl --user stop nusallm.service") {
		t.Fatalf("calls:\n%s", joined)
	}
}
