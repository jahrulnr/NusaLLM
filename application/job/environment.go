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
	TextService(enum.Style) (dservice.TextModelService, bool)
	Tool(string) (dservice.Tool, bool)
	ToolSchemas() []valueobject.ToolSchema
}

// EnvironmentBundle is the basic driver built from injected ports. Backends
// may implement EnvironmentDriver directly when they need custom lifecycle.
type EnvironmentBundle struct {
	WorkspaceStore drepo.Workspace
	Services       map[enum.Style]dservice.TextModelService
	Tools          map[string]dservice.Tool
}

func (b EnvironmentBundle) Workspace() drepo.Workspace { return b.WorkspaceStore }

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

func (d Deps) environment(conv *aggregate.Conversation) (EnvironmentDriver, error) {
	kind := conv.Environment()
	driver, ok := d.Environments[kind]
	if !ok || driver == nil {
		return nil, apperror.New(400, "conversation environment unavailable", fmt.Errorf("environment %q is not registered", kind))
	}
	return driver, nil
}
