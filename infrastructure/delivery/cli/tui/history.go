package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

var historyEscape = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

// historyUnescape scans escape pairs left-to-right — a replacer chain
// would corrupt a literal backslash followed by "n" ("\n" inside "\\n").
func historyUnescape(s string) string {
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

// loadHistory reads newline-escaped entries oldest-first. Malformed or
// unreadable content yields whatever was parsed — history is a nicety,
// never a boot blocker.
func loadHistory(path string) []string {
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

// appendHistory persists one submitted input as a single escaped line.
func appendHistory(path, entry string) error {
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
