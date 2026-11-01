package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	toolsummary "github.com/jahrulnr/nusallm/infrastructure/service/tool/summary"
)

// mustSummaryTool builds the injected compaction contract once per Deps.
func mustSummaryTool(t *testing.T) dservice.Tool {
	t.Helper()
	tool, err := toolsummary.NewSummaryTool()
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// requireWorkspaceContext asserts msg is the workspace context record the
// turn loop appends after an editable user line: a system-role hydration
// message carrying one completed injection-typed `workspace` call whose
// JSON input names dir and whose stored output is exactly `workspace
// changed to <dir-or-(unset)>` — no measured latency.
func requireWorkspaceContext(t *testing.T, msg entity.Message, dir string) {
	t.Helper()
	if msg.Role() != enum.RoleSystem || msg.Editable() {
		t.Fatalf("workspace context = %v editable=%v, want a synthetic system record", msg.Role(), msg.Editable())
	}
	if msg.Content() != "" {
		t.Fatalf("workspace context content=%q", msg.Content())
	}
	calls := msg.ToolCalls()
	if len(calls) != 1 || calls[0].Name() != "workspace" || calls[0].Type() != enum.TooltypeInjection {
		t.Fatalf("workspace context calls = %+v", calls)
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(calls[0].Input()), &input); err != nil {
		t.Fatalf("workspace input is not valid JSON: %q", calls[0].Input())
	}
	if input.Path != dir {
		t.Fatalf("workspace input path = %q, want %q", input.Path, dir)
	}
	shown := dir
	if shown == "" {
		shown = "(unset)"
	}
	if want := "workspace changed to " + shown; calls[0].Output() != want {
		t.Fatalf("workspace output = %q, want %q", calls[0].Output(), want)
	}
	if calls[0].OutputLatency() != "" {
		t.Fatalf("injection latency = %q, want empty — a stored record, not an execution", calls[0].OutputLatency())
	}
}

// countWorkspaceContexts tallies the injection records in a history — at
// most one live pin per directory: an unchanged pin is not repeated.
func countWorkspaceContexts(msgs []entity.Message) int {
	n := 0
	for _, m := range msgs {
		for _, call := range m.ToolCalls() {
			if call.Type() == enum.TooltypeInjection && call.Name() == "workspace" {
				n++
			}
		}
	}
	return n
}

// seedRoom builds a saved conversation holding one editable user line —
// the runTurn precondition every turn test starts from.
func seedRoom(t *testing.T, convs drepo.Conversation, convID, text string) *aggregate.Conversation {
	t.Helper()
	conv, err := aggregate.NewConversation(convID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := entity.NewMessage(enum.RoleUser, text)
	if err != nil {
		t.Fatal(err)
	}
	conv.Append(user)
	if err := convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	if _, err := convs.AppendMessage(t.Context(), convID, user); err != nil {
		t.Fatal(err)
	}
	return conv
}

// hydrationDeps wires the full job surface — conversation and gateway
// repos plus the local environment — so Edit and Send run end to end
// against a scripted service.
func hydrationDeps(t *testing.T, svc dservice.TextModelService) Deps {
	t.Helper()
	gw, err := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	if err != nil {
		t.Fatal(err)
	}
	m, err := entity.NewModel("m1", "gw1")
	if err != nil {
		t.Fatal(err)
	}
	gw.SetModels([]entity.Model{m})
	gateways := repogateway.NewGatewayRepository(t.TempDir(), nil)
	if err := gateways.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	return Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Gateways:    gateways,
		Environments: map[enum.Environment]EnvironmentDriver{
			enum.EnvironmentLocal: EnvironmentBundle{
				Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
			},
		},
		Config: config.ConversationConfig{MaxTurnStep: 4},
	}
}

// A completed turn is [user, workspace record, assistant]: the record
// cites the bound workspace as valid JSON even with quoting in the path,
// the system prompt stays verbatim, and the write persists.
func TestRunTurnAppendsWorkspaceContextAfterEditableLine(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	dir := `/tmp/work "quoted"\dir` // exercises JSON escaping end to end
	conv := seedRoom(t, d.Convs, "room", "hi")
	conv.SetWorkspace(dir)
	conv.SetSystemPrompt("EMBEDDED PROMPT — untouched")
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	svc := &captureSvc{}
	// The driver's default must lose to the binding.
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{DefaultRoot: "/elsewhere"}, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 3 {
		t.Fatalf("history = %d msgs, want user+context+answer", len(msgs))
	}
	requireWorkspaceContext(t, msgs[1], dir)
	if msgs[2].Role() != enum.RoleAssistant || msgs[2].Content() != "done" {
		t.Fatalf("reply = %+v", msgs[2])
	}
	if svc.sawPrompt != "EMBEDDED PROMPT — untouched" {
		t.Fatalf("provider saw prompt %q — the embedded prompt must pass through verbatim", svc.sawPrompt)
	}
	stored, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages()) != 3 {
		t.Fatalf("persisted history = %d, want 3", len(stored.Messages()))
	}
	requireWorkspaceContext(t, stored.Messages()[1], dir)
}

