package repository

import (
	"io/fs"
)

// FileTree exposes host directory metadata for workspace selection and
// bootstrap path checks. It does not execute commands or own network I/O.
type FileTree interface {
	Stat(path string) (fs.FileInfo, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	HomeDir() (string, error)
}
