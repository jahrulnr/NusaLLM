//go:build !linux

package system

import "runtime"

// New reports no supported manager until this platform's
// implementation lands — tasksched.go for Windows (the user-level
// analog; SCM is system-scope), launchd.go for macOS. The unavailable
// Service keeps non-service commands (install --no-service,
// uninstall, serve) working in the meantime.
func New() Service {
	return unavailable{name: runtime.GOOS, reason: "no supported service manager"}
}
