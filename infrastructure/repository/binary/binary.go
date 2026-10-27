// Package repobinary stores the installed executable. A new binary is staged
// beside the installed one and renamed over it, so a launch never meets a
// half-written file, and a failed update leaves the current executable intact.
package repobinary

import (
	"errors"
	"os"
	"path/filepath"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

type installed struct{ path string }

// NewInstalled binds the repository to one executable path.
func NewInstalled(path string) drepo.InstalledBinary {
	return &installed{path: path}
}

func (b *installed) Path() string { return b.path }

// Stage creates the directory if needed and opens a temporary file in it. The
// temporary file shares the target's filesystem, which keeps the commit rename
// atomic.
func (b *installed) Stage() (drepo.BinaryStaging, error) {
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".nusallm-update-*")
	if err != nil {
		return nil, err
	}
	return &staging{path: b.path, file: f}, nil
}

type staging struct {
	path string
	file *os.File
	done bool
}

func (s *staging) Write(p []byte) (int, error) {
	return s.file.Write(p)
}

func (s *staging) Commit() error {
	if s.done {
		return errors.New("binary staging already finished")
	}
	s.done = true
	name := s.file.Name()
	if err := s.file.Chmod(0o755); err != nil {
		_ = s.file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := s.file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func (s *staging) Discard() error {
	if s.done {
		return nil
	}
	s.done = true
	name := s.file.Name()
	_ = s.file.Close()
	return os.Remove(name)
}
