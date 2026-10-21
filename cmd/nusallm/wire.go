package main

import (
	"errors"
	"path/filepath"
	"time"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
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
	toolsummary "github.com/jahrulnr/nusallm/infrastructure/service/tool/summary"
	toolexec "github.com/jahrulnr/nusallm/infrastructure/service/tool/terminal"
	toolweb "github.com/jahrulnr/nusallm/infrastructure/service/tool/web"
	toolworkspace "github.com/jahrulnr/nusallm/infrastructure/service/tool/workspace"
	"github.com/jahrulnr/searchwire"
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
// the conversation broker from boot state. workspaceRoot is the
// delivery's default workspace — the root file tools, file://
// resolution, and the local driver's FallbackRoot run against when a
// room binds no workspace. Every fallible step runs before the logger
// is created, so an error return can never leak it. Caller owns
// Shutdown on the broker and Close on the logger.
func compose(cfg config.Config, secrets *reposecret.Store, configPath, workspaceRoot string) (*engine, error) {
	// An empty root would silently resolve to the process cwd inside
	// CheckRoot — exactly the accidental-coupling this param removes.
	if workspaceRoot == "" {
		return nil, errors.New("compose: workspace root is required")
	}
	dir := cfg.Storage.Dir
	gateways := repogateway.NewGatewayRepository(filepath.Join(dir, "gateways"), secrets)
	convs := repoconversation.NewConversationRepository(filepath.Join(dir, "conversations"))
	tools := map[string]dservice.Tool{}
	workspaceStore := repoworkspace.NewLocal()
	readFile, err := toolfile.NewFileReadTool(workspaceStore, workspaceRoot)
	if err != nil {
		return nil, err
	}
	writeFile, err := toolfile.NewFileWriteTool(workspaceStore, workspaceRoot)
	if err != nil {
		return nil, err
	}
	patchFile, err := toolfile.NewFilePatchTool(workspaceStore, workspaceRoot)
	if err != nil {
		return nil, err
	}
	readMedia, err := toolfile.NewReadMediaTool(workspaceStore, workspaceRoot)
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
	// One terminal-session registry shared by every conversation — tools
	// resolve the room from ctx; the app reaps through it on delete and
	// shutdown.
	terminals := toolexec.NewSessionStore()
	execSession, err := toolexec.NewExecSessionTool(terminals)
	if err != nil {
		return nil, err
	}
	writeStdin, err := toolexec.NewWriteStdinTool(terminals)
	if err != nil {
		return nil, err
	}
	// The workspace record is a harness-side hydration tool: registering
	// it lets its name resolve in every room's catalog while
	// ToolPermissionSystem keeps it out of the provider's tool list.
	workspaceRecord, err := toolworkspace.NewWorkspaceTool()
	if err != nil {
		return nil, err
	}
	// The compaction checkpoint contract rides no room catalog — the
	// compaction job offers it alone inside its scratch dialogue.
	summaryTool, err := toolsummary.NewSummaryTool()
	if err != nil {
		return nil, err
	}
	for _, tool := range []dservice.Tool{readFile, writeFile, patchFile, readMedia, execTool, execSession, writeStdin, workspaceRecord} {
		tools[tool.Schema().Name()] = tool
	}
	// Web search/fetch run locally through searchwire so the bound model
	// reasons over raw results itself — the answer tool is deliberately
	// skipped: it delegates answering to a provider-side model. The
	// conversation output store lets oversized fetches spill to readable
	// artifacts instead of eliding mid-document.
	webTools, err := toolweb.Tools(toolOutputs, searchwire.ToolSearch, searchwire.ToolFetch)
	if err != nil {
		return nil, err
	}
	for _, tool := range webTools {
		tools[tool.Schema().Name()] = tool
	}

	logOpts := repolog.Options{MaxLines: cfg.Log.MaxLines, MaxFiles: cfg.Log.MaxFiles, Redact: redact.WithoutBodies}
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
		FileRoot:      workspaceRoot,
		FileStore:     workspaceStore,
	}
	services := map[enum.Style]dservice.TextModelService{
		enum.StyleCompletion:      textcompletion.New(textOpts),
		enum.StyleResponses:       textresponses.New(textOpts),
		enum.StyleMessages:        textmessages.New(textOpts),
		enum.StyleGenerateContent: textgeneratecontent.New(textOpts),
	}
	environments := map[enum.Environment]appjob.EnvironmentDriver{
		enum.EnvironmentLocal: localEnvironment(workspaceRoot, workspaceStore, services, tools),
	}

	return &engine{
		conversations: appconversation.New(convs, gateways, environments, cfg, repoconfig.NewLocal(), configPath, terminals, summaryTool),
		logger:        logger,
	}, nil
}

// localEnvironment bundles the host-machine driver: the workspace repo,
// provider adapters, and tools all default to the delivery's workspace
// root, so a room with no bound workspace runs there.
func localEnvironment(root string, workspaceStore drepo.Workspace, services map[enum.Style]dservice.TextModelService, tools map[string]dservice.Tool) appjob.EnvironmentBundle {
	return appjob.EnvironmentBundle{
		WorkspaceStore: workspaceStore,
		DefaultRoot:    root,
		Services:       services,
		Tools:          tools,
	}
}
