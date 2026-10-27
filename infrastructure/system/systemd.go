//go:build linux

package system

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

// systemd is the Service over `systemctl --user` — user units under
// ~/.config/systemd/user, the common case on mainstream distros.
type systemd struct{}

func (systemd) Name() string { return "systemd user service" }

// execCommand runs a host program. Tests replace it so the adapter
// never talks to the real user systemd bus.
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

func systemctlUser(args ...string) ([]byte, int, error) {
	return execCommand("systemctl", append([]string{"--user"}, args...)...)
}

func busDown(text string) bool {
	return strings.Contains(text, "Failed to connect to bus") ||
		strings.Contains(text, "Failed to connect to user scope bus") ||
		strings.Contains(text, "not been booted with systemd")
}

// validName reports whether a registration identity is safe to render
// into unit filenames and pass as a systemctl argument — the portable
// set every planned manager accepts.
func validName(name string) bool {
	// A leading dash would read as a systemctl flag; "." and ".." are
	// path segments. The charset keeps the name safe in unit filenames
	// and rendered files alike.
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "-") {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool {
		return r != '_' && r != '-' && r != '.' && r != '@' &&
			!(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9')
	}) < 0
}

func errBadName(name string) error {
	return fmt.Errorf("systemd: invalid service name %q", name)
}

func (systemd) unitName(name string) string { return name + ".service" }

func (systemd) unitPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Abs(filepath.Join(dir, "systemd", "user"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(home, ".config", "systemd", "user"))
}

// Available probes the user bus — the runtime readiness check behind
// every mutating verb. A degraded-but-answering manager still counts.
func (systemd) Available() error {
	out, _, err := systemctlUser("is-system-running")
	if err == nil && !busDown(string(out)) {
		return nil
	}
	reason := strings.TrimSpace(string(out))
	if err != nil {
		reason = err.Error()
	}
	return fmt.Errorf("user systemd is unavailable (%s)", reason)
}

// Register writes or rewrites the unit, enables it for autostart, and
// starts it now — restarting when it was already running so an update
// picks up a new binary.
func (s systemd) Register(spec Spec) error {
	if !validName(spec.Name) {
		return errBadName(spec.Name)
	}
	if spec.Exec == "" {
		return fmt.Errorf("systemd: Spec.Exec is required")
	}
	if err := s.Available(); err != nil {
		return err
	}
	wasActive := s.Active(spec.Name)
	// Render before touching the filesystem — a rejected spec must not
	// leave half-created state behind.
	text, err := s.render(spec)
	if err != nil {
		return err
	}
	dir, err := s.unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(spec.WorkingDir, 0o755); err != nil {
		return err
	}
	unitPath := filepath.Join(dir, s.unitName(spec.Name))
	if err := writeFileAtomic(unitPath, []byte(text), 0o644); err != nil {
		return err
	}
	if err := s.run("daemon-reload"); err != nil {
		return err
	}
	// Heal a StartLimit wedge the same way Start does: a service that
	// crash-looped on the old binary would otherwise refuse the enable
	// --now below and turn every update into a manual stop+start.
	_, _, _ = systemctlUser("reset-failed", s.unitName(spec.Name))
	if err := s.run("enable", "--now", s.unitName(spec.Name)); err != nil {
		return err
	}
	if wasActive {
		if err := s.run("restart", s.unitName(spec.Name)); err != nil {
			return err
		}
	}
	return nil
}

// Deregister stops and removes the unit. A dead bus cannot stop the
// service but never blocks removing the file — the note reports the
// degradation. A missing unit is success.
func (s systemd) Deregister(name string) (string, error) {
	if !validName(name) {
		return "", errBadName(name)
	}
	dir, err := s.unitPath()
	if err != nil {
		return "", err
	}
	unitPath := filepath.Join(dir, s.unitName(name))
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	note := ""
	out, code, runErr := systemctlUser("disable", "--now", s.unitName(name))
	down := runErr != nil || busDown(string(out))
	var disableErr error
	if down {
		reason := strings.TrimSpace(string(out))
		if runErr != nil {
			reason = runErr.Error()
		}
		note = fmt.Sprintf("user systemd is unavailable (%s); removed the unit file anyway", reason)
	} else if code != 0 {
		// A live-bus disable failure does not stop the cleanup: an
		// enabled unit file left behind would autostart next login.
		// Remove what can be removed, reload, then report.
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		disableErr = fmt.Errorf("systemctl --user disable --now %s: %s", s.unitName(name), msg)
	}
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if !down {
		if err := s.run("daemon-reload"); err != nil {
			return "", err
		}
	}
	if disableErr != nil {
		return "", disableErr
	}
	return note, nil
}

// Start heals the wedge a repeated-too-quickly failure leaves behind —
// reset-failed clears the StartLimit counter before the start, where a
// bare start would be refused. Failure points at the journal because
// systemctl's own line ("control process exited") never names the
// cause.
func (s systemd) Start(name string) error {
	if !validName(name) {
		return errBadName(name)
	}
	_, _, _ = systemctlUser("reset-failed", s.unitName(name))
	if err := s.run("start", s.unitName(name)); err != nil {
		return fmt.Errorf("%w — see `journalctl --user -e -u %s`", err, s.unitName(name))
	}
	return nil
}

func (s systemd) Stop(name string) error {
	if !validName(name) {
		return errBadName(name)
	}
	return s.run("stop", s.unitName(name))
}

