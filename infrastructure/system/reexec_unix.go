//go:build unix

package system

import (
	"os"
	"syscall"
)

// Reexec replaces this process with the program at path, keeping its argv,
// environment, and controlling terminal. It returns only when the exec fails.
// Go opens files and sockets close-on-exec, so the replacement starts without
// inheriting this process's listeners.
func Reexec(path string, argv []string) error {
	return syscall.Exec(path, argv, os.Environ())
}
