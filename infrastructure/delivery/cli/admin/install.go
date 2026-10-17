package admin

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
)

const serviceName = "nusallm.service"

// execCommand runs a host program. Tests replace it so an install never
// talks to the real user systemd bus.
var execCommand = func(name string, args ...string) ([]byte, int, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitCode(), nil
	}
	return out, -1, err
}

const installUsage = `Usage: ` + CLIName + ` install [--no-service] [--linger]

Copy this executable to ~/.local/bin/` + CLIName + ` and run the WebUI
as a systemd user service. The service starts at login. The CLI and the
WebUI are this same binary; the service runs it with "serve".

Flags:
  --no-service   copy the CLI only
  --linger       also run loginctl enable-linger (service survives logout)
`

const uninstallUsage = `Usage: ` + CLIName + ` uninstall

Remove the CLI binary and the systemd user service.
Config and data directories are left in place.
`

const serveAdminUsage = `Usage: ` + CLIName + ` serve install [--linger]
       ` + CLIName + ` serve start|stop|status|uninstall

Manage the WebUI systemd user service. The service runs the binary
installed at ~/.local/bin/` + CLIName + `.
`

// IsServeAdmin reports argv that manages the WebUI service. Flags such
// as --port stay with the foreground server.
func IsServeAdmin(arg string) bool {
	switch arg {
	case "install", "start", "stop", "status", "uninstall":
		return true
	default:
		return false
	}
}

// Install copies the running executable into ~/.local/bin and enables the
// WebUI user service. --no-service stops after the copy. A missing user
// systemd bus still leaves the CLI installed.
func Install(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noService := fs.Bool("no-service", false, "copy the CLI only")
	linger := fs.Bool("linger", false, "keep the user service running after logout")
	fs.Usage = func() { fmt.Fprint(stderr, installUsage) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", CLIName, fs.Arg(0))
		fmt.Fprint(stderr, installUsage)
		return 2
	}
	configPath, created, err := ensureInstalledConfig()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if created {
		fmt.Fprintf(stderr, "%s: no config found; wrote defaults to %s\n", CLIName, configPath)
	}
	bin, err := userBinPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if _, err := installBinary(bin); err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: installed CLI at %s\n", CLIName, bin)
	if !dirOnPath(filepath.Dir(bin)) {
		fmt.Fprintf(stderr, "%s: %s is not on PATH\nexport PATH=%q\n", CLIName, filepath.Dir(bin), filepath.Dir(bin)+":$PATH")
	}
	if *noService {
		if *linger {
			fmt.Fprintf(stderr, "%s: --linger has no effect with --no-service\n", CLIName)
		}
		return 0
	}
	return enableService(stdout, stderr, *linger)
}

// Uninstall removes the user service and the copied binary. Config and
// data stay where they are.
func Uninstall(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, uninstallUsage)
		return 0
	}
	if len(args) != 0 {
		fmt.Fprint(stderr, uninstallUsage)
		return 2
	}
	code := removeService(stderr)
	bin, err := userBinPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		if code == 0 {
			code = 1
		}
	} else if removed, rmErr := removeInstalledBinary(bin); rmErr != nil {
		fmt.Fprintln(stderr, CLIName+":", rmErr)
		if code == 0 {
			code = 1
		}
	} else if removed {
		fmt.Fprintf(stdout, "%s: removed %s\n", CLIName, bin)
	}
	cfg, cfgErr := userConfigPath()
	data, dataErr := userDataDir()
	if cfgErr == nil {
		fmt.Fprintf(stdout, "%s: kept config %s\n", CLIName, cfg)
	}
	if dataErr == nil {
		fmt.Fprintf(stdout, "%s: kept data %s\n", CLIName, data)
	}
	return code
}

// ServeAdmin handles `nusallm serve install|start|stop|status|uninstall`.
func ServeAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, serveAdminUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, serveAdminUsage)
		return 0
	case "install":
		return serveInstall(args[1:], stdout, stderr)
	case "start", "stop", "status":
		if len(args) != 1 {
			fmt.Fprint(stderr, serveAdminUsage)
			return 2
		}
		return serveControl(args[0], stdout, stderr)
	case "uninstall":
		if len(args) != 1 {
			fmt.Fprint(stderr, serveAdminUsage)
			return 2
		}
		return removeService(stderr)
	default:
		fmt.Fprintf(stderr, "%s: unknown serve command %q\n", CLIName, args[0])
		fmt.Fprint(stderr, serveAdminUsage)
		return 2
	}
}

func serveInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	linger := fs.Bool("linger", false, "keep the user service running after logout")
	fs.Usage = func() { fmt.Fprint(stderr, serveAdminUsage) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n", CLIName, fs.Arg(0))
		fmt.Fprint(stderr, serveAdminUsage)
		return 2
	}
	bin, err := userBinPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	fi, err := os.Stat(bin)
	if err != nil || !fi.Mode().IsRegular() {
		fmt.Fprintf(stderr, "%s: CLI is not installed at %s — run %s install\n", CLIName, bin, CLIName)
		return 1
	}
	configPath, created, err := ensureInstalledConfig()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if created {
		fmt.Fprintf(stderr, "%s: no config found; wrote defaults to %s\n", CLIName, configPath)
	}
	return enableService(stdout, stderr, *linger)
}

func serveControl(action string, stdout, stderr io.Writer) int {
	if action == "status" {
		configPath, err := userConfigPath()
		if err != nil {
			fmt.Fprintln(stderr, CLIName+":", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: WebUI available at http://127.0.0.1:%d\n", CLIName, listenPort(configPath))
	}
	out, code, err := systemctlUser(action, serviceName)
	if len(out) > 0 {
		_, _ = stdout.Write(out)
		if !strings.HasSuffix(string(out), "\n") {
			fmt.Fprintln(stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	return code
}

// enableService writes the user unit and starts it. A user bus that is
// not there is success with a note: the CLI install must still stand on
// a machine without systemd.
func enableService(stdout, stderr io.Writer, linger bool) int {
	probe, _, err := systemctlUser("is-system-running")
	if err != nil || busDown(string(probe)) {
		reason := strings.TrimSpace(string(probe))
		if err != nil {
			reason = err.Error()
		}
		fmt.Fprintf(stderr, "%s: user systemd is unavailable (%s); service was not installed\n", CLIName, reason)
		return 0
	}
	_, activeCode, activeErr := systemctlUser("is-active", "--quiet", serviceName)
	wasActive := activeErr == nil && activeCode == 0

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	configPath, err := userConfigPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	bin, err := userBinPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	work := serviceWorkingDir(home, configPath)
	if err := os.MkdirAll(work, 0o755); err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	text, err := renderUserUnit(bin, work, configPath)
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	unitPath, err := systemdUnitPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if err := writeFileAtomic(unitPath, []byte(text), 0o644); err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if code := runSystemctl(stderr, "daemon-reload"); code != 0 {
		return code
	}
	if code := runSystemctl(stderr, "enable", "--now", serviceName); code != 0 {
		return code
	}
	if wasActive {
		if code := runSystemctl(stderr, "restart", serviceName); code != 0 {
			return code
		}
	}
	fmt.Fprintf(stdout, "%s: WebUI available at http://127.0.0.1:%d (starts at login)\n", CLIName, listenPort(configPath))
	if linger {
		enableLinger(stderr)
	}
	return 0
}

func runSystemctl(stderr io.Writer, args ...string) int {
	out, code, err := systemctlUser(args...)
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if code != 0 {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		fmt.Fprintf(stderr, "%s: systemctl --user %s: %s\n", CLIName, strings.Join(args, " "), msg)
		return code
	}
	return 0
}

func enableLinger(stderr io.Writer) {
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	args := []string{"enable-linger"}
	if name != "" {
		args = append(args, name)
	}
	out, code, err := execCommand("loginctl", args...)
	if err == nil && code == 0 {
		return
	}
	reason := strings.TrimSpace(string(out))
	if err != nil {
		reason = err.Error()
	}
	fmt.Fprintf(stderr, "%s: linger was not enabled: %s\n", CLIName, reason)
	if name == "" {
		fmt.Fprintf(stderr, "%s: enable it with: sudo loginctl enable-linger \"$USER\"\n", CLIName)
		return
	}
	fmt.Fprintf(stderr, "%s: enable it with: sudo loginctl enable-linger %s\n", CLIName, name)
}

func removeService(stderr io.Writer) int {
	unitPath, err := systemdUnitPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return 0
	} else if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	out, code, runErr := systemctlUser("disable", "--now", serviceName)
	down := runErr != nil || busDown(string(out))
	if runErr != nil {
		fmt.Fprintf(stderr, "%s: user systemd is unavailable (%v); removed the unit file anyway\n", CLIName, runErr)
	} else if down {
		fmt.Fprintf(stderr, "%s: user systemd is unavailable (%s); removed the unit file anyway\n", CLIName, strings.TrimSpace(string(out)))
	} else if code != 0 {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		fmt.Fprintf(stderr, "%s: systemctl --user disable --now %s: %s\n", CLIName, serviceName, msg)
	}
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if !down {
		if out, code, err := systemctlUser("daemon-reload"); err != nil || code != 0 {
			msg := strings.TrimSpace(string(out))
			if err != nil {
				msg = err.Error()
			}
			if msg == "" {
				msg = fmt.Sprintf("exit %d", code)
			}
			fmt.Fprintf(stderr, "%s: systemctl --user daemon-reload: %s\n", CLIName, msg)
		}
	}
	if !down && code != 0 {
		return code
	}
	return 0
}

func systemctlUser(args ...string) ([]byte, int, error) {
	return execCommand("systemctl", append([]string{"--user"}, args...)...)
}

func busDown(text string) bool {
	return strings.Contains(text, "Failed to connect to bus") ||
		strings.Contains(text, "Failed to connect to user scope bus") ||
		strings.Contains(text, "not been booted with systemd")
}

func listenPort(configPath string) int {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return 10998
	}
	cfg, err := config.Validate(raw)
	if err != nil || cfg.Server.Port < 1 {
		return 10998
	}
	return cfg.Server.Port
}

// serviceWorkingDir is the unit WorkingDirectory. An absolute storage.dir
// is stable. A relative one stays relative to the home directory — pinning
// it under the data directory would nest ./data inside itself.
func serviceWorkingDir(home, configPath string) string {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return home
	}
	cfg, err := config.Validate(raw)
	if err != nil || !filepath.IsAbs(cfg.Storage.Dir) {
		return home
	}
	return cfg.Storage.Dir
}

// renderUserUnit writes a user unit that starts this same binary with
// "serve". Paths are absolute so the unit does not depend on the service
// manager's PATH or on the directory it was installed from.
func renderUserUnit(binary, workingDir, configPath string) (string, error) {
	execStart, err := systemdArg(binary)
	if err != nil {
		return "", err
	}
	work, err := systemdArg(workingDir)
	if err != nil {
		return "", err
	}
	env, err := systemdArg("CONFIG_PATH=" + configPath)
	if err != nil {
		return "", err
	}
	lines := []string{
		"[Unit]",
		"Description=NusaLLM",
		"After=network-online.target",
		"Wants=network-online.target",
		"StartLimitIntervalSec=60",
		"StartLimitBurst=5",
		"",
		"[Service]",
		"ExecStart=" + execStart + " serve",
		"WorkingDirectory=" + work,
		"Environment=" + env,
		"Restart=on-failure",
		"RestartSec=5",
		"TimeoutStopSec=20",
		"KillMode=control-group",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}
	return strings.Join(lines, "\n"), nil
}

func systemdArg(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("systemd value contains a line break")
	}
	if !strings.ContainsAny(value, " \t\"\\") {
		return value, nil
	}
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`, nil
}

// installBinary copies the running executable to dest. The same file
// (reinstall from the installed path) is left untouched. The temp file
// sits next to dest so the rename stays on one filesystem, and the
// running process keeps its old inode until it is restarted.
func installBinary(dest string) (bool, error) {
	src, err := os.Executable()
	if err != nil {
		return false, err
	}
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return false, err
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	if destInfo, statErr := os.Stat(dest); statErr == nil {
		srcInfo, err := os.Stat(src)
		if err != nil {
			return false, err
		}
		if os.SameFile(srcInfo, destInfo) {
			return false, nil
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".nusallm-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return false, err
	}
	return true, nil
}

func removeInstalledBinary(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file; left in place", path)
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

// ensureInstalledConfig creates the user config once. An existing file is
// not rewritten — server.port and storage.dir may already be choices.
// A new file gets an absolute storage.dir so the service and the CLI do
// not depend on the working directory.
func ensureInstalledConfig() (string, bool, error) {
	configPath, err := userConfigPath()
	if err != nil {
		return "", false, err
	}
	dataDir, err := userDataDir()
	if err != nil {
		return "", false, err
	}
	store := repoconfig.NewLocal()
	exists, err := store.Exists(configPath)
	if err != nil || exists {
		return configPath, false, err
	}
	cfg := config.NewConfig()
	cfg.Storage.Dir = dataDir
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", false, err
	}
	if err := store.WriteAtomic(configPath, append(raw, '\n')); err != nil {
		return "", false, err
	}
	return configPath, true, nil
}

func userConfigPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Abs(filepath.Join(dir, "nusallm", "config.json"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(home, ".config", "nusallm", "config.json"))
}

func userDataDir() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Abs(filepath.Join(dir, "nusallm"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(home, ".local", "share", "nusallm"))
}

func userBinPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(home, ".local", "bin", CLIName))
}

func systemdUnitPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Abs(filepath.Join(dir, "systemd", "user", serviceName))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(home, ".config", "systemd", "user", serviceName))
}

func dirOnPath(dir string) bool {
	dir = filepath.Clean(dir)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == "" {
			continue
		}
		cleaned := filepath.Clean(entry)
		if !filepath.IsAbs(cleaned) {
			abs, err := filepath.Abs(cleaned)
			if err != nil {
				continue
			}
			cleaned = abs
		}
		if cleaned == dir {
			return true
		}
	}
	return false
}

func writeFileAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".unit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