// Restart queues a restart of the unit and returns without waiting for it.
// --no-block matters: a caller running inside the unit is stopped by its own
// restart, so a blocking call could never report back.
func (s systemd) Restart(name string) error {
	if !validName(name) {
		return errBadName(name)
	}
	return s.run("--no-block", "restart", s.unitName(name))
}

// Status renders the native report for display and reads the machine
// ActiveState for the portable State — parsing the human "Active:" line
// would break under a translated locale. A dead bus or a failed query
// is an error, never a silent "stopped".
func (s systemd) Status(name string) (string, State, error) {
	if !validName(name) {
		return "", StateUnknown, errBadName(name)
	}
	out, code, err := systemctlUser("status", "--no-pager", s.unitName(name))
	report := strings.TrimRight(string(out), "\n")
	if err != nil {
		return report, StateUnknown, err
	}
	if busDown(string(out)) {
		return report, StateUnknown, fmt.Errorf("user systemd is unavailable (%s)", strings.TrimSpace(string(out)))
	}
	if code == 0 {
		return report, StateRunning, nil
	}
	active, _, err := systemctlUser("show", s.unitName(name), "--value", "-p", "ActiveState")
	if err != nil {
		return report, StateUnknown, err
	}
	if busDown(string(active)) {
		return report, StateUnknown, fmt.Errorf("user systemd is unavailable (%s)", strings.TrimSpace(string(active)))
	}
	if strings.TrimSpace(string(active)) == "failed" {
		return report, StateFailed, nil
	}
	return report, StateStopped, nil
}

func (s systemd) Active(name string) bool {
	if !validName(name) {
		return false
	}
	_, code, err := systemctlUser("is-active", "--quiet", s.unitName(name))
	return err == nil && code == 0
}

// ActiveElsewhere distinguishes "the unit is up" from "the unit is up
// and this process is not it": INVOCATION_ID alone cannot tell them
// apart — every systemd-launched process carries one, session scopes
// included. Only this process's invocation id matching the unit's own
// means this process IS the service.
func (s systemd) ActiveElsewhere(name string) bool {
	if !validName(name) || !s.Active(name) {
		return false
	}
	id := os.Getenv("INVOCATION_ID")
	if id == "" {
		return true
	}
	out, _, err := systemctlUser("show", s.unitName(name), "--value", "-p", "InvocationID")
	return err == nil && strings.TrimSpace(string(out)) != id
}

// PersistAcrossLogout maps to loginctl enable-linger — the user's
// services keep running and autostart without a session.
func (s systemd) PersistAcrossLogout() error {
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
		return nil
	}
	reason := strings.TrimSpace(string(out))
	if err != nil {
		reason = err.Error()
	}
	target := name
	if target == "" {
		target = `"$USER"`
	}
	return fmt.Errorf("linger was not enabled: %s — enable it with: sudo loginctl enable-linger %s", reason, target)
}

// run wraps a systemctl call into an error carrying its output.
func (systemd) run(args ...string) error {
	out, code, err := systemctlUser(args...)
	if err != nil {
		return err
	}
	if code != 0 {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// render produces the user unit. Paths and env values are absolute and
// quoted so the unit does not depend on the manager's PATH, the
// directory it was installed from, or the caller's environment.
func (s systemd) render(spec Spec) (string, error) {
	execStart, err := systemdEnvArg(spec.Exec)
	if err != nil {
		return "", err
	}
	for _, arg := range spec.Args {
		quoted, err := systemdEnvArg(arg)
		if err != nil {
			return "", err
		}
		execStart += " " + quoted
	}
	work, err := systemdArg(spec.WorkingDir)
	if err != nil {
		return "", err
	}
	envKeys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	envLines := make([]string, 0, len(envKeys))
	for _, k := range envKeys {
		v, err := systemdEnvArg(k + "=" + spec.Env[k])
		if err != nil {
			return "", err
		}
		envLines = append(envLines, "Environment="+v)
	}
	desc, err := systemdArg(spec.Description)
	if err != nil {
		return "", err
	}
	lines := []string{
		"[Unit]",
		"Description=" + desc,
		"After=network-online.target",
		"Wants=network-online.target",
		"StartLimitIntervalSec=60",
		"StartLimitBurst=5",
		"",
		"[Service]",
		"ExecStart=" + execStart,
		"WorkingDirectory=" + work,
	}
	lines = append(lines, envLines...)
	lines = append(lines,
		"Restart=on-failure",
		"RestartSec=5",
		"TimeoutStopSec=20",
		"KillMode=control-group",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	)
	return strings.Join(lines, "\n"), nil
}

// systemdArg escapes specifier expansion (%% for a literal %) so a
// value stays literal in any unit setting, rejects line breaks, and
// quotes when systemd's own syntax requires it — spaces, quotes,
// backslashes.
func systemdArg(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("systemd value contains a line break")
	}
	value = strings.ReplaceAll(value, "%", "%%")
	if !strings.ContainsAny(value, " \t\"\\") {
		return value, nil
	}
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`, nil
}

// systemdEnvArg is systemdArg plus $$ escaping — the literal-$ escape
// for settings that perform environment substitution (ExecStart,
// Environment). WorkingDirectory and Description do not expand $, so
// escaping there would show the doubled dollar literally.
func systemdEnvArg(value string) (string, error) {
	return systemdArg(strings.ReplaceAll(value, "$", "$$"))
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
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