// With no bound workspace the record cites the driver's fallback root;
// with neither it renders "(unset)" and binds nothing into tool context.
func TestRunTurnWorkspaceContextFallbackAndUnset(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &captureSvc{}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{DefaultRoot: "/srv/root"}, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	requireWorkspaceContext(t, conv.Messages()[1], "/srv/root")

	conv2 := seedRoom(t, d.Convs, "empty", "hi")
	if err := d.runTurn(t.Context(), "empty", conv2, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	requireWorkspaceContext(t, conv2.Messages()[1], "")
	if out := conv2.Messages()[1].ToolCalls()[0].Output(); !strings.Contains(out, "(unset)") {
		t.Fatalf("unset workspace output = %q, want the (unset) marker", out)
	}
}

// The record is exact-turn state, not a loop step: a tool round-trip
// ends on an assistant message, so no second record lands.
func TestRunTurnSingleContextThroughToolLoop(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	conv := seedRoom(t, d.Convs, "room", "go")
	schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"push": pushTool{schema: schema}}}
	svc := &steerFoldSvc{}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	// user / workspace record / assistant+push call / assistant done
	if len(msgs) != 4 {
		t.Fatalf("history = %d msgs, want 4", len(msgs))
	}
	requireWorkspaceContext(t, msgs[1], "")
	if got := countWorkspaceContexts(msgs); got != 1 {
		t.Fatalf("workspace records = %d, want exactly 1 per editable line", got)
	}
	if msgs[3].Content() != "done" {
		t.Fatalf("final answer = %q", msgs[3].Content())
	}
}

// overflowOnceSvc 413s the first real call with no append — the
// provider's own overflow verdict — then serves the scratch summary and
// the retried send.
type overflowOnceSvc struct{ realCalls atomic.Int32 }

func (s *overflowOnceSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	for _, tool := range conv.Tools() {
		if tool.Name() == toolsummary.ToolName {
			m, _ := entity.NewMessage(enum.RoleAssistant, "")
			tc, err := entity.NewToolCall("sum_1", toolsummary.ToolName, `{"text":"CHECKPOINT"}`, enum.TooltypeOrigin)
			if err != nil {
				return err
			}
			m.AddToolCall(tc)
			m.SetFinishReason(enum.FinishReasonToolCalls)
			conv.Append(m)
			return nil
		}
	}
	if s.realCalls.Add(1) == 1 {
		return apperror.New(413, "context overflow", errors.New("too long"))
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

// keepTail=1 is the tightest tail the config admits: after the 413-driven
// fold, exactly one workspace record survives — seated right before the
// retried reply — and the retry does not append a duplicate.
func TestRunTurn413KeepTailOneLeavesSingleContext(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4, CompactKeepTail: 1},
	}
	conv := seedRoom(t, d.Convs, "room", "hi")
	svc := &overflowOnceSvc{}
	if err := d.runTurn(t.Context(), "room", conv, EnvironmentBundle{}, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 3 {
		t.Fatalf("history = %d msgs, want checkpoint+context+answer: %+v", len(msgs), msgs)
	}
	if msgs[0].UserType() != enum.LLMUserTypeSystem || len(msgs[0].ToolCalls()) != 0 {
		t.Fatalf("head = %+v, want the compaction checkpoint", msgs[0])
	}
	requireWorkspaceContext(t, msgs[1], "")
	if msgs[2].Content() != "done" {
		t.Fatalf("retried reply = %q", msgs[2].Content())
	}
	if got := countWorkspaceContexts(msgs); got != 1 {
		t.Fatalf("workspace records = %d, want 1 — the retry must not duplicate it", got)
	}
	stored, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages()) != 3 || countWorkspaceContexts(stored.Messages()) != 1 {
		t.Fatalf("persisted tail = %d msgs %d records", len(stored.Messages()), countWorkspaceContexts(stored.Messages()))
	}
}

