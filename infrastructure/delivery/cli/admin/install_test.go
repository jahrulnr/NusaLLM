package admin

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/system"
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

// fakeService is the system.Service port scripted per test — the admin
// layer is exercised through the interface, the way main wires it.
type fakeService struct {
	availErr   error
	registered system.Spec
	regErr     error
	deregName  string
	deregNote  string
	deregErr   error
	startCalls int
	startErr   error
	stopCalls  int
	stopErr    error
	isActive   bool
	elsewhere  bool
	statusOut  string
	state      system.State
	statusErr  error
	lingerErr  error
	calls      []string
}

func (f *fakeService) Name() string { return "test service" }
func (f *fakeService) Available() error {
	f.calls = append(f.calls, "available")
	return f.availErr
}
func (f *fakeService) Register(spec system.Spec) error {
	f.calls = append(f.calls, "register")
	f.registered = spec
	return f.regErr
}
func (f *fakeService) Deregister(name string) (string, error) {
	f.calls = append(f.calls, "deregister "+name)
	f.deregName = name
	return f.deregNote, f.deregErr
}
func (f *fakeService) Start(name string) error {
	f.calls = append(f.calls, "start "+name)
	f.startCalls++
	return f.startErr
}
func (f *fakeService) Stop(name string) error {
	f.calls = append(f.calls, "stop "+name)
	f.stopCalls++
	return f.stopErr
}
func (f *fakeService) Status(name string) (string, system.State, error) {
	f.calls = append(f.calls, "status "+name)
	return f.statusOut, f.state, f.statusErr
}
func (f *fakeService) Active(string) bool          { return f.isActive }
func (f *fakeService) ActiveElsewhere(string) bool { return f.elsewhere }
func (f *fakeService) PersistAcrossLogout() error {
	f.calls = append(f.calls, "linger")
	return f.lingerErr
}

// writeServeConfig places a user config carrying the given port under
// the isolated HOME — serveControl reads the port from it via
// listenPort.
func writeServeConfig(t *testing.T, home string, port int) {
	t.Helper()
	cfg := filepath.Join(home, ".config", "nusallm", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("{\"server\":{\"port\":%d}}\n", port)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// heldPort opens a loopback listener and keeps it — the port counts
// as in-use until the caller closes the returned listener.
func heldPort(t *testing.T) (int, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln.Addr().(*net.TCPAddr).Port, ln
}

// freePort returns a loopback port that answered "refused" a moment
// ago — good enough to exercise the not-in-use branch.
func freePort(t *testing.T) int {
	t.Helper()
	port, ln := heldPort(t)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
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

func TestServeStartRefusesHeldPort(t *testing.T) {
	home := isolateInstallHome(t)
	port, ln := heldPort(t)
	defer ln.Close()
	writeServeConfig(t, home, port)
	svc := &fakeService{}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"start"}, &out, &errs, svc); code == 0 {
		t.Fatal("start on a held port succeeded")
	}
	if !strings.Contains(errs.String(), "already in use") {
		t.Fatalf("stderr = %q, want the port conflict named", errs.String())
	}
	if svc.startCalls != 0 {
		t.Fatalf("start reached the manager with a held port: %v", svc.calls)
	}
}

func TestServeStartDelegates(t *testing.T) {
	home := isolateInstallHome(t)
	writeServeConfig(t, home, freePort(t))
	svc := &fakeService{}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"start"}, &out, &errs, svc); code != 0 {
		t.Fatalf("start exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if svc.startCalls != 1 {
		t.Fatalf("start did not reach the manager: %v", svc.calls)
	}
}

func TestServeStopDelegatesWithoutConfig(t *testing.T) {
	isolateInstallHome(t)
	// stop must not depend on the config path resolving — a broken
	// config cannot be allowed to block stopping the service.
	svc := &fakeService{}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"stop"}, &out, &errs, svc); code != 0 {
		t.Fatalf("stop exit %d\n%s", code, errs.String())
	}
	if svc.stopCalls != 1 {
		t.Fatalf("stop did not reach the manager: %v", svc.calls)
	}
}

func TestServeStopSurfacesImplError(t *testing.T) {
	isolateInstallHome(t)
	svc := &fakeService{stopErr: errors.New("systemctl --user stop: boom")}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"stop"}, &out, &errs, svc); code == 0 {
		t.Fatal("stop succeeded against a failing manager")
	}
	if !strings.Contains(errs.String(), "boom") {
		t.Fatalf("stderr = %q", errs.String())
	}
}

func TestServeStartFailureNamesHeldPort(t *testing.T) {
	home := isolateInstallHome(t)
	// The service believes itself active (so the preflight delegates),
	// start still fails, and the port turns out held — the conflict
	// message beats the raw manager error.
	port, ln := heldPort(t)
	defer ln.Close()
	writeServeConfig(t, home, port)
	svc := &fakeService{isActive: true, startErr: errors.New("systemctl --user start nusallm.service: boom")}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"start"}, &out, &errs, svc); code == 0 {
		t.Fatal("start succeeded against a failing manager")
	}
	if !strings.Contains(errs.String(), "already in use") {
		t.Fatalf("stderr = %q, want the port conflict named", errs.String())
	}
}

