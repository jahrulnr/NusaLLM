package job

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

type skillDiscovery struct {
	schema          valueobject.ToolSchema
	text, workspace string
	err             error
	calls           int
}

func (s *skillDiscovery) Schema() valueobject.ToolSchema { return s.schema }
func (s *skillDiscovery) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	s.workspace = dservice.WorkspaceDir(ctx)
	s.calls++
	return dservice.ToolResult{Text: s.text}, s.err
}

func skillsDriver(t *testing.T) (EnvironmentBundle, *skillDiscovery) {
	t.Helper()
	schema, err := valueobject.NewToolSchema("skills", "catalog", nil, true, enum.ToolPermissionGeneral)
	if err != nil {
		t.Fatal(err)
	}
	discovery := &skillDiscovery{schema: schema, text: `{"skills":[{"name":"fixture","description":"Fixture"}]}`}
	return EnvironmentBundle{DefaultRoot: "/workspace", Tools: map[string]dservice.Tool{"skills": discovery}}, discovery
}

func skillRecords(conv *aggregate.Conversation) []entity.ToolCall {
	var calls []entity.ToolCall
	for _, msg := range conv.Messages() {
		for _, call := range msg.ToolCalls() {
			if call.Name() == "skills" && call.Type() == enum.TooltypeInjection {
				calls = append(calls, call)
			}
		}
	}
	return calls
}

// The provider must see discovery before its first call, and persistence must
// retain the exact hydration result with no fabricated latency.
func TestRunTurnHydratesSkillsAndRefreshesChanges(t *testing.T) {
	d := Deps{Convs: repoconversation.NewConversationRepository(t.TempDir()), Config: config.ConversationConfig{MaxTurnStep: 4}}
	conv := seedRoom(t, d.Convs, "skills-room", "go")
	driver, discovery := skillsDriver(t)
	customSchema, err := valueobject.NewToolSchema("custom_tools", "paths", nil, true, enum.ToolPermissionSystem)
	if err != nil {
		t.Fatal(err)
	}
	driver.Tools["custom_tools"] = &skillDiscovery{schema: customSchema, text: `{"paths":["/workspace/.agents/tools/bin"]}`}
	svc := &skillsSeeingService{t: t}
	for turn := 0; turn < 3; turn++ {
		if turn > 0 {
			user, _ := entity.NewMessage(enum.RoleUser, "again")
			conv.Append(user)
			if _, err := d.Convs.AppendMessage(t.Context(), conv.ID(), user); err != nil {
				t.Fatal(err)
			}
		}
		if turn == 2 {
			discovery.text = `{"skills":[]}`
		}
		if err := d.runTurn(t.Context(), conv.ID(), conv, driver, nil, entity.Model{}, svc, nil, nil); err != nil {
			t.Fatal(err)
		}
		want := 1
		if turn == 2 {
			want = 2
		}
		if len(skillRecords(conv)) != want {
			t.Fatalf("turn %d records = %d", turn, len(skillRecords(conv)))
		}
	}
	if discovery.workspace != "/workspace" {
		t.Fatalf("bound workspace = %q", discovery.workspace)
	}
	stored, err := d.Convs.Load(t.Context(), conv.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(skillRecords(stored)) != 2 || skillRecords(stored)[1].Output() != discovery.text {
		t.Fatal("discovery not persisted exactly")
	}
}

func TestAgentCanCallSkillsCatalogAfterHydration(t *testing.T) {
	d := Deps{
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "skills-agent-call", "find a skill")
	driver, discovery := skillsDriver(t)
	svc := &skillsCallingService{t: t}
	if err := d.runTurn(t.Context(), conv.ID(), conv, driver, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	if svc.calls != 2 {
		t.Fatalf("provider calls = %d, want tool call followed by answer", svc.calls)
	}
	for _, msg := range conv.Messages() {
		for _, call := range msg.ToolCalls() {
			if call.Name() != "skills" || call.Type() != enum.TooltypeOrigin {
				continue
			}
			if !call.HasOutput() || call.Output() != discovery.text {
				t.Fatalf("agent skills call output = %q", call.Output())
			}
			return
		}
	}
	t.Fatal("agent skills call was not recorded")
}

type skillsCallingService struct {
	t     *testing.T
	calls int
}

func (s *skillsCallingService) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.calls++
	if s.calls == 1 {
		agentCallable := false
		for _, schema := range conv.Tools() {
			if schema.Name() == "skills" && schema.CallableByAgent() {
				agentCallable = true
				break
			}
		}
		if !agentCallable {
			s.t.Fatal("skills catalog was not offered to the agent")
		}
		message, _ := entity.NewMessage(enum.RoleAssistant, "refreshing the skill catalog")
		call, err := entity.NewToolCall("skills_agent_1", "skills", `{}`, enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		message.AddToolCall(call)
		message.SetFinishReason(enum.FinishReasonToolCalls)
		conv.Append(message)
		return nil
	}
	message, _ := entity.NewMessage(enum.RoleAssistant, "done")
	message.SetFinishReason(enum.FinishReasonStop)
	conv.Append(message)
	return nil
}

type skillsSeeingService struct{ t *testing.T }

func (s *skillsSeeingService) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	s.t.Helper()
	if len(skillRecords(conv)) == 0 {
		s.t.Fatal("provider called without skills hydration")
	}
	customPaths := false
	for _, msg := range conv.Messages() {
		for _, call := range msg.ToolCalls() {
			if call.Name() == "custom_tools" && call.Type() == enum.TooltypeInjection && call.HasOutput() && strings.Contains(call.Output(), "/workspace/.agents/tools/bin") {
				customPaths = true
			}
		}
	}
	if !customPaths {
		s.t.Fatal("provider called without executable PATH hydration")
	}
	for _, msg := range conv.Messages() {
		for _, call := range msg.ToolCalls() {
			if call.Name() == "skills" {
				if msg.Role() != enum.RoleSystem || !call.HasOutput() || call.OutputLatency() != "" {
					s.t.Fatal("invalid skills provenance")
				}
				var input struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal([]byte(call.Input()), &input); err != nil || input.Path != "/workspace" {
					s.t.Fatalf("input = %q", call.Input())
				}
			}
		}
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

func TestSkillsHydrationReappearsAfterCompaction(t *testing.T) {
	for _, reactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "budget", true: "413"}[reactive], func(t *testing.T) {
			d := Deps{SummaryTool: mustSummaryTool(t), Convs: repoconversation.NewConversationRepository(t.TempDir()), Config: config.ConversationConfig{MaxTurnStep: 4, CompactBudgetPct: 100, CompactKeepTail: 1}}
			conv := seedRoom(t, d.Convs, "skills-fold", "go")
			driver, discovery := skillsDriver(t)
			schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
			driver.Tools["push"] = fatTool{schema: schema}
			model := entity.Model{}
			inner := &foldWatchSvc{}
			if reactive {
				inner.failAt = 2
			} else {
				model.SetContextWindow(entity.DefaultUnknownMaxOutputTokens + 500)
			}
			svc := &skillsFoldService{t: t, inner: inner}
			if err := d.runTurn(t.Context(), conv.ID(), conv, driver, nil, model, svc, nil, nil); err != nil {
				t.Fatal(err)
			}
			if conv.Messages()[0].UserType() != enum.LLMUserTypeSystem {
				t.Fatal("fixture did not compact")
			}
			calls := skillRecords(conv)
			if len(calls) != 1 || calls[0].Output() != discovery.text {
				t.Fatalf("post-fold catalog = %+v", calls)
			}
			stored, err := d.Convs.Load(t.Context(), conv.ID())
			if err != nil || len(skillRecords(stored)) != 1 {
				t.Fatalf("persisted catalog lost: %v", err)
			}
		})
	}
}