// Editing a user line rewrites the turn: RewriteFrom drops the old
// workspace record with the rest of the suffix, and the regenerated turn
// writes a fresh one citing the room's current binding.
func TestEditDropsAndRegeneratesWorkspaceContext(t *testing.T) {
	svc := &captureSvc{}
	d := hydrationDeps(t, svc)
	conv := seedRoom(t, d.Convs, "room", "original")
	conv.SetRoute("gw1")
	conv.SetModelID("m1")
	conv.SetWorkspace("/ws/one")
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	driver := d.Environments[enum.EnvironmentLocal]
	model, ok := mustGatewayModel(t, d, "gw1", "m1")
	if !ok {
		t.Fatal("seed gateway missing m1")
	}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, model, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Messages()) != 3 {
		t.Fatalf("seeded history = %d, want 3", len(before.Messages()))
	}
	oldContextID := before.Messages()[1].ID()

	j := &Edit{Deps: d, MsgID: before.Messages()[0].ID(), Content: "edited"}
	j.Bind("room", nil)
	if err := j.Execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := after.Messages()
	if len(msgs) != 3 || msgs[0].Content() != "edited" {
		t.Fatalf("regenerated history = %+v", msgs)
	}
	requireWorkspaceContext(t, msgs[1], "/ws/one")
	if msgs[1].ID() == oldContextID {
		t.Fatal("the rewritten turn must mint a fresh workspace record, not keep the old one")
	}
	if got := countWorkspaceContexts(msgs); got != 1 {
		t.Fatalf("workspace records = %d, want exactly 1", got)
	}
	if msgs[2].Content() != "done" {
		t.Fatalf("regenerated answer = %q", msgs[2].Content())
	}
}

func mustGatewayModel(t *testing.T, d Deps, route, modelID string) (entity.Model, bool) {
	t.Helper()
	gw, err := d.Gateways.Load(t.Context(), route)
	if err != nil {
		return entity.Model{}, false
	}
	return gw.Model(modelID)
}

// dirProbe records the workspace dir bound into tool execution context —
// the same value the workspace record cites.
type dirProbe struct {
	schema valueobject.ToolSchema
	saw    string
}

func (t *dirProbe) Schema() valueobject.ToolSchema { return t.schema }
func (t *dirProbe) Execute(ctx context.Context, _ entity.ToolCall, _ dservice.ToolEmitter) (dservice.ToolResult, error) {
	t.saw = dservice.WorkspaceDir(ctx)
	return dservice.ToolResult{Text: "ok"}, nil
}

// dirProbeSvc asks for the probe tool on call 1 and answers on call 2.
type dirProbeSvc struct{ calls atomic.Int32 }

