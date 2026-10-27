//go:build !unix

package system

import "errors"

// Reexec is unsupported where there is no exec(2) to replace the process with.
func Reexec(string, []string) error {
	return errors.New("replacing the running process is not supported on this platform")
}
