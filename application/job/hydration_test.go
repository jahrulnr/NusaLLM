package job

import (
	"context"
	"encoding/json"
	"errors"
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
)

// requireWorkspaceContext asserts msg is the workspace context record the
// turn loop appends after an editable user line: a non-editable system
// user message carrying one injection-typed `workspace` call whose JSON
// input names dir and whose stored output is exactly `workspace changed
// to <dir-or-(unset)>` — no measured latency.
func requireWorkspaceContext(t *testing.T, msg entity.Message, dir string) {
	t.Helper()
	if msg.Role() != enum.RoleUser || msg.Editable() {
		t.Fatalf("workspace context = %v editable=%v, want a non-editable user record", msg.Role(), msg.Editable())
	}
	if msg.Content() != "" || msg.UserType() != enum.LLMUserTypeSystem {
		t.Fatalf("workspace context content=%q userType=%q", msg.Content(), msg.UserType())
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

// countWorkspaceContexts tallies the injection records in a history — the
// turn may append exactly one per editable line.
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
		Convs:    repoconversation.NewConversationRepository(t.TempDir()),
		Gateways: gateways,
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
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
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
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
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
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
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
		if tool.Name() == SummaryToolName {
			m, _ := entity.NewMessage(enum.RoleAssistant, "")
			tc, err := entity.NewToolCall("sum_1", SummaryToolName, `{"text":"CHECKPOINT"}`, enum.TooltypeOrigin)
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
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4, CompactKeepTail: 1},
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
	j.Bind("room")
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

// TurnWorkspace resolves one value per turn — the binding first, then the
// driver's default — and runTurn binds that single value into tool
// execution context (exec cwd included) and into the record's text.
func TestTurnWorkspaceBindingFallbackAndExecution(t *testing.T) {
	d := Deps{
		Convs:  repoconversation.NewConversationRepository(t.TempDir()),
		Config: config.ConversationConfig{MaxTurnStep: 4},
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
