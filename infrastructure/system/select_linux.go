//go:build linux

package system

import (
	"os"
	"os/exec"
)

// candidate pairs a BackendEnv key with the manager it selects and a
// cheap presence probe — LookPath-level, never a subprocess: New runs
// on CLI paths that may have nothing to do with the service.
type candidate struct {
	key     string
	service Service
	present func() bool
}

// lookPath is a seam so tests can stage PATH presence.
var lookPath = exec.LookPath

func binaryOnPath(name string) func() bool {
	return func() bool { _, err := lookPath(name); return err == nil }
}

// candidates is the preference order on systems where several managers
// coexist — native init wins over user-installed supervisors. Later
// managers (supervisord, openrc --user, dinit, runit, cron) slot in
// here as their files land; each stays a build-tag-free file because
// supervisord and cron are not linux-only in principle.
var candidates = []candidate{
	{"systemd", systemd{}, binaryOnPath("systemctl")},
}

// New picks the host service manager for this platform — never nil,
// never a subprocess. BackendEnv names a specific candidate; an
// unknown or absent one yields an unavailable Service so the caller's
// degradation policy decides, never a silent fallback to a different
// manager.
func New() Service {
	if key := os.Getenv(BackendEnv); key != "" {
		for _, c := range candidates {
			if c.key == key {
				if c.present() {
					return c.service
				}
				return unavailable{name: c.key, reason: "named by " + BackendEnv + " but not found on PATH"}
			}
		}
		return unavailable{name: key, reason: "unknown " + BackendEnv}
	}
	for _, c := range candidates {
		if c.present() {
			return c.service
		}
	}
	return unavailable{name: "host service", reason: "no supported service manager found"}
}
