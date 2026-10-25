//go:build !unix && !windows

package repoconversation

import "os"

// No advisory file locking on this platform — degraded to a no-op; the
// in-process mutex still serializes a single instance.
func fileLock(f *os.File) error   { return nil }
func fileUnlock(f *os.File) error { return nil }