func TestServeStartDelegatesWhenServiceActive(t *testing.T) {
	home := isolateInstallHome(t)
	// The service's own listener holds the port — an active service
	// must still delegate start (a no-op), not be refused as a
	// foreign port conflict.
	port, ln := heldPort(t)
	defer ln.Close()
	writeServeConfig(t, home, port)
	svc := &fakeService{isActive: true}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"start"}, &out, &errs, svc); code != 0 {
		t.Fatalf("start exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if svc.startCalls != 1 {
		t.Fatalf("active service start did not reach the manager: %v", svc.calls)
	}
}

func TestServeStartFailureSurfacesImplError(t *testing.T) {
	home := isolateInstallHome(t)
	writeServeConfig(t, home, freePort(t))
	svc := &fakeService{startErr: errors.New("start failed — see `journalctl -u x`")}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"start"}, &out, &errs, svc); code == 0 {
		t.Fatal("start succeeded against a failing manager")
	}
	if !strings.Contains(errs.String(), "journalctl") {
		t.Fatalf("stderr = %q, want the impl's own hint passed through", errs.String())
	}
}

func TestServeInstallWarnsWhenManagerUnavailable(t *testing.T) {
	home := isolateInstallHome(t)
	// serve install is the full install — the CLI copy stands even when
	// no manager exists, so the service gap degrades to a warning.
	svc := &fakeService{availErr: errors.New("user systemd is unavailable (no bus)")}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"install"}, &out, &errs, svc); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "nusallm")); err != nil {
		t.Fatal(err)
	}
	for _, c := range svc.calls {
		if c == "register" {
			t.Fatalf("register ran without a manager: %v", svc.calls)
		}
	}
	if !strings.Contains(errs.String(), "service was not installed") {
		t.Fatalf("stderr = %q", errs.String())
	}
}

func TestInstallLingerUnsupportedIsNote(t *testing.T) {
	isolateInstallHome(t)
	svc := &fakeService{lingerErr: fmt.Errorf("%w: test service", system.ErrUnsupported)}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"install", "--linger"}, &out, &errs, svc); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if !strings.Contains(errs.String(), "--linger skipped") {
		t.Fatalf("stderr = %q, want the unsupported note", errs.String())
	}
}

func TestServeStatusMapsStateToExitCode(t *testing.T) {
	home := isolateInstallHome(t)
	writeServeConfig(t, home, freePort(t))
	for _, tc := range []struct {
		state    system.State
		statusEr error
		wantCode int
	}{
		{system.StateRunning, nil, 0},
		{system.StateStopped, nil, 3},
		{system.StateFailed, nil, 3},
		{system.StateUnknown, errors.New("bus gone"), 1},
	} {
		t.Run(fmt.Sprint(tc.state), func(t *testing.T) {
			svc := &fakeService{statusOut: "report", state: tc.state, statusErr: tc.statusEr}
			var out, errs bytes.Buffer
			if code := ServeAdmin([]string{"status"}, &out, &errs, svc); code != tc.wantCode {
				t.Fatalf("status exit %d, want %d", code, tc.wantCode)
			}
		})
	}
}

func TestServeUninstallDeregisters(t *testing.T) {
	home := isolateInstallHome(t)
	bin := filepath.Join(home, ".local", "bin", "nusallm")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := &fakeService{deregNote: "bus down; removed the file anyway"}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"uninstall"}, &out, &errs, svc); code != 0 {
		t.Fatalf("uninstall exit %d", code)
	}
	if svc.deregName != "nusallm" {
		t.Fatalf("deregister called with %q, want the CLI name", svc.deregName)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("binary still present after uninstall: %v", err)
	}
	if !strings.Contains(errs.String(), "removed the file anyway") {
		t.Fatalf("stderr = %q, want the impl's note surfaced", errs.String())
	}
}

func TestInstallCopiesExecutable(t *testing.T) {
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

func TestInstallRegistersWebService(t *testing.T) {
	home := isolateInstallHome(t)
	svc := &fakeService{}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"install"}, &out, &errs, svc); code != 0 {
		t.Fatalf("install exit %d\nstdout:\n%s\nstderr:\n%s", code, out.String(), errs.String())
	}
	bin := filepath.Join(home, ".local", "bin", "nusallm")
	if _, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".config", "nusallm", "config.json")
	spec := svc.registered
	if spec.Name != "nusallm" {
		t.Fatalf("spec.Name = %q", spec.Name)
	}
	if spec.Exec != bin || len(spec.Args) != 1 || spec.Args[0] != "serve" {
		t.Fatalf("spec.Exec/Args = %q %v, want %s serve", spec.Exec, spec.Args, bin)
	}
	if spec.Env["CONFIG_PATH"] != cfg {
		t.Fatalf("spec.Env = %v, want CONFIG_PATH=%s", spec.Env, cfg)
	}
	// The daemon bakes a PATH into the unit so agent exec sees the
	// user's toolchain — the installer's PATH led by stable user bins.
	path := spec.Env["PATH"]
	for _, want := range []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "go", "bin"),
	} {
		if !strings.Contains(path, want) {
			t.Fatalf("spec.Env PATH %q missing %s", path, want)
		}
	}
	if host := os.Getenv("PATH"); host != "" && !strings.Contains(path, host) {
		t.Fatalf("spec.Env PATH %q lost the installer PATH %q", path, host)
	}
	data := filepath.Join(home, ".local", "share", "nusallm")
	if spec.WorkingDir != data {
		t.Fatalf("spec.WorkingDir = %q, want %q", spec.WorkingDir, data)
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
	if !strings.Contains(errs.String(), "not on PATH") {
		t.Fatalf("stderr = %q, want PATH hint", errs.String())
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:10998") {
		t.Fatalf("stdout = %q, want WebUI url", out.String())
	}
}