func (s *dirProbeSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	var m entity.Message
	if s.calls.Add(1) == 1 {
		m, _ = entity.NewMessage(enum.RoleAssistant, "need a tool")
		tc, err := entity.NewToolCall("call_1", "probe", `{}`, enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
	} else {
		m, _ = entity.NewMessage(enum.RoleAssistant, "done")
		m.SetFinishReason(enum.FinishReasonStop)
	}
	conv.Append(m)
	return nil
}

// wsImitator scripts the model imitating the hydration call it observed.
type wsImitator struct{ calls atomic.Int32 }

func (s *wsImitator) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	var m entity.Message
	if s.calls.Add(1) == 1 {
		m, _ = entity.NewMessage(enum.RoleAssistant, "checking the workspace")
		tc, err := entity.NewToolCall("call_1", "workspace", `{"path":"/somewhere"}`, enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
	} else {
		m, _ = entity.NewMessage(enum.RoleAssistant, "done")
		m.SetFinishReason(enum.FinishReasonStop)
	}
	conv.Append(m)
	return nil
}

// A model that imitates the workspace pin it observed gets the permission
// gate, not the executor: the call name resolves in the catalog but
// ToolPermissionSystem keeps it out of the agent's reach.
func TestWorkspaceImitationDeniedByPermissionGate(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	wsSchema, _ := valueobject.NewToolSchema("workspace", "hydration record", nil, true, enum.ToolPermissionSystem)
	driver := EnvironmentBundle{
		DefaultRoot: "/ws",
		Tools:       map[string]dservice.Tool{"workspace": &dirProbe{schema: wsSchema}},
	}
	conv := seedRoom(t, d.Convs, "room", "go")
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, &wsImitator{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	var denied bool
	for _, m := range msgs {
		for _, tc := range m.ToolCalls() {
			if tc.Name() == "workspace" && tc.Type() == enum.TooltypeOrigin {
				denied = true
				if tc.Output() != "error: tool not permitted" {
					t.Fatalf("imitated call output = %q, want the permission denial", tc.Output())
				}
			}
		}
	}
	if !denied {
		t.Fatal("imitated workspace call missing from history")
	}
}

// TurnWorkspace resolves one value per turn — the binding first, then the
// driver's default — and runTurn binds that single value into tool
// execution context (exec cwd included) and into the record's text.
func TestTurnWorkspaceBindingFallbackAndExecution(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	probeSchema, _ := valueobject.NewToolSchema("probe", "captures dir", nil, true, enum.ToolPermissionAgent)

	// No binding → the driver's default reaches the tool and the record.
	probe := &dirProbe{schema: probeSchema}
	driver := EnvironmentBundle{
		DefaultRoot: "/fallback/dir",
		Tools:       map[string]dservice.Tool{"probe": probe},
	}
	conv := seedRoom(t, d.Convs, "room", "go")
	if got := TurnWorkspace(conv, driver); got != "/fallback/dir" {
		t.Fatalf("unbound TurnWorkspace = %q, want the fallback", got)
	}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, &dirProbeSvc{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if probe.saw != "/fallback/dir" {
		t.Fatalf("tool ctx workspace = %q, want the fallback", probe.saw)
	}
	requireWorkspaceContext(t, conv.Messages()[1], "/fallback/dir")

	// A binding wins over the default for both consumers.
	probe2 := &dirProbe{schema: probeSchema}
	driver2 := EnvironmentBundle{
		DefaultRoot: "/fallback/dir",
		Tools:       map[string]dservice.Tool{"probe": probe2},
	}
	conv2 := seedRoom(t, d.Convs, "bound", "go")
	conv2.SetWorkspace("/bound/dir")
	if got := TurnWorkspace(conv2, driver2); got != "/bound/dir" {
		t.Fatalf("bound TurnWorkspace = %q, want the binding", got)
	}
	if err := d.runTurn(t.Context(), "bound", conv2, driver2, nil, entity.Model{}, &dirProbeSvc{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if probe2.saw != "/bound/dir" {
		t.Fatalf("tool ctx workspace = %q, want the binding", probe2.saw)
	}
	requireWorkspaceContext(t, conv2.Messages()[1], "/bound/dir")
}

// fatTool answers with an oversized result — enough context weight to
// trip the next step's budget check inside a tiny model window.
type fatTool struct{ schema valueobject.ToolSchema }

func (t fatTool) Schema() valueobject.ToolSchema { return t.schema }
func (t fatTool) Execute(context.Context, entity.ToolCall, dservice.ToolEmitter) (dservice.ToolResult, error) {
	return dservice.ToolResult{Text: strings.Repeat("x", 4000)}, nil
}

// foldWatchSvc serves the compaction scratch call (the summary tool is
// offered only there), asks for "push" on the first real call — whose
// fat result is what trips the budget — and on the second real call
// records whether a workspace record sat at the tail it was handed,
// proving the pin lands ahead of the provider call it feeds. failAt
// numbers a real call to fail with a bare 413 — the provider's own
// overflow verdict — appending nothing.
type foldWatchSvc struct {
	realCalls atomic.Int32
	failAt    int32 // real call number to 413; 0 = never
	tailWS    bool
}

func (s *foldWatchSvc) Send(_ context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	for _, tool := range conv.Tools() {
		if tool.Name() == toolsummary.ToolName {
			m, _ := entity.NewMessage(enum.RoleAssistant, "")
			tc, err := entity.NewToolCall("sum_1", toolsummary.ToolName, `{"text":"CHECKPOINT"}`, enum.TooltypeOrigin)
			if err != nil {
				return err
			}
			m.AddToolCall(tc)
			m.SetFinishReason(enum.FinishReasonToolCalls)
			conv.Append(m)
			return nil
		}
	}
	n := s.realCalls.Add(1)
	if n == s.failAt {
		return apperror.New(413, "context overflow", errors.New("too long"))
	}
	var m entity.Message
	if n == 1 {
		m, _ = entity.NewMessage(enum.RoleAssistant, "need a tool")
		tc, err := entity.NewToolCall("call_1", "push", `{}`, enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
	} else {
		if last := conv.Last(); last != nil {
			for _, tc := range last.ToolCalls() {
				if tc.Type() == enum.TooltypeInjection && tc.Name() == "workspace" {
					s.tailWS = true
				}
			}
		}
		m, _ = entity.NewMessage(enum.RoleAssistant, "done")
		m.SetFinishReason(enum.FinishReasonStop)
	}
	conv.Append(m)
	return nil
}

// The fold fires mid-turn on a tail that ends with the answered
// assistant call — the user line's record is sealed inside the
// checkpoint — so the turn re-pins the workspace for the provider call
// that follows: [checkpoint, answered call, fresh record, reply].
func TestRunTurnCompactRepinsWorkspaceContext(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4, CompactBudgetPct: 100, CompactKeepTail: 1},
	}
	conv := seedRoom(t, d.Convs, "room", "go")
	conv.SetWorkspace("/ws/repin")
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	// A window barely over the output reserve leaves a 500-token input
	// allowance: the seed turn fits, the fat tool result overflows it.
	model := entity.Model{}
	model.SetContextWindow(entity.DefaultUnknownMaxOutputTokens + 500)
	schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"push": fatTool{schema: schema}}}
	svc := &foldWatchSvc{}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, model, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 4 {
		t.Fatalf("history = %d msgs, want checkpoint+call+record+answer: %+v", len(msgs), msgs)
	}
	if msgs[0].UserType() != enum.LLMUserTypeSystem || len(msgs[0].ToolCalls()) != 0 {
		t.Fatalf("head = %+v, want the compaction checkpoint", msgs[0])
	}
	if msgs[1].Role() != enum.RoleAssistant || !msgs[1].ToolCalls()[0].HasOutput() {
		t.Fatalf("kept tail = %+v, want the answered assistant call", msgs[1])
	}
	requireWorkspaceContext(t, msgs[2], "/ws/repin")
	if msgs[3].Content() != "done" {
		t.Fatalf("final answer = %q", msgs[3].Content())
	}
	if !svc.tailWS {
		t.Fatal("the post-fold provider call saw no workspace record at the tail — the model ran blind")
	}
	if got := countWorkspaceContexts(msgs); got != 1 {
		t.Fatalf("workspace records = %d, want 1 — the sealed one stays inside the checkpoint", got)
	}
	stored, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages()) != 4 || countWorkspaceContexts(stored.Messages()) != 1 {
		t.Fatalf("persisted history = %d msgs %d records", len(stored.Messages()), countWorkspaceContexts(stored.Messages()))
	}
	requireWorkspaceContext(t, stored.Messages()[2], "/ws/repin")
}

// The provider's own 413 verdict folds mid-turn onto the same kind of
// non-editable tail — the kept assistant call — so the retried call
// must find the workspace re-pinned exactly as after a budget fold:
// [checkpoint, answered call, fresh record, reply].
func TestRunTurn413FoldRepinsWorkspaceContext(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4, CompactKeepTail: 1},
	}
	conv := seedRoom(t, d.Convs, "room", "go")
	conv.SetWorkspace("/ws/repin")
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"push": pushTool{schema: schema}}}
	// The second real call 413s into a fold whose kept tail is the
	// answered assistant call — the seed line's record is sealed.
	svc := &foldWatchSvc{failAt: 2}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 4 {
		t.Fatalf("history = %d msgs, want checkpoint+call+record+answer: %+v", len(msgs), msgs)
	}
	if msgs[0].UserType() != enum.LLMUserTypeSystem || len(msgs[0].ToolCalls()) != 0 {
		t.Fatalf("head = %+v, want the compaction checkpoint", msgs[0])
	}
	if msgs[1].Role() != enum.RoleAssistant || !msgs[1].ToolCalls()[0].HasOutput() {
		t.Fatalf("kept tail = %+v, want the answered assistant call", msgs[1])
	}
	requireWorkspaceContext(t, msgs[2], "/ws/repin")
	if msgs[3].Content() != "done" {
		t.Fatalf("retried reply = %q", msgs[3].Content())
	}
	if !svc.tailWS {
		t.Fatal("the retried provider call saw no workspace record at the tail — the model ran blind")
	}
	if got := countWorkspaceContexts(msgs); got != 1 {
		t.Fatalf("workspace records = %d, want 1 — the sealed one stays inside the checkpoint", got)
	}
	stored, err := d.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages()) != 4 || countWorkspaceContexts(stored.Messages()) != 1 {
		t.Fatalf("persisted history = %d msgs %d records", len(stored.Messages()), countWorkspaceContexts(stored.Messages()))
	}
	requireWorkspaceContext(t, stored.Messages()[2], "/ws/repin")
}

