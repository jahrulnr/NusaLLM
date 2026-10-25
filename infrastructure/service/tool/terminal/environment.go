package toolexec

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// LocalEnvironment supplies workspace/global/application tool directories and
// private Python launchers without mutating the daemon's process environment.
type LocalEnvironment struct {
	Home, Storage string
	Paths         []string
}

func (e LocalEnvironment) ToolPaths(workspace string) []string {
	var paths []string
	if workspace != "" {
		paths = append(paths, filepath.Join(workspace, ".agents", "tools", "bin"))
	}
	if e.Home != "" {
		paths = append(paths, filepath.Join(e.Home, ".agents", "tools", "bin"))
	}
	paths = append(paths, e.Paths...)
	if e.Storage != "" {
		paths = append(paths, filepath.Join(e.Storage, "tools", "bin"))
	}
	var resolved []string
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
			if e.Home == "" {
				// Unresolvable ~ must not fall through to the workspace join,
				// which would silently expose the workspace root on PATH.
				continue
			}
			path = filepath.Join(e.Home, path[1:])
		}
		if !filepath.IsAbs(path) && workspace != "" {
			path = filepath.Join(workspace, path)
		}
		path = filepath.Clean(path)
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		resolved = append(resolved, path)
	}
	return resolved
}

func (e LocalEnvironment) Environ(workspace string) []string {
	paths := e.ToolPaths(workspace)
	// Python launchers follow user tool paths, but precede host PATH so a
	// missing or incomplete host Python is resolved by the lazy bootstrap.
	if e.Storage != "" {
		paths = append(paths, filepath.Join(e.Storage, "runtimes", "bin"))
	}
	var env []string
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		switch {
		case strings.EqualFold(key, "PATH"):
			paths = append(paths, value)
		case strings.EqualFold(key, "NUSALLM_RUNTIME_DIR"):
			// Dropped; the storage-rooted value below replaces it.
		default:
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+strings.Join(paths, string(os.PathListSeparator)))
	if e.Storage != "" {
		env = append(env, "NUSALLM_RUNTIME_DIR="+filepath.Join(e.Storage, "runtimes"))
	}
	return env
}

var _ dservice.CommandEnvironment = LocalEnvironment{}
