// Package repohistory persists CLI input history in a local file.
package repohistory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Local struct{}

func NewLocal() Local { return Local{} }

func (Local) Open(dir string) (string, error) {
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

var historyEscape = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

func historyUnescape(s string) string {
	// Scan once: chained replacements would turn an escaped literal \n into
	// a newline after the first replacement removes its doubled slash.
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == 'n' {
				b.WriteByte('\n')
			} else {
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (Local) Load(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line != "" {
			entries = append(entries, historyUnescape(line))
		}
	}
	return entries
}

func (Local) Append(path, entry string) error {
	if entry == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(historyEscape.Replace(entry) + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