// A fold whose kept tail retains the live pin must NOT re-pin: the dedup
// scan sees the surviving same-directory record and suppresses the
// append — [checkpoint, kept pin, kept call, reply], one pin total.
func TestRunTurn413FoldKeepsPinSuppressesRepin(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4, CompactKeepTail: 2},
	}
	conv := seedRoom(t, d.Convs, "room", "go")
	conv.SetWorkspace("/ws/kept")
	if err := d.Convs.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"push": pushTool{schema: schema}}}
	svc := &foldWatchSvc{failAt: 2}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 4 {
		t.Fatalf("history = %d msgs, want checkpoint+pin+call+answer: %+v", len(msgs), msgs)
	}
	requireWorkspaceContext(t, msgs[1], "/ws/kept")
	if msgs[2].Role() != enum.RoleAssistant || !msgs[2].ToolCalls()[0].HasOutput() {
		t.Fatalf("kept tail = %+v, want the answered assistant call", msgs[2])
	}
	if msgs[3].Content() != "done" {
		t.Fatalf("retried reply = %q", msgs[3].Content())
	}
	if got := countWorkspaceContexts(msgs); got != 1 {
		t.Fatalf("workspace records = %d, want 1 — the kept pin suppresses a second", got)
	}
}

// The same loop shape without a fold keeps the old contract: a bare
// assistant tail is not pinned — the record stays behind the editable
// line that earned it.
func TestRunTurnNoFoldLeavesNonEditableTailUnpinned(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4, CompactBudgetPct: 100, CompactKeepTail: 1},
	}
	conv := seedRoom(t, d.Convs, "room", "go")
	schema, _ := valueobject.NewToolSchema("push", "test tool", nil, true, enum.ToolPermissionAgent)
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{"push": fatTool{schema: schema}}}
	svc := &foldWatchSvc{}
	// The default window is wide enough that the fat result never
	// reaches the budget — the fold branch never fires.
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, svc, nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 4 {
		t.Fatalf("history = %d msgs, want user+record+call+answer", len(msgs))
	}
	requireWorkspaceContext(t, msgs[1], "")
	if msgs[2].Role() != enum.RoleAssistant || msgs[3].Content() != "done" {
		t.Fatalf("tail = %+v, want the call then the answer with no record between", msgs[2:])
	}
	if svc.tailWS {
		t.Fatal("a non-editable tail was pinned without a fold — the re-pin belongs to the fold only")
	}
	if got := countWorkspaceContexts(msgs); got != 1 {
		t.Fatalf("workspace records = %d, want the 1 behind the user line", got)
	}
}

