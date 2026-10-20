package job

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// EnvironmentDriver is the per-conversation capability bundle. Tool and
// provider adapters inside one driver must resolve the same workspace; the
// application never mixes ports from different registered environments.
type EnvironmentDriver interface {
	Workspace() drepo.Workspace
	// FallbackRoot is the working directory a turn reports and executes
	// in when the room binds no workspace — "" means unset.
	FallbackRoot() string
	TextService(enum.Style) (dservice.TextModelService, bool)
	Tool(string) (dservice.Tool, bool)
	ToolSchemas() []valueobject.ToolSchema
}

// EnvironmentBundle is the basic driver built from injected ports. Backends
// may implement EnvironmentDriver directly when they need custom lifecycle.
type EnvironmentBundle struct {
	WorkspaceStore drepo.Workspace
	// DefaultRoot feeds FallbackRoot — the directory a room with no bound
	// workspace resolves file tools and exec against. Each delivery picks
	// it: the REPL passes its launch directory, serve the owner's home.
	DefaultRoot string
	Services    map[enum.Style]dservice.TextModelService
	Tools       map[string]dservice.Tool
}

func (b EnvironmentBundle) Workspace() drepo.Workspace { return b.WorkspaceStore }

// FallbackRoot reports the driver's default working directory — "" when the
// composition wired none.
func (b EnvironmentBundle) FallbackRoot() string { return b.DefaultRoot }

func (b EnvironmentBundle) TextService(style enum.Style) (dservice.TextModelService, bool) {
	s, ok := b.Services[style]
	return s, ok
}

func (b EnvironmentBundle) Tool(name string) (dservice.Tool, bool) {
	t, ok := b.Tools[name]
	return t, ok
}

func (b EnvironmentBundle) ToolSchemas() []valueobject.ToolSchema {
	schemas := make([]valueobject.ToolSchema, 0, len(b.Tools))
	for _, tool := range b.Tools {
		schemas = append(schemas, tool.Schema())
	}
	slices.SortFunc(schemas, func(a, b valueobject.ToolSchema) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return schemas
}

// TurnWorkspace resolves the working directory one turn binds into context:
// the room's bound workspace wins; an empty binding falls back to the
// driver's default root so file tools, exec, and the workspace context
// record all cite the same path.
func TurnWorkspace(conv *aggregate.Conversation, driver EnvironmentDriver) string {
	if dir := conv.Workspace(); dir != "" {
		return dir
	}
	return driver.FallbackRoot()
}

func (d Deps) environment(conv *aggregate.Conversation) (EnvironmentDriver, error) {
	kind := conv.Environment()
	driver, ok := d.Environments[kind]
	if !ok || driver == nil {
		return nil, apperror.New(400, "conversation environment unavailable", fmt.Errorf("environment %q is not registered", kind))
	}
	return driver, nil
}
