// Package fs is the WebUI's local-filesystem browse boundary — the
// workspace picker's directory listing. It answers directories only:
// this is a picker feed, not a file API. The default directory is the
// owner's home; navigation ascends to the filesystem root so targets
// outside home (mounted storage, /apps, Windows drives) stay reachable.
// The path must be absolute; every entry returned is a joined child of
// the requested directory — the API never interprets `..` segments or
// serves file contents.
package fs

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// maxEntries bounds one directory listing — picker feeds stay small.
const maxEntries = 1000

type browseDTO struct {
	Path    string     `json:"path"`
	Parent  string     `json:"parent,omitempty"`
	Roots   []rootDTO  `json:"roots"`
	Entries []entryDTO `json:"entries"`
}

type rootDTO struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type entryDTO struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Hidden bool   `json:"hidden,omitempty"`
}

// Register mounts this boundary's routes under /api.
func Register(api *gin.RouterGroup) {
	api.GET("/fs/browse", browse)
}

func browse(c *gin.Context) {
	dir := strings.TrimSpace(c.Query("path"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			transport.WriteProblem(c, 500, "internal", "could not resolve the home directory")
			return
		}
		dir = home
	}
	if !filepath.IsAbs(dir) {
		transport.WriteProblem(c, 400, "invalid_argument", "path must be absolute")
		return
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		transport.WriteProblem(c, 404, "not_found", "no such directory")
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		transport.WriteProblem(c, 403, "forbidden", "directory is not readable")
		return
	}
	out := browseDTO{Path: dir, Roots: roots(), Entries: make([]entryDTO, 0)}
	if parent := filepath.Dir(dir); parent != dir {
		out.Parent = parent
	}
	for _, e := range ents {
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue // picker feeds directories; symlinks get statted below
		}
		if e.Type()&os.ModeSymlink != 0 {
			// A symlink counts only if it resolves to a directory — the
			// picker navigates real trees.
			fi, err := os.Stat(filepath.Join(dir, e.Name()))
			if err != nil || !fi.IsDir() {
				continue
			}
		}
		out.Entries = append(out.Entries, entryDTO{
			Name:   e.Name(),
			Path:   filepath.Join(dir, e.Name()),
			Hidden: strings.HasPrefix(e.Name(), "."),
		})
		if len(out.Entries) >= maxEntries {
			break
		}
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
	c.JSON(http.StatusOK, out)
}

// roots lists the navigable filesystem roots — "/" on unix, the present
// drive letters on Windows — so the picker can jump to a tree outside
// the current directory's ancestry.
func roots() []rootDTO {
	if runtime.GOOS != "windows" {
		return []rootDTO{{Name: "/", Path: "/"}}
	}
	var out []rootDTO
	for d := 'A'; d <= 'Z'; d++ {
		p := string(d) + `:\`
		if _, err := os.Stat(p); err == nil {
			out = append(out, rootDTO{Name: string(d) + ":", Path: p})
		}
	}
	return out
}
