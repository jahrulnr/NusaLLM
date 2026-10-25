//go:build unix

package repoconversation

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileLock takes the exclusive advisory lock; fileUnlock releases it.
func fileLock(f *os.File) error   { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
func fileUnlock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
