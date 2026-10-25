package service

// CommandEnvironment resolves the executable search path for a selected
// workspace. Both one-shot commands and terminal sessions use the same port.
type CommandEnvironment interface {
	ToolPaths(workspace string) []string
	Environ(workspace string) []string
}