func TestInstallReportsStartVsRestart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		active  bool
		wantOut string
	}{
		{"fresh install starts", false, "service started"},
		{"update restarts", true, "service restarted onto the new binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateInstallHome(t)
			svc := &fakeService{isActive: tc.active}
			var out, errs bytes.Buffer
			if code := ServeAdmin([]string{"install"}, &out, &errs, svc); code != 0 {
				t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Fatalf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
		})
	}
}

func TestInstallSkipsServiceWhenManagerUnavailable(t *testing.T) {
	home := isolateInstallHome(t)
	svc := &fakeService{availErr: errors.New("user systemd is unavailable (no bus)")}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"install"}, &out, &errs, svc); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "nusallm")); err != nil {
		t.Fatal(err)
	}
	for _, c := range svc.calls {
		if c == "register" {
			t.Fatalf("register ran without a manager: %v", svc.calls)
		}
	}
	if !strings.Contains(errs.String(), "service was not installed") {
		t.Fatalf("stderr = %q", errs.String())
	}
}

func TestInstallNoServiceSkipsManager(t *testing.T) {
	home := isolateInstallHome(t)
	svc := &fakeService{}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"install", "--no-service"}, &out, &errs, svc); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "nusallm")); err != nil {
		t.Fatal(err)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("manager touched with --no-service: %v", svc.calls)
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
	svc := &fakeService{}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"install"}, &out, &errs, svc); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("config rewritten:\n%s", got)
	}
	if svc.registered.WorkingDir != home {
		t.Fatalf("spec.WorkingDir = %q, want home %q (relative storage.dir)", svc.registered.WorkingDir, home)
	}
}

func TestInstallLingerFailureStillSucceeds(t *testing.T) {
	isolateInstallHome(t)
	svc := &fakeService{lingerErr: errors.New("linger was not enabled: denied — sudo loginctl enable-linger $USER")}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"install", "--linger"}, &out, &errs, svc); code != 0 {
		t.Fatalf("install exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if !strings.Contains(errs.String(), "sudo loginctl enable-linger") {
		t.Fatalf("stderr = %q", errs.String())
	}
	if !strings.Contains(out.String(), "http://127.0.0.1:10998") {
		t.Fatalf("stdout = %q", out.String())
	}
	for _, c := range svc.calls {
		if c == "linger" {
			return
		}
	}
	t.Fatalf("PersistAcrossLogout never called: %v", svc.calls)
}

func TestUninstallKeepsConfigAndData(t *testing.T) {
	home := isolateInstallHome(t)
	cfg := filepath.Join(home, ".config", "nusallm", "config.json")
	data := filepath.Join(home, ".local", "share", "nusallm")
	bin := filepath.Join(home, ".local", "bin", "nusallm")
	for _, path := range []string{cfg, filepath.Join(data, "keep"), bin} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("marker"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc := &fakeService{}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"uninstall"}, &out, &errs, svc); code != 0 {
		t.Fatalf("uninstall exit %d\n%s\n%s", code, out.String(), errs.String())
	}
	if svc.deregName != "nusallm" {
		t.Fatalf("deregister = %q, want the CLI name", svc.deregName)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("binary err = %v, want not exist", err)
	}
	if b, err := os.ReadFile(cfg); err != nil || string(b) != "marker" {
		t.Fatalf("config = %q, err %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(data, "keep")); err != nil || string(b) != "marker" {
		t.Fatalf("data = %q, err %v", b, err)
	}
	if !strings.Contains(out.String(), "kept config "+cfg) || !strings.Contains(out.String(), "kept data "+data) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestUninstallSurfacesDeregisterError(t *testing.T) {
	isolateInstallHome(t)
	svc := &fakeService{deregErr: errors.New("systemctl --user disable --now: boom")}
	var out, errs bytes.Buffer
	if code := ServeAdmin([]string{"uninstall"}, &out, &errs, svc); code == 0 {
		t.Fatal("uninstall succeeded against a failing deregister")
	}
	if !strings.Contains(errs.String(), "boom") {
		t.Fatalf("stderr = %q", errs.String())
	}
}