type skillsFoldService struct {
	t     *testing.T
	inner *foldWatchSvc
}

func (s *skillsFoldService) Send(ctx context.Context, conv *aggregate.Conversation, gw *aggregate.Gateway, model entity.Model, emit event.TurnEventHandler) error {
	for _, schema := range conv.Tools() {
		if schema.Name() == "summary" {
			return s.inner.Send(ctx, conv, gw, model, emit)
		}
	}
	if len(skillRecords(conv)) == 0 {
		s.t.Fatal("provider cannot see catalog after compaction")
	}
	return s.inner.Send(ctx, conv, gw, model, emit)
}

func TestSkillsHydrationReportsErrorsAndChecksSystemPermission(t *testing.T) {
	d := Deps{Convs: repoconversation.NewConversationRepository(t.TempDir())}
	conv := seedRoom(t, d.Convs, "skills-errors", "go")
	driver, discovery := skillsDriver(t)
	discovery.err = errors.New("discovery failed")
	if err := d.appendToolContext(t.Context(), conv.ID(), conv, driver, "skills"); err != nil {
		t.Fatal(err)
	}
	if out := skillRecords(conv)[0].Output(); !strings.Contains(out, "discovery failed") {
		t.Fatalf("error = %q", out)
	}
	discovery.schema, _ = valueobject.NewToolSchema("skills", "catalog", nil, true, enum.ToolPermissionAgent)
	if err := d.appendToolContext(t.Context(), conv.ID(), conv, driver, "skills"); err == nil {
		t.Fatal("agent-only tool executed by hydration")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// Restore system permission: a canceled request must never persist a record.
	discovery.schema, _ = valueobject.NewToolSchema("skills", "catalog", nil, true, enum.ToolPermissionGeneral)
	if err := d.appendToolContext(ctx, conv.ID(), conv, driver, "skills"); err != context.Canceled {
		t.Fatalf("cancel = %v", err)
	}
}
