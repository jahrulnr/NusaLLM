// Package reposkill discovers filesystem skills and installs shipped assets.
package reposkill

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

type Local struct{ home, builtin string }

func NewLocal(home, builtin string) drepo.Skills { return &Local{home: home, builtin: builtin} }

func (s *Local) List(ctx context.Context, workspace string) (drepo.SkillCatalog, error) {
	catalog := drepo.SkillCatalog{Skills: []drepo.Skill{}}
	byName := map[string]drepo.Skill{}
	// Later roots replace earlier entries by declared name.
	roots := []struct{ path, source string }{{s.builtin, "builtin"}}
	if s.home != "" {
		roots = append(roots, struct{ path, source string }{filepath.Join(s.home, ".agents", "skills"), "global"})
	}
	if workspace != "" {
		roots = append(roots, struct{ path, source string }{filepath.Join(workspace, ".agents", "skills"), "workspace"})
	}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return catalog, err
		}
		if root.path == "" {
			continue
		}
		abs, err := filepath.Abs(root.path)
		if err != nil {
			return catalog, err
		}
		dirs, err := os.ReadDir(abs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			catalog.Diagnostics = append(catalog.Diagnostics, fmt.Sprintf("%s: %v", abs, err))
			continue
		}
		seen := map[string]bool{}
		for _, dir := range dirs {
			if err := ctx.Err(); err != nil {
				return catalog, err
			}
			if !dir.IsDir() && dir.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(abs, dir.Name(), "SKILL.md")
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				catalog.Diagnostics = append(catalog.Diagnostics, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			name, description, err := frontmatter(raw)
			if err != nil {
				catalog.Diagnostics = append(catalog.Diagnostics, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			if seen[name] {
				catalog.Diagnostics = append(catalog.Diagnostics, fmt.Sprintf("%s: duplicate skill name %q in %s", path, name, abs))
				continue
			}
			seen[name] = true
			byName[name] = drepo.Skill{Name: name, Description: description, Path: path, Source: root.source}
		}
	}
	for _, skill := range byName {
		catalog.Skills = append(catalog.Skills, skill)
	}
	sort.Slice(catalog.Skills, func(i, j int) bool { return catalog.Skills[i].Name < catalog.Skills[j].Name })
	return catalog, ctx.Err()
}

// frontmatter parses YAML rather than guessing at colon-separated lines, so
// folded descriptions, quoted names and optional nested metadata work.
func frontmatter(raw []byte) (string, string, error) {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", "", fmt.Errorf("missing YAML frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] != "---" {
			continue
		}
		var meta struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if err := yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &meta); err != nil {
			return "", "", fmt.Errorf("invalid frontmatter: %w", err)
		}
		name, description := strings.TrimSpace(meta.Name), strings.TrimSpace(meta.Description)
		if name == "" || description == "" {
			return "", "", fmt.Errorf("frontmatter requires name and description")
		}
		return name, description, nil
	}
	return "", "", fmt.Errorf("unclosed YAML frontmatter")
}

func (s *Local) Read(ctx context.Context, workspace, name string) (drepo.Skill, string, error) {
	catalog, err := s.List(ctx, workspace)
	if err != nil {
		return drepo.Skill{}, "", err
	}
	for _, skill := range catalog.Skills {
		if skill.Name != name {
			continue
		}
		if err := ctx.Err(); err != nil {
			return drepo.Skill{}, "", err
		}
		raw, err := os.ReadFile(skill.Path)
		return skill, string(raw), err
	}
	return drepo.Skill{}, "", fmt.Errorf("unknown skill %q", name)
}

// InstallBuiltins materializes embedded assets in the application's own skill
// root. Shipped files follow the binary's contents: paths the binary no longer
// ships are pruned, and user roots are untouched.
func InstallBuiltins(dir string, assets fs.FS) error {
	shipped := map[string]bool{}
	if err := fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		shipped[path] = true
		raw, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		prior, err := os.ReadFile(dest)
		if err == nil && bytes.Equal(prior, raw) {
			return nil
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		// Readers can discover skills while another engine bootstraps. Publish
		// each shipped file atomically so they never see a partial document.
		tmp, err := os.CreateTemp(filepath.Dir(dest), ".skill-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		_, writeErr := tmp.Write(raw)
		closeErr := tmp.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Chmod(tmp.Name(), 0644); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), dest)
	}); err != nil {
		return err
	}
	var dirs []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Skip publish temps a concurrent bootstrap may still be writing.
		if !shipped[filepath.ToSlash(rel)] && !strings.HasPrefix(entry.Name(), ".skill-") {
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		// Deepest-first so emptied parents collapse after their children.
		// Dirs still holding shipped files or anything unreadable stay.
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
	return nil
}