// A pin already live in the tail for the same directory is not repeated —
// an identical record per editable line spends tokens without telling the
// model anything new. A directory change, or a tail that lost the pin to
// compaction, still records.
func TestWorkspacePinSkipsWhenUnchanged(t *testing.T) {
	d := Deps{
		SummaryTool: mustSummaryTool(t),
		Convs:       repoconversation.NewConversationRepository(t.TempDir()),
		Config:      config.ConversationConfig{MaxTurnStep: 4},
	}
	driver := EnvironmentBundle{DefaultRoot: "/fallback/dir"}
	conv := seedRoom(t, d.Convs, "room", "first")
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, doneSvc{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	requireWorkspaceContext(t, conv.Messages()[1], "/fallback/dir")

	// A second turn over the same directory adds no second pin.
	user2, _ := entity.NewMessage(enum.RoleUser, "second")
	conv.Append(user2)
	if _, err := d.Convs.AppendMessage(t.Context(), "room", user2); err != nil {
		t.Fatal(err)
	}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, doneSvc{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := countWorkspaceContexts(conv.Messages()); got != 1 {
		t.Fatalf("pins = %d, want 1 — the live pin already cites this dir", got)
	}

	// A bound workspace change is a real change: the new dir pins again
	// and the newest record is what the wire cites.
	conv.SetWorkspace("/other/dir")
	user3, _ := entity.NewMessage(enum.RoleUser, "third")
	conv.Append(user3)
	if _, err := d.Convs.AppendMessage(t.Context(), "room", user3); err != nil {
		t.Fatal(err)
	}
	if err := d.runTurn(t.Context(), "room", conv, driver, nil, entity.Model{}, doneSvc{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := countWorkspaceContexts(conv.Messages()); got != 2 {
		t.Fatalf("pins = %d, want 2 — a dir change must re-record", got)
	}
	requireWorkspaceContext(t, conv.Messages()[len(conv.Messages())-2], "/other/dir")
}

// contextScript is an EnvironmentDriver whose ToolContexts returns a
// scripted record set — it exercises the job's append contract without a
// real hydrator.
type contextScript struct {
	EnvironmentBundle
	contexts []ToolContext
	err      error
}

func (s *contextScript) ToolContexts(context.Context) ([]ToolContext, error) {
	return s.contexts, s.err
}

// Each environment tool context lands as its own injection call under the
// real tool name, gathered on a single hidden system message per pass.
func TestAppendToolContextsRecordsEachCallOnOneMessage(t *testing.T) {
	d := Deps{Convs: repoconversation.NewConversationRepository(t.TempDir())}
	conv := seedRoom(t, d.Convs, "room", "go")
	driver := &contextScript{contexts: []ToolContext{
		{Name: "runtime_context", Input: `{"path":"/ws"}`, Output: `{"currentDate":"2026-01-01T00:00:00Z"}`},
		{Name: "skills", Input: `{"path":"/ws"}`, Output: "catalog"},
		{Name: "custom_tools", Input: `{"path":"/ws"}`, Output: "paths"},
	}}
	if err := d.appendToolContexts(t.Context(), conv.ID(), conv, driver); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 2 {
		t.Fatalf("history = %d messages, want 2", len(msgs))
	}
	record := msgs[1]
	if record.Role() != enum.RoleSystem {
		t.Fatalf("role = %s, want %s", record.Role(), enum.RoleSystem)
	}
	calls := record.ToolCalls()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	for i, want := range []struct{ name, input, output string }{
		{"runtime_context", `{"path":"/ws"}`, `{"currentDate":"2026-01-01T00:00:00Z"}`},
		{"skills", `{"path":"/ws"}`, "catalog"},
		{"custom_tools", `{"path":"/ws"}`, "paths"},
	} {
		call := calls[i]
		if call.Name() != want.name {
			t.Fatalf("call[%d].Name = %q, want %q", i, call.Name(), want.name)
		}
		if call.Input() != want.input {
			t.Fatalf("call[%d].Input = %q, want %q", i, call.Input(), want.input)
		}
		if call.Type() != enum.TooltypeInjection {
			t.Fatalf("call[%d].Type = %s, want %s", i, call.Type(), enum.TooltypeInjection)
		}
		if !strings.HasPrefix(call.ID(), "call_") {
			t.Fatalf("call[%d].ID = %q, want the minted call_ shape", i, call.ID())
		}
		if !call.HasOutput() || call.Output() != want.output {
			t.Fatalf("call[%d].Output = %q, want %q", i, call.Output(), want.output)
		}
		if call.OutputLatency() != "" {
			t.Fatalf("call[%d].Latency = %q, want empty — a stored record, not an execution", i, call.OutputLatency())
		}
	}
	stored, err := d.Convs.Load(t.Context(), conv.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages()) != 2 || len(stored.Messages()[1].ToolCalls()) != 3 {
		t.Fatal("the gathered record did not persist")
	}
}

// Volatile-only churn must not re-record: a tool that renders a fresh
// timestamp every pass supplies a Dedup normalizer and the job compares
// semantic content only. A semantic change still re-records — and
// persists the full-fidelity output, never the key.
func TestAppendToolContextsDedupsOnNormalizedOutput(t *testing.T) {
	d := Deps{Convs: repoconversation.NewConversationRepository(t.TempDir())}
	conv := seedRoom(t, d.Convs, "room", "go")

	dedup := func(out string) string {
		var snapshot map[string]any
		if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
			return out
		}
		delete(snapshot, "currentDate")
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return out
		}
		return string(raw)
	}
	contexts := func(date, env string) []ToolContext {
		return []ToolContext{{
			Name:   "runtime_context",
			Input:  `{"path":"/ws"}`,
			Output: fmt.Sprintf(`{"currentDate":%q,"environment":%q}`, date, env),
			Dedup:  dedup,
		}}
	}
	if err := d.appendToolContexts(t.Context(), conv.ID(), conv, &contextScript{contexts: contexts("2026-01-01T00:00:00Z", "nusallm")}); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Messages()); got != 2 {
		t.Fatalf("history = %d messages, want 2", got)
	}

	// Same semantic payload behind a fresh timestamp: still one record.
	if err := d.appendToolContexts(t.Context(), conv.ID(), conv, &contextScript{contexts: contexts("2026-01-01T00:00:13Z", "nusallm")}); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Messages()); got != 2 {
		t.Fatalf("volatile-only change re-recorded — history = %d, want 2", got)
	}

	// A semantic change re-records with the full output, timestamp and all.
	if err := d.appendToolContexts(t.Context(), conv.ID(), conv, &contextScript{contexts: contexts("2026-01-01T00:00:26Z", "nusallm 1.2")}); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 3 {
		t.Fatalf("semantic change skipped — history = %d, want 3", len(msgs))
	}
	call := msgs[2].ToolCalls()[0]
	if want := `{"currentDate":"2026-01-01T00:00:26Z","environment":"nusallm 1.2"}`; call.Output() != want {
		t.Fatalf("output = %q, want %q", call.Output(), want)
	}
}

