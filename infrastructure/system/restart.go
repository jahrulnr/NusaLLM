package system

// Restarter brings the running WebUI onto a freshly installed executable. A
// process the host service manager owns is restarted through the manager. A
// foreground run has no manager, so it replaces itself in place with the
// installed file.
type Restarter struct {
	svc    Service
	name   string
	path   string
	argv   []string
	reexec func(path string, argv []string) error
}

// NewRestarter binds the restart to the registration name, the installed
// executable, and the argv a replacement starts with.
func NewRestarter(svc Service, name, path string, argv []string) Restarter {
	return Restarter{svc: svc, name: name, path: path, argv: argv, reexec: Reexec}
}

// Mode names the path a restart takes: "service" when the manager owns this
// process, "process" for a foreground run.
func (r Restarter) Mode() string {
	if r.svc.Active(r.name) && !r.svc.ActiveElsewhere(r.name) {
		return "service"
	}
	return "process"
}

// Restart performs the restart for mode. The "process" path does not return on
// success: the process it replaces is this one.
func (r Restarter) Restart(mode string) error {
	if mode == "service" {
		return r.svc.Restart(r.name)
	}
	return r.reexec(r.path, r.argv)
}
