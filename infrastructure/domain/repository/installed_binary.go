package repository

import "io"

// InstalledBinary is the executable the next launch runs, e.g.
// ~/.local/bin/nusallm. An update replaces it in one rename: the new bytes are
// staged beside it, then committed over it.
type InstalledBinary interface {
	Path() string
	Stage() (BinaryStaging, error)
}

// BinaryStaging is a new executable being written beside the installed one.
// Exactly one of Commit or Discard ends it.
type BinaryStaging interface {
	io.Writer
	// Commit makes the staged file executable and renames it over the installed
	// path. A running process keeps the file it started from; the next launch
	// runs the new one.
	Commit() error
	// Discard drops the staged file and leaves the installed path alone.
	Discard() error
}
