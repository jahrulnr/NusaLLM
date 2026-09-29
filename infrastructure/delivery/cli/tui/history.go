package tui

// HistoryStore persists the REPL input history. The storage format and
// filesystem operations belong to the injected repository implementation.
type HistoryStore interface {
	Open(dir string) (string, error)
	Load(path string) []string
	Append(path, entry string) error
}
