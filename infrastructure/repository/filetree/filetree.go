// Package repofiletree exposes local directory metadata to the file picker.
package repofiletree

import (
	"io/fs"
	"os"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

type Local struct{}

func NewLocal() drepo.FileTree { return Local{} }

func (Local) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

func (Local) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }

func (Local) HomeDir() (string, error) { return os.UserHomeDir() }
