package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
)

func isolateInstallHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("PATH", "/usr/bin")
	t.Chdir(t.TempDir())
	return home
}

func withExec(t *testing.T, fn func(name string, args []string) ([]byte, int, error)) {
	t.Helper()
	prev := execCommand
	t.Cleanup(func() { execCommand = prev })
	execCommand = func(name string, args ...string) ([]byte, int, error) {
		return fn(name, args)
	}
}

func scriptSystemd(t *testing.T, active, bus bool) *[]string {
	t.Helper()
	calls := []string{}
	withExec(t, func(name string, args []string) ([]byte, int, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, line)
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
	return &calls
}

func TestServeLifecycle(t *testing.T) {
	for _, arg := range []string{"install", "start", "stop", "status", "uninstall"} {
		if !IsServeAdmin(arg) {
			t.Fatalf("IsServeAdmin(%q) = false", arg)
		}
	}
	for _, arg := range []string{"--port", "-h", "help", "--help", "repair"} {
		if IsServeAdmin(arg) {
			t.Fatalf("IsServeAdmin(%q) = true", arg)
		}
	}
}

func TestUserUnitUsesAbsoluteExecAndPinnedConfig(t *testing.T) {
	bin := "/home/user/.local/bin/nusallm"
	cfg := "/home/user/.config/nusallm/config.json"
	data := "/home/user/.local/share/nusallm"
	got, err := renderUserUnit(bin, data, cfg)
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
WorkingDirectory=/home/user/.local/share/nusallm
Environment=CONFIG_PATH=/home/user/.config/nusallm/config.json
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

	quoted, err := renderUserUnit("/home/user name/.local/bin/nusallm", "/home/user name/data", "/home/user name/.config/nusallm/config.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`ExecStart="/home/user name/.local/bin/nusallm" serve`,
		`WorkingDirectory="/home/user name/data"`,
		`Environment="CONFIG_PATH=/home/user name/.config/nusallm/config.json"`,
	} {
		if !strings.Contains(quoted, line) {
			t.Fatalf("quoted unit missing %q\n%s", line, quoted)
		}
	}

	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	if err := os.WriteFile(configPath, []byte("{\"storage\":{\"dir\":\"./data\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := serviceWorkingDir(home, configPath); got != home {
		t.Fatalf("relative storage working dir = %q, want home %q", got, home)
	}
	abs := filepath.Join(home, "state")
	if err := os.WriteFile(configPath, []byte("{\"storage\":{\"dir\":\""+abs+"\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := serviceWorkingDir(home, configPath); got != abs {
		t.Fatalf("absolute storage working dir = %q, want %q", got, abs)
	}
}

func TestInstallBinaryCopiesExecutable(t *testing.T) {
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "nusallm")
	copied, err := installBinary(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("expected a copy")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, before) {
		t.Fatal("copied bytes differ from the executable")
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", fi.Mode().Perm())
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("source executable was modified")
	}
}

func TestInstallBinarySkipsSameFile(t *testing.T) {
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := installBinary(src)
	if err != nil {
		t.Fatal(err)
	}
	if copied {
		t.Fatal("reinstall of the running executable copied onto itself")
	}
	after, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		t.Fatalf("executable changed: before %+v after %+v", before, after)
	}
}

func TestInstallWritesUnitAndEnables(t *testing.T) {
	home := isolateInstallHome(t)
	calls := scriptSystemd(t, false, true)
	var out, errs bytes.Buffer
	if code := Install(nil, &out, &errs); code != 0 {
		t.Fatalf("install exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errs.String())
	}
	bin := filepath.Join(home, ".local", "bin", "nusallm")
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(home, ".config", "systemd", "user", "nusallm.service")
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(home, ".local", "share", "nusallm")
	cfg := filepath.Join(home, ".config", "nusallm", "config.json")
	rendered, err := renderUserUnit(bin, data, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(unit) != rendered {
		t.Fatalf("unit =\n%s\nwant\n%s", unit, rendered)
	}
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Validate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Storage.Dir != data {
		t.Fatalf("storage.dir = %q, want %q", loaded.Storage.Dir, data)
	}
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{"daemon-reload", "enable --now nusallm.service"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, " restart ") {
		t.Fatalf("first install restarted:\n%s", joined)
	}
	if !strings.Contains(errs.String(), "not on PATH") {
		t.Fatalf("stderr = %q, want PATH hint", errs.String())
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:10998") {
		t.Fatalf("stdout = %q, want WebUI url", out.String())
	}
}

func TestInstallRestartsActiveService(t *testing.T) {
	isolateInstallHome(t)
	calls := scriptSystemd(t, true, true)
	var out, errs bytes.Buffer
	if code := Install(nil, &out, &errs); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	joined := strings.Join(*calls, "\n")
	enable := strings.Index(joined, "enable --now")
	restart := strings.Index(joined, " restart ")
	active := strings.Index(joined, "is-active")
	if active < 0 || enable < 0 || restart < 0 || active > enable || restart < enable {
		t.Fatalf("call order:\n%s", joined)
	}
}

func TestInstallSkipsServiceWhenBusUnavailable(t *testing.T) {
	home := isolateInstallHome(t)
	calls := scriptSystemd(t, false, false)
	var out, errs bytes.Buffer
	if code := Install(nil, &out, &errs); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "nusallm")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nusallm.service")); !os.IsNotExist(err) {
		t.Fatalf("unit file err = %v, want not exist", err)
	}
	if strings.Contains(strings.Join(*calls, "\n"), "enable") {
		t.Fatalf("enable ran without a bus:\n%s", strings.Join(*calls, "\n"))
	}
	if !strings.Contains(errs.String(), "service was not installed") {
		t.Fatalf("stderr = %q", errs.String())
	}
}

func TestInstallNoServiceSkipsUnit(t *testing.T) {
	home := isolateInstallHome(t)
	withExec(t, func(name string, args []string) ([]byte, int, error) {
		t.Errorf("command ran with --no-service: %s %s", name, strings.Join(args, " "))
		return nil, 1, nil
	})
	var out, errs bytes.Buffer
	if code := Install([]string{"--no-service"}, &out, &errs); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "nusallm")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "nusallm.service")); !os.IsNotExist(err) {
		t.Fatalf("unit file err = %v, want not exist", err)
	}
}

