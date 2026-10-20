// Package system integrates the app with the host OS. Today that means
// the user-level service manager keeping the WebUI running — systemd
// --user on mainstream Linux; later supervisord, OpenRC user services,
// launchd, or the Windows Task Scheduler.
//
// Verbs on Service are intent-shaped ("register", "start"), never the
// vocabulary of one manager ("enable --now", "bootstrap"). Each
// implementation renders its native registration format, probes its
// own availability, and carries its own remediation text inside the
// errors it returns.
//
// Adding a manager: implement Service in a file named after the
// mechanism (systemd.go, supervisord.go, tasksched.go, launchd.go),
// tag it for the OSes it supports, and append it to the candidate list
// in the matching select_<os>.go. Several managers can coexist on one
// OS — candidate order is preference order; NUSALLM_SERVICE_BACKEND
// overrides selection explicitly.
package system

import (
	"errors"
	"fmt"
)

// BackendEnv names the env override for manager selection — a key from
// the platform's candidate list ("systemd", "supervisord", ...). An
// unknown or absent manager yields an unavailable Service rather than
// a silent fallback.
const BackendEnv = "NUSALLM_SERVICE_BACKEND"

// ErrUnsupported marks a verb the selected manager does not have —
// PersistAcrossLogout on a manager with no session concept, Register
// on a platform with no impl. Callers map it to a warning or a no-op.
var ErrUnsupported = errors.New("host service operation unsupported")

// Spec describes the program a service manager runs — manager-agnostic.
// Name is the registration identity; each implementation derives its
// native naming from it ("nusallm.service", "program:nusallm",
// "ai.nusallm.agent"). Implementations validate the fields they
// consume — an empty Exec or control characters must fail at the
// adapter, not inside the rendered file.
type Spec struct {
	Name        string
	Description string
	Exec        string            // absolute path to the binary
	Args        []string          // e.g. ["serve"]
	WorkingDir  string            // cwd for the managed process
	Env         map[string]string // extra environment for the managed process
}

// State is the portable running-state a manager maps its native status
// onto. Delivery decides what a state becomes — exit code, color, text.
type State int

const (
	StateUnknown State = iota
	StateStopped       // registered but not running, or not registered
	StateRunning
	StateFailed // the manager holds a failure record
)

// Service is a host-level background-service registration. Register is
// the only verb that sees a full Spec; the control verbs address the
// registration by its Name because that is the identity every manager
// keys on.
//
// Contracts implementations must honor:
//   - New selection is presence-only (PATH lookups, env reads — no
//     subprocess). Available is the runtime readiness probe
//     ("is the bus up"), called at point of use.
//   - Register writes or updates the registration, enables autostart,
//     starts the service now, and restarts it when it was already
//     running — install and update are the same call.
//   - Deregister is idempotent: a missing registration is success, and
//     an unreachable manager still removes the local artifacts it can —
//     the returned note reports anything degraded.
//   - Start heals stale failure state the manager may hold (a
//     StartLimit wedge) rather than surfacing it as a cryptic refusal.
//   - Status maps native states onto State; err is non-nil only when
//     the manager itself could not be queried.
//   - ActiveElsewhere reports the service running while this process
//     is not it — a held port's likely owner. Implementations answer
//     however they can identity-match: systemd compares the unit's
//     InvocationID with this process's INVOCATION_ID; managers without
//     an equivalent bake a marker env into the registration
//     (Environment=, environment=) and compare this process's value.
//     When no match is possible, returning false degrades to a generic
//     message — never claim the service holds something it might not.
type Service interface {
	Name() string
	Available() error
	Register(spec Spec) error
	Deregister(name string) (note string, err error)
	Start(name string) error
	Stop(name string) error
	Status(name string) (report string, state State, err error)
	Active(name string) bool
	ActiveElsewhere(name string) bool

	// PersistAcrossLogout keeps the service alive and autostarted
	// without a login session — systemd linger, a boot-triggered task.
	// Managers without the concept return ErrUnsupported; managers
	// that inherently persist (cron @reboot) return nil.
	PersistAcrossLogout() error
}

// unavailable is the Service for a platform or override with no usable
// implementation — New returns it rather than failing, so CLI-side
// paths that do not need a manager keep working.
type unavailable struct {
	name   string
	reason string
}

func (u unavailable) Name() string        { return u.name }
func (u unavailable) Available() error    { return fmt.Errorf("%s: %s", u.name, u.reason) }
func (u unavailable) Register(Spec) error { return u.Available() }
func (u unavailable) Deregister(string) (string, error) {
	return "", u.Available()
}
func (u unavailable) Start(string) error { return u.Available() }
func (u unavailable) Stop(string) error  { return u.Available() }
func (u unavailable) Status(string) (string, State, error) {
	return "", StateUnknown, u.Available()
}
func (u unavailable) Active(string) bool          { return false }
func (u unavailable) ActiveElsewhere(string) bool { return false }
func (u unavailable) PersistAcrossLogout() error {
	return fmt.Errorf("%w: %s", ErrUnsupported, u.name)
}
