package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/redact"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	repolog "github.com/jahrulnr/nusallm/infrastructure/repository/log"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	textcompletion "github.com/jahrulnr/nusallm/infrastructure/service/core/text/completion"
	textgeneratecontent "github.com/jahrulnr/nusallm/infrastructure/service/core/text/generatecontent"
	textmessages "github.com/jahrulnr/nusallm/infrastructure/service/core/text/messages"
	textresponses "github.com/jahrulnr/nusallm/infrastructure/service/core/text/responses"
	toolfile "github.com/jahrulnr/nusallm/infrastructure/service/tool/file"
	toolexec "github.com/jahrulnr/nusallm/infrastructure/service/tool/terminal"
)

// engine is the wired application slice every front-end shares — the
// conversation broker over repositories, provider adapters, and tools,
// plus the diagnostic logger the adapters report to. The REPL and the
// `serve` HTTP server both compose one of these; gateways and settings
// come from admin.LoadBoot.
type engine struct {
	conversations *appconversation.ConversationApp
	logger        *repolog.Logger
}

// compose wires repositories, provider adapters, the tool catalog, and
// the conversation broker from boot state. Every fallible step runs
// before the logger is created, so an error return can never leak it.
// Caller owns Shutdown on the broker and Close on the logger.
func compose(cfg config.Config, secrets *reposecret.Store, configPath string) (*engine, error) {
	dir := cfg.Storage.Dir
	gateways := repogateway.NewGatewayRepository(filepath.Join(dir, "gateways"), secrets)
	convs := repoconversation.NewConversationRepository(filepath.Join(dir, "conversations"))
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	tools := map[string]dservice.Tool{}
	workspaceStore := repoworkspace.NewLocal()
	readFile, err := toolfile.NewFileReadTool(workspaceStore, wd) // workspace root = cwd
	if err != nil {
		return nil, err
	}
	writeFile, err := toolfile.NewFileWriteTool(workspaceStore, wd)
	if err != nil {
		return nil, err
	}
	patchFile, err := toolfile.NewFilePatchTool(workspaceStore, wd)
	if err != nil {
		return nil, err
	}
	readMedia, err := toolfile.NewReadMediaTool(workspaceStore, wd)
	if err != nil {
		return nil, err
	}
	toolOutputs, err := repoconversation.NewToolOutputRepository(filepath.Join(dir, "conversations"))
	if err != nil {
		return nil, err
	}
	execTool, err := toolexec.NewExecTool(toolOutputs, toolexec.NewLocalRunner())
	if err != nil {
		return nil, err
	}
	for _, tool := range []dservice.Tool{readFile, writeFile, patchFile, readMedia, execTool} {
		tools[tool.Schema().Name()] = tool
	}

	logOpts := repolog.Options{MaxLines: cfg.Log.MaxLines, MaxFiles: cfg.Log.MaxFiles, Redact: redactWithoutBodies}
	logger, err := repolog.New(filepath.Join(dir, "logs"), logOpts)
	if err != nil {
		return nil, err
	}

	// One adapter instance per wire style — each is stateless and reads the
	// gateway per Send, so the same instance serves every route of a style.
	textOpts := coretext.Options{
		Title:         cfg.App.Name,
		Referer:       cfg.App.Source,
		Logger:        logger,
		HeaderTimeout: time.Duration(cfg.Service.HeaderTimeout),
		IdleTimeout:   time.Duration(cfg.Service.IdleTimeout),
		FileRoot:      wd,
		FileStore:     workspaceStore,
	}
	services := map[enum.Style]dservice.TextModelService{
		enum.StyleCompletion:      textcompletion.New(textOpts),
		enum.StyleResponses:       textresponses.New(textOpts),
		enum.StyleMessages:        textmessages.New(textOpts),
		enum.StyleGenerateContent: textgeneratecontent.New(textOpts),
	}
	environments := map[enum.Environment]appjob.EnvironmentDriver{
		enum.EnvironmentLocal: appjob.EnvironmentBundle{
			WorkspaceStore: workspaceStore,
			Services:       services,
			Tools:          tools,
		},
	}

	return &engine{
		conversations: appconversation.New(convs, gateways, environments, cfg, repoconfig.NewLocal(), configPath),
		logger:        logger,
	}, nil
}

// redactWithoutBodies keeps diagnostic metadata without persisting conversation content.
// provider.request is logged as unmarshaled JSON (the whole body), not a
// map with a "body" key — omit it entirely. Response/error maps still drop
// only their "body" field so status and request_id survive.
func redactWithoutBodies(kind string, e any) any {
	e = redact.Value(e)
	if kind == "provider.request" {
		return map[string]any{"omitted": true}
	}
	switch v := e.(type) {
	case map[string]any:
		delete(v, "body")
		return v
	case []json.RawMessage:
		return map[string]any{"chunks": len(v)}
	case []any:
		return map[string]any{"chunks": len(v)}
	default:
		return map[string]any{"omitted": true}
	}
}
