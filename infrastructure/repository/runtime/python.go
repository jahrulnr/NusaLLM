// Package reporuntime stores private runtime installations and launchers.
package reporuntime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
)

type Local struct{ root string }

func NewLocal(root string) drepo.PythonRuntime { return &Local{root: root} }

func (s *Local) Cached() (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.root, "python-path"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(raw))
	if path == "" {
		return "", nil
	}
	return filepath.Join(s.root, path), nil
}

func pythonExecutable(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "python", "python.exe")
	}
	return filepath.Join(dir, "python", "bin", "python3")
}

type installation struct {
	root, dir string
	committed bool
}

func (i *installation) Executable() string { return pythonExecutable(i.dir) }
func (i *installation) Close() error {
	if i.committed {
		return nil
	}
	return os.RemoveAll(i.dir)
}
func (i *installation) Commit() error {
	relative, err := filepath.Rel(i.root, i.Executable())
	if err != nil {
		return err
	}
	// Installations keep immutable paths. Parallel bootstraps may publish
	// either validated interpreter without moving/deleting one in use.
	if err := repoconfig.NewLocal().WriteAtomic(filepath.Join(i.root, "python-path"), []byte(relative)); err != nil {
		return err
	}
	i.committed = true
	return nil
}

func (s *Local) Stage(ctx context.Context, archive io.Reader) (result drepo.PythonInstallation, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.root, 0755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(s.root, "python-")
	if err != nil {
		return nil, err
	}
	staged := &installation{root: s.root, dir: dir}
	defer func() {
		if retErr != nil {
			_ = staged.Close()
		}
	}()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	// Extraction runs before the caller verifies the archive checksum, so a
	// corrupt payload must stay bounded instead of filling the disk. The cap
	// is one byte over the limit so N==0 after the loop means the stream
	// truly exceeded maxExtracted rather than ending on the boundary.
	const maxExtracted = 1 << 30
	limited := &io.LimitedReader{R: gz, N: maxExtracted + 1}
	reader := tar.NewReader(limited)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.FromSlash(header.Name)
		if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return nil, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, header.FileInfo().Mode().Perm()); err != nil {
				return nil, err
			}
		case tar.TypeReg, tar.TypeRegA:
			file, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode().Perm())
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		case tar.TypeSymlink:
			if err := root.Symlink(filepath.FromSlash(header.Linkname), name); err != nil {
				return nil, err
			}
		case tar.TypeLink:
			if err := root.Link(filepath.FromSlash(header.Linkname), name); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported Python archive entry %q (type %d)", header.Name, header.Typeflag)
		}
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("python archive exceeds %d bytes decompressed", maxExtracted)
	}
	return staged, nil
}

// PrepareLaunchers makes python/python3 resolve lazily through the same binary.
// Runtime selection happens only on invocation, so shell/Node tools stay usable
// without Python or network access. The terminal environment supplies the root.
func PrepareLaunchers(root, executable string) error {
	for _, name := range []string{"python", "python3"} {
		path := filepath.Join(root, "bin", name)
		text := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' python \"$@\"\n"
		if runtime.GOOS == "windows" {
			path += ".cmd"
			text = "@echo off\r\n\"" + strings.ReplaceAll(executable, "%", "%%") + "\" python %*\r\nexit /b %errorlevel%\r\n"
		}
		if err := repoconfig.NewLocal().WriteAtomic(path, []byte(text)); err != nil {
			return err
		}
		if err := os.Chmod(path, 0755); err != nil {
			return err
		}
	}
	return nil
}
