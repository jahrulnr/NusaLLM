package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func privateHistoryFile(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("history directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("history path %q must be a real directory", dir)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return "", err
	}

	path := filepath.Join(abs, "history")
	info, err = os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("history path %q must be a regular file", path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}
