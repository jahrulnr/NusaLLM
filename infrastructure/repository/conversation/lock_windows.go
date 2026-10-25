//go:build windows

package repoconversation

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileLock takes the exclusive advisory lock (blocking — LockFileEx
// waits when LOCKFILE_FAIL_IMMEDIATELY is not set); fileUnlock releases
// the one-byte range the lock covers.
func fileLock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}

func fileUnlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}
