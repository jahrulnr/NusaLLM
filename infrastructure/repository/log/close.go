package repolog

// Close flushes and releases the active file. The logger stays closed —
// Append after Close errors instead of lazily reopening.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		l.closed = true
		return nil
	}
	err := l.f.Close()
	l.f = nil
	l.closed = true
	return err
}