func TestInstallLeavesExistingConfig(t *testing.T) {
	home := isolateInstallHome(t)
	cfg := filepath.Join(home, ".config", "nusallm", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("{\"storage\":{\"dir\":\"./data\"}}\n")
	if err := os.WriteFile(cfg, body, 0o644); err != nil {
		t.Fatal(err)
	}
	scriptSystemd(t, false, true)
	var out, errs bytes.Buffer
	if code := Install(nil, &out, &errs); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("config rewritten:\n%s", got)
	}
	unit, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "nusallm.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "WorkingDirectory="+home+"\n") {
		t.Fatalf("unit =\n%s", unit)
	}
}

func TestInstallLingerFailureStillSucceeds(t *testing.T) {
	isolateInstallHome(t)
	withExec(t, func(name string, args []string) ([]byte, int, error) {
		if name == "loginctl" {
			return []byte("Access denied\n"), 1, nil
		}
		if name == "systemctl" && len(args) > 1 && args[1] == "is-active" {
			return []byte("inactive\n"), 3, nil
		}
		return nil, 0, nil
	})
	var out, errs bytes.Buffer
	if code := Install([]string{"--linger"}, &out, &errs); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if !strings.Contains(errs.String(), "sudo loginctl enable-linger") {
		t.Fatalf("stderr = %q", errs.String())
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:10998") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestUninstallKeepsConfigAndData(t *testing.T) {
	home := isolateInstallHome(t)
	cfg := filepath.Join(home, ".config", "nusallm", "config.json")
	data := filepath.Join(home, ".local", "share", "nusallm")
	bin := filepath.Join(home, ".local", "bin", "nusallm")
	unit := filepath.Join(home, ".config", "systemd", "user", "nusallm.service")
	for _, path := range []string{cfg, filepath.Join(data, "keep"), bin, unit} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("marker"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	calls := scriptSystemd(t, true, true)
	var out, errs bytes.Buffer
	if code := Uninstall(nil, &out, &errs); code != 0 {
		t.Fatalf("uninstall exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("binary err = %v, want not exist", err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatalf("unit err = %v, want not exist", err)
	}
	if b, err := os.ReadFile(cfg); err != nil || string(b) != "marker" {
		t.Fatalf("config = %q, err %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(data, "keep")); err != nil || string(b) != "marker" {
		t.Fatalf("data = %q, err %v", b, err)
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "disable --now nusallm.service") {
		t.Fatalf("calls:\n%s", joined)
	}
	if !strings.Contains(out.String(), "kept config "+cfg) || !strings.Contains(out.String(), "kept data "+data) {
		t.Fatalf("stdout = %q", out.String())
	}
}