// Only the records whose (name, input, normalized output) changed are
// carried on the next pass's message; same-name records dedup per input
// so one untouched record never anchors another same-name rescan.
func TestAppendToolContextsReRecordsOnlyChanged(t *testing.T) {
	d := Deps{Convs: repoconversation.NewConversationRepository(t.TempDir())}
	conv := seedRoom(t, d.Convs, "room", "go")
	pass := func(skills, pathsA, pathsB string) error {
		return d.appendToolContexts(t.Context(), conv.ID(), conv, &contextScript{contexts: []ToolContext{
			{Name: "skills", Input: `{"path":"/ws"}`, Output: skills},
			{Name: "custom_tools", Input: `{"path":"/ws/a"}`, Output: pathsA},
			{Name: "custom_tools", Input: `{"path":"/ws/b"}`, Output: pathsB},
		}})
	}
	if err := pass("catalog", "bin-a", "bin-b"); err != nil {
		t.Fatal(err)
	}
	if err := pass("catalog", "bin-a", "bin-b v2"); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 3 {
		t.Fatalf("history = %d messages, want 3", len(msgs))
	}
	calls := msgs[2].ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("re-recorded calls = %d, want 1", len(calls))
	}
	if calls[0].Name() != "custom_tools" || calls[0].Input() != `{"path":"/ws/b"}` {
		t.Fatalf("re-recorded %s %s, want custom_tools /ws/b", calls[0].Name(), calls[0].Input())
	}

	// Nothing changed at all → no new message.
	if err := pass("catalog", "bin-a", "bin-b v2"); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Messages()); got != 3 {
		t.Fatalf("unchanged pass re-recorded — history = %d, want 3", got)
	}
}

