package admin

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
	"github.com/jahrulnr/nusallm/infrastructure/system"
)

const serveAdminUsage = `Usage: ` + CLIName + ` serve [--port N]
       ` + CLIName + ` serve install [--no-service] [--linger]
       ` + CLIName + ` serve start|stop|status|uninstall

The serve family owns the WebUI: run it in the foreground, install it
for the current user, and manage the host service.

install copies this executable to ~/.local/bin/` + CLIName + ` and
registers the WebUI service with the host manager — the service runs
this same binary with "serve" and starts at login. Re-running install
updates the binary and restarts a running service onto it; install and
update are one command. uninstall removes the registration and the
copied binary; config and data stay.

Flags (install):
  --no-service   copy the CLI only
  --linger       keep the service running after logout (where supported)
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

// serveInstall copies the running executable into ~/.local/bin and
// registers the WebUI service with the host manager. --no-service stops
// after the copy. A missing or unsupported manager still leaves the CLI
// installed.
func serveInstall(args []string, stdout, stderr io.Writer, svc system.Service) int {
	fs := flag.NewFlagSet("serve install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noService := fs.Bool("no-service", false, "copy the CLI only")
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
	return installWebService(svc, *linger, stdout, stderr)
}

// serveUninstall removes the service registration and the copied
// binary. Config and data stay where they are.
func serveUninstall(args []string, stdout, stderr io.Writer, svc system.Service) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, serveAdminUsage)
		return 0
	}
	if len(args) != 0 {
		fmt.Fprint(stderr, serveAdminUsage)
		return 2
	}
	code := 0
	note, err := svc.Deregister(CLIName)
	if note != "" {
		fmt.Fprintf(stderr, "%s: %s\n", CLIName, note)
	}
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		code = 1
	}
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
func ServeAdmin(args []string, stdout, stderr io.Writer, svc system.Service) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, serveAdminUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, serveAdminUsage)
		return 0
	case "install":
		return serveInstall(args[1:], stdout, stderr, svc)
	case "start", "stop", "status":
		if len(args) != 1 {
			fmt.Fprint(stderr, serveAdminUsage)
			return 2
		}
		return serveControl(args[0], stdout, stderr, svc)
	case "uninstall":
		return serveUninstall(args[1:], stdout, stderr, svc)
	default:
		fmt.Fprintf(stderr, "%s: unknown serve command %q\n", CLIName, args[0])
		fmt.Fprint(stderr, serveAdminUsage)
		return 2
	}
}

func serveControl(action string, stdout, stderr io.Writer, svc system.Service) int {
	if action == "stop" {
		if err := svc.Stop(CLIName); err != nil {
			fmt.Fprintln(stderr, CLIName+":", err)
			return 1
		}
		return 0
	}
	// start and status report against the configured port; stop above
	// needs no config — a broken config path must not block stopping.
	configPath, err := userConfigPath()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	port := listenPort(configPath)
	if action == "status" {
		fmt.Fprintf(stdout, "%s: WebUI available at http://127.0.0.1:%d\n", CLIName, port)
		report, state, err := svc.Status(CLIName)
		if report != "" {
			fmt.Fprintln(stdout, report)
		}
		if err != nil {
			fmt.Fprintln(stderr, CLIName+":", err)
			return 1
		}
		if state == system.StateRunning {
			return 0
		}
		// Registered-but-not-running is not a query failure — LSB
		// convention reports it as 3.
		return 3
	}
	// A start against a held port crash-loops the service until the
	// manager's restart budget burns out — then every later start is
	// refused with no hint of the real conflict. Report it up front;
	// an already-running service still delegates so start stays
	// idempotent.
	if !svc.Active(CLIName) && portInUse(port) {
		fmt.Fprintln(stderr, portHeldMessage(port))
		return 1
	}
	if err := svc.Start(CLIName); err != nil {
		if portInUse(port) {
			fmt.Fprintln(stderr, portHeldMessage(port))
		} else {
			fmt.Fprintln(stderr, CLIName+":", err)
		}
		return 1
	}
	return 0
}

func portHeldMessage(port int) string {
	return fmt.Sprintf("%s: 127.0.0.1:%d is already in use — a foreground `nusallm serve`/`make serve` or another process may hold it", CLIName, port)
}

// portInUse reports whether a listener accepts on the loopback port —
// the cheapest way to explain a refused start or a failed bind.
func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func listenPort(configPath string) int {
	fallback := config.NewConfig().Server.Port
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fallback
	}
	cfg, err := config.Validate(raw)
	if err != nil || cfg.Server.Port < 1 {
		return fallback
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

// webServiceSpec describes the WebUI service for the host manager —
// the installed binary, the storage dir as cwd, the config path pinned
// in its environment. The registration identity is CLIName.
func webServiceSpec() (system.Spec, string, error) {
	bin, err := userBinPath()
	if err != nil {
		return system.Spec{}, "", err
	}
	configPath, err := userConfigPath()
	if err != nil {
		return system.Spec{}, "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return system.Spec{}, "", err
	}
	return system.Spec{
		Name:        CLIName,
		Description: "NusaLLM",
		Exec:        bin,
		Args:        []string{"serve"},
		WorkingDir:  serviceWorkingDir(home, configPath),
		Env:         map[string]string{"CONFIG_PATH": configPath},
	}, configPath, nil
}

// installWebService registers the WebUI service with the selected host
// manager. A manager that is not available degrades to a warning — the
// CLI copy already stands by the time this runs.
func installWebService(svc system.Service, linger bool, stdout, stderr io.Writer) int {
	spec, configPath, err := webServiceSpec()
	if err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	if err := svc.Available(); err != nil {
		fmt.Fprintf(stderr, "%s: %s; service was not installed\n", CLIName, err)
		return 0
	}
	// The report names what Register actually did: a running service is
	// restarted onto the new binary, a stopped one is started — install
	// and update are one command either way.
	restarting := svc.Active(CLIName)
	if err := svc.Register(spec); err != nil {
		fmt.Fprintln(stderr, CLIName+":", err)
		return 1
	}
	action := "service started"
	if restarting {
		action = "service restarted onto the new binary"
	}
	fmt.Fprintf(stdout, "%s: WebUI available at http://127.0.0.1:%d (%s, starts at login)\n", CLIName, listenPort(configPath), action)
	if linger {
		if err := svc.PersistAcrossLogout(); err != nil {
			if errors.Is(err, system.ErrUnsupported) {
				fmt.Fprintf(stderr, "%s: the %s cannot keep the service running after logout; --linger skipped\n", CLIName, svc.Name())
			} else {
				fmt.Fprintln(stderr, CLIName+":", err)
			}
		}
	}
	return 0
}
