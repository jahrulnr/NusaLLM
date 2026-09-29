package config

// Store is the storage port for configuration documents. Parsing and defaults
// stay in config; the repository decides where and how bytes are persisted.
type Store interface {
	Read(path string) ([]byte, error)
	WriteAtomic(path string, raw []byte) error
	Exists(path string) (bool, error)
}