// An empty context set writes nothing, and a production error aborts the
// pass before any partial record is persisted.
func TestAppendToolContextsEmptyAndError(t *testing.T) {
	d := Deps{Convs: repoconversation.NewConversationRepository(t.TempDir())}
	conv := seedRoom(t, d.Convs, "room", "go")
	if err := d.appendToolContexts(t.Context(), conv.ID(), conv, &contextScript{}); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Messages()); got != 1 {
		t.Fatalf("empty contexts wrote a message — history = %d, want 1", got)
	}
	want := errors.New("production failed")
	if err := d.appendToolContexts(t.Context(), conv.ID(), conv, &contextScript{err: want}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if got := len(conv.Messages()); got != 1 {
		t.Fatalf("failed pass wrote a message — history = %d, want 1", got)
	}
}

// tickingTool renders a fresh timestamp every pass — the volatile-output
// shape that defeated verbatim dedup — but its DedupKey strips it so the
// record dedups on semantic content.
type tickingTool struct {
	schema valueobject.ToolSchema
	tick   int
}

func (t *tickingTool) Schema() valueobject.ToolSchema { return t.schema }
func (t *tickingTool) Execute(context.Context, entity.ToolCall, dservice.ToolEmitter) (dservice.ToolResult, error) {
	t.tick++
	return dservice.ToolResult{Text: fmt.Sprintf(`{"tick":%d,"payload":%q}`, t.tick, t.schema.Name())}, nil
}
func (t *tickingTool) DedupKey(output string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return output
	}
	delete(parsed, "tick")
	raw, err := json.Marshal(parsed)
	if err != nil {
		return output
	}
	return string(raw)
}

// The bundle executes every enabled system-callable tool sorted by name
// against the workspace pin input: an agent-only tool produces no
// context, a failing tool still returns a record carrying the error
// text, and a ToolDeduper tool attaches its normalizer. Two passes
// differing only in the volatile field dedup to one record.
func TestBundleToolContextsRunsSystemToolsSorted(t *testing.T) {
	d := Deps{Convs: repoconversation.NewConversationRepository(t.TempDir())}
	conv := seedRoom(t, d.Convs, "room", "go")
	sysSchema := func(name string) valueobject.ToolSchema {
		s, err := valueobject.NewToolSchema(name, "stub "+name, nil, true, enum.ToolPermissionSystem)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	agentSchema, err := valueobject.NewToolSchema("agent_only", "stub", nil, true, enum.ToolPermissionAgent)
	if err != nil {
		t.Fatal(err)
	}
	zeta := &tickingTool{schema: sysSchema("zeta_ctx")}
	driver := EnvironmentBundle{Tools: map[string]dservice.Tool{
		"zeta_ctx":   zeta,
		"alpha_ctx":  &tickingTool{schema: sysSchema("alpha_ctx")},
		"agent_only": &skillDiscovery{schema: agentSchema},
	}}
	ctx := dservice.WithWorkspaceDir(t.Context(), "/ws")
	contexts, err := driver.ToolContexts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 2 {
		t.Fatalf("contexts = %d, want 2 — the agent-only tool produces none", len(contexts))
	}
	if contexts[0].Name != "alpha_ctx" || contexts[1].Name != "zeta_ctx" {
		t.Fatalf("context order = %s, %s — want sorted by name", contexts[0].Name, contexts[1].Name)
	}
	if contexts[0].Input != `{"path":"/ws"}` {
		t.Fatalf("input = %q, want the workspace pin shape", contexts[0].Input)
	}
	for i, c := range contexts {
		if c.Dedup == nil {
			t.Fatalf("contexts[%d] lost its normalizer — a volatile-output tool must carry DedupKey", i)
		}
	}

	// Two passes differing only in the ticking field record once.
	if err := d.appendToolContexts(ctx, conv.ID(), conv, driver); err != nil {
		t.Fatal(err)
	}
	if err := d.appendToolContexts(ctx, conv.ID(), conv, driver); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if len(msgs) != 2 || len(msgs[1].ToolCalls()) != 2 {
		t.Fatalf("volatile churn re-recorded — history = %d messages", len(msgs))
	}
	if zeta.tick < 2 {
		t.Fatalf("dedup skipped execution — tick = %d, want a render per pass", zeta.tick)
	}

	// A dead ctx aborts the pass before any record lands.
	dead, cancel := context.WithCancel(t.Context())
	cancel()
	if err := d.appendToolContexts(dead, conv.ID(), conv, driver); err != context.Canceled {
		t.Fatalf("cancel = %v", err)
	}
	if got := len(conv.Messages()); got != 2 {
		t.Fatalf("canceled pass wrote a message — history = %d, want 2", got)
	}
}
