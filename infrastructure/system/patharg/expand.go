// Package patharg turns a path as a caller wrote it into the path a tool
// opens. Three shapes mean something other than the characters: a file: URL
// names a local file, a leading ~ means the owner's home, and on Windows
// %VAR% names an environment value. Everything else is literal — a name is a
// name.
//
// The package knows nothing about workspaces: the workspace repository
// expands first and then applies its own placement rule, so one expansion
// covers every tool and any future route that opens what a model named. Host
// facts (the OS, the separator, the home and the environment) are inputs, not
// globals: the Windows rules stay testable on Linux, and a driver whose
// workspace lives on another host decodes against that host's facts.
package patharg

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Host is what expansion depends on: the host OS name, its path separator,
// the owner's home directory, and (on Windows) the environment. A zero Home
// means the home is unknown — a leading ~ is then an error, never a literal
// folder.
type Host struct {
	GOOS string
	Sep  string
	Home string
	Env  map[string]string // names upper-cased: Windows lookup ignores case
}

// ProcessHost reads the facts of the host this process runs on.
func ProcessHost() Host {
	h := Host{GOOS: runtime.GOOS, Sep: string(filepath.Separator)}
	if home, err := os.UserHomeDir(); err == nil {
		h.Home = strings.TrimRight(home, `/\`)
	}
	if h.GOOS == "windows" {
		h.Env = map[string]string{}
		for _, kv := range os.Environ() {
			if name, value, ok := strings.Cut(kv, "="); ok {
				h.Env[strings.ToUpper(name)] = value
			}
		}
	}
	return h
}

// Expand turns what a caller wrote into the path to open: a file: URL becomes
// the local file it names, a leading `~` (alone, `~/…` or `~\…`) becomes the
// owner's home, and on Windows `%VAR%` becomes that variable's value.
// Everything else stays literal — `$HOME` and `${HOME}` are names a caller
// may mean to write, `~user` names a file too (this host has no other users
// to ask about), and `%APPDATA%` on a non-Windows host is a legal folder name.
func Expand(raw string) (string, error) { return ExpandWith(ProcessHost(), raw) }

// ExpandWith is Expand against facts the caller supplies.
func ExpandWith(h Host, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	local, isFile, err := fileURLPath(h.GOOS, raw)
	if err != nil {
		return "", err
	}
	if isFile {
		raw = local
	}
	// Only a leading ~ is the home marker; a ~ anywhere else is part of a
	// name.
	if strings.HasPrefix(raw, "~") && (len(raw) == 1 || raw[1] == '/' || raw[1] == '\\') {
		if h.Home == "" {
			return "", fmt.Errorf(`cannot expand "~": the home directory is not known`)
		}
		rest := strings.TrimLeft(raw[1:], `/\`)
		if rest == "" {
			return h.Home, nil
		}
		return h.Home + h.Sep + rest, nil
	}
	if h.GOOS == "windows" {
		return expandEnvPercents(h, raw)
	}
	return raw, nil
}

// fileURLPath answers the local path a file: URL names. A host other than
// empty or localhost is refused — these tools open this machine. The slash
// before a drive letter (file:///C:/…) is Windows-only URL syntax: on any
// other host "/C:/…" is a real, odd path and is left alone.
func fileURLPath(goos, raw string) (string, bool, error) {
	if len(raw) < len("file:") || !strings.EqualFold(raw[:len("file:")], "file:") {
		return raw, false, nil
	}
	rest := raw[len("file:"):]
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		cut := strings.IndexAny(rest, `/\`)
		if cut < 0 {
			return "", true, fmt.Errorf("file URL %q names no path", raw)
		}
		if authority := rest[:cut]; authority != "" && !strings.EqualFold(authority, "localhost") {
			return "", true, fmt.Errorf("file URL %q is not a local path", raw)
		}
		rest = rest[cut:]
	}
	if rest == "" {
		return "", true, fmt.Errorf("file URL %q names no path", raw)
	}
	// Markdown destinations are URLs, so a caller encodes spaces; an
	// unparsable escape is a filename character, not an error.
	if decoded, err := url.PathUnescape(rest); err == nil {
		rest = decoded
	}
	if goos == "windows" && len(rest) > 2 && rest[0] == '/' && rest[2] == ':' && isASCIILetter(rest[1]) {
		rest = rest[1:]
	}
	return rest, true, nil
}

// expandEnvPercents replaces Windows %VAR% references. An unset variable is an
// error naming it rather than a literal folder: %APPDATA%\x silently becoming
// a folder called %APPDATA% is the same defect class as ~ turning into ./~.
// A % that encloses no plausible variable name stays literal, so a file
// called "50% off" survives.
func expandEnvPercents(h Host, raw string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(raw); {
		if raw[i] != '%' {
			b.WriteByte(raw[i])
			i++
			continue
		}
		end := strings.IndexByte(raw[i+1:], '%')
		if end < 0 {
			b.WriteString(raw[i:])
			break
		}
		name := raw[i+1 : i+1+end]
		if name == "" { // %% is a literal percent
			b.WriteByte('%')
			i += 2
			continue
		}
		if !envName(name) {
			b.WriteByte('%')
			i++
			continue
		}
		value, ok := h.Env[strings.ToUpper(name)]
		if !ok {
			return "", fmt.Errorf("cannot expand %%%s%%: the environment variable is not set", name)
		}
		b.WriteString(value)
		i += end + 2
	}
	return b.String(), nil
}

// envName reports whether name has the shape of a Windows environment variable
// — %ProgramFiles(x86)% included.
func envName(name string) bool {
	for i, c := range name {
		switch {
		case c == '_' || c == '(' || c == ')':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return name != ""
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
