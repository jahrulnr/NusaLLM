package tui

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	repohistory "github.com/jahrulnr/nusallm/infrastructure/repository/history"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
)

// gatedToolSvc parks inside the first provider call until released,
// then asks for the echo tool — the deterministic live window for a
// steer. Later calls record the last two message contents they saw —
// the folded steer and the context record that trails it.
type gatedToolSvc struct {
	entered  chan struct{}
	release  chan struct{}
	calls    int32
	lastSeen string
	prevSeen string
}

func (s *gatedToolSvc) Send(ctx context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	n := atomic.AddInt32(&s.calls, 1)
	var m entity.Message
	if n == 1 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		m, _ = entity.NewMessage(enum.RoleAssistant, "need a tool")
		tc, err := entity.NewToolCall("call_1", "echo", `{}`, enum.TooltypeOrigin)
		if err != nil {
			return err
		}
		m.AddToolCall(tc)
		m.SetFinishReason(enum.FinishReasonToolCalls)
	} else {
		msgs := conv.Messages()
		s.lastSeen = msgs[len(msgs)-1].Content()
		s.prevSeen = msgs[len(msgs)-2].Content()
		m, _ = entity.NewMessage(enum.RoleAssistant, "done")
		m.SetFinishReason(enum.FinishReasonStop)
	}
	conv.Append(m)
	return nil
}

// gatedTextSvc parks inside the first provider call until released and
// answers "done" — the turn ends without a tool step, so a steer pushed
// during the gate is never consumed. Each call records the last two
// message contents — a fresh user line always trails a context record.
type gatedTextSvc struct {
	entered  chan struct{}
	release  chan struct{}
	calls    int32
	lastSeen string
	prevSeen string
}

func (s *gatedTextSvc) Send(ctx context.Context, conv *aggregate.Conversation, _ *aggregate.Gateway, _ entity.Model, _ event.TurnEventHandler) error {
	n := atomic.AddInt32(&s.calls, 1)
	if n == 1 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	msgs := conv.Messages()
	s.lastSeen = msgs[len(msgs)-1].Content()
	if len(msgs) > 1 {
		s.prevSeen = msgs[len(msgs)-2].Content()
	}
	m, _ := entity.NewMessage(enum.RoleAssistant, "done")
	m.SetFinishReason(enum.FinishReasonStop)
	conv.Append(m)
	return nil
}

type fixedTool struct{ name string }

func (t fixedTool) Schema() valueobject.ToolSchema {
	s, _ := valueobject.NewToolSchema(t.name, "test tool", nil, true, enum.ToolPermissionAgent)
	return s
}
func (fixedTool) Execute(context.Context, entity.ToolCall, dservice.ToolEmitter) (dservice.ToolResult, error) {
	return dservice.ToolResult{Text: "tool output"}, nil
}

// steerModel builds the real app→session→REPL→model stack against a
// scripted service — steering travels the same path the running binary
// uses, minus the terminal.
func steerModel(t *testing.T, svc dservice.TextModelService, tools map[string]dservice.Tool) (replModel, *repoconversation.ConversationRepository) {
	t.Helper()
	storage := t.TempDir()
	secrets, err := reposecret.New(filepath.Join(storage, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	gatewayRepository := repogateway.NewGatewayRepository(filepath.Join(storage, "gateways"), secrets)
	gatewayApplication := appgateway.New(gatewayRepository, secrets, nil)
	gw, _ := aggregate.NewGateway("gw1", "stub", "http://unused", enum.StyleCompletion)
	gw.DisableStream()
	mdl, _ := entity.NewModel("m1", "gw1")
	gw.SetModels([]entity.Model{mdl})
	if err := gatewayRepository.Save(t.Context(), &gw); err != nil {
		t.Fatal(err)
	}
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: svc},
			Tools:    tools,
		}},
		config.NewConfig(), nil, "", nil, nil)
	t.Cleanup(func() { _ = conversationApplication.Shutdown(context.Background()) })

	var schemas []valueobject.ToolSchema
	for _, tool := range tools {
		schemas = append(schemas, tool.Schema())
	}
	seed, _ := aggregate.NewConversation("room")
	seed.SetRoute("gw1")
	seed.SetModelID("m1")
	seed.SetTools(schemas)
	if err := conversationRepository.Save(t.Context(), seed); err != nil {
		t.Fatal(err)
	}

	sess, err := session.New(session.Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      t.TempDir(),
		WorkspaceStore: repoworkspace.NewLocal(),
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.ConversationID = "room"
	sess.Route = "gw1"
	sess.ModelID = "m1"
	m := newModel(&REPL{Session: sess, historyFile: filepath.Join(storage, "cli", "history"), historyStore: repohistory.NewLocal()}, true)
	m.ctx = t.Context()
	m.runtime = &tuiRuntime{}
	return m, conversationRepository
}

// driveCmd executes a command tree the way the program's executor
// would — flattening Sequence order and Batch fan-out — and reports
// every produced message on sink.
func driveCmd(cmd tea.Cmd, sink chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var wg sync.WaitGroup
		for _, c := range msg {
			wg.Add(1)
			go func(c tea.Cmd) {
				defer wg.Done()
				driveCmd(c, sink)
			}(c)
		}
		wg.Wait()
	default:
		if seq, ok := asSequence(msg); ok {
			for _, c := range seq {
				driveCmd(c, sink)
			}
			return
		}
		sink <- msg
	}
}

// sequenceMsg is bubbletea's unexported ordered-cmd carrier — unwrap it
// by shape (a slice of cmds), not by name.
func asSequence(msg tea.Msg) ([]tea.Cmd, bool) {
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Type().Name() != "sequenceMsg" {
		return nil, false
	}
	cmds := make([]tea.Cmd, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
			cmds = append(cmds, c)
		}
	}
	return cmds, true
}

// runTurnCmd drives a command tree and reports the first terminal turn
// message — ticks, print acks, and stray events are skipped.
func runTurnCmd(cmd tea.Cmd) <-chan tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() {
		msgs := make(chan tea.Msg, 16)
		go driveCmd(cmd, msgs)
		for {
			switch m := (<-msgs).(type) {
			case sendDoneMsg, jobDoneMsg:
				done <- m
				return
			}
		}
	}()
	return done
}

func awaitMsg(t *testing.T, ch <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("turn never finished")
		return nil
	}
}

// /steer during a tool-gated turn travels through the session into the
// running job's inbox, folds after the tool result, and the fold event
// clears the pending tracking — Esc can no longer recall it.
func TestSteerFoldsThroughSession(t *testing.T) {
	svc := &gatedToolSvc{entered: make(chan struct{}), release: make(chan struct{})}
	m, convs := steerModel(t, svc, map[string]dservice.Tool{"echo": fixedTool{name: "echo"}})

	done := runTurnCmd(m.beginTurn("go"))
	<-svc.entered

	nm, _ := m.submit("/steer check X")
	m = nm.(replModel)
	if len(m.pendingSteer) != 1 || m.pendingSteer[0] != "check X" {
		t.Fatalf("pendingSteer = %v", m.pendingSteer)
	}

	close(svc.release)
	finished := awaitMsg(t, done)

	// On a live program the fold event arrives ahead of the done
	// message — feed it the same way the pump would.
	nm, _ = m.Update(turnEventMsg{ev: event.NewSteerEvent("check X")})
	m = nm.(replModel)
	if len(m.pendingSteer) != 0 {
		t.Fatalf("folded steer still tracked: %v", m.pendingSteer)
	}

	conv, err := convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	// user "go" / workspace record / assistant+echo call / folded steer /
	// assistant "done" — the unchanged dir pin is not repeated.
	if len(msgs) != 5 || msgs[3].Role() != enum.RoleUser || msgs[3].Content() != "check X" {
		t.Fatalf("steer not in history: %d msgs", len(msgs))
	}
	if svc.prevSeen != "need a tool" || svc.lastSeen != "check X" {
		t.Fatalf("provider saw prev=%q last=%q, want the tool turn then the steer", svc.prevSeen, svc.lastSeen)
	}

	// finishTurn drains nothing — the landed steer must not requeue.
	out := finished.(sendDoneMsg)
	nm, _ = m.finishTurn(out.result, out.err)
	m = nm.(replModel)
	if len(m.queued) != 0 {
		t.Fatalf("landed steer requeued: %v", m.queued)
	}
}

// Esc peels steer → queue → turn: each press restores one pending line
// to the composer, and only once nothing is pending does it interrupt.
func TestEscRecallsSteerThenQueueThenCancels(t *testing.T) {
	svc := &gatedTextSvc{entered: make(chan struct{}), release: make(chan struct{})}
	m, _ := steerModel(t, svc, nil)

	done := runTurnCmd(m.beginTurn("go"))
	<-svc.entered

	nm, _ := m.submit("queue me")
	m = nm.(replModel)
	nm, _ = m.submit("/steer steer me")
	m = nm.(replModel)
	if len(m.queued) != 1 || len(m.pendingSteer) != 1 {
		t.Fatalf("queue=%v steer=%v", m.queued, m.pendingSteer)
	}

	// Esc #1 takes the pending steer back into the composer.
	nm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	m = nm.(replModel)
	if got := m.textarea.Value(); got != "steer me" {
		t.Fatalf("esc #1 composer = %q, want the recalled steer", got)
	}
	if len(m.pendingSteer) != 0 {
		t.Fatalf("steer still pending after recall: %v", m.pendingSteer)
	}

	// Esc #2 — composer cleared by the user — pops the queued line.
	m.textarea.Reset()
	nm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	m = nm.(replModel)
	if got := m.textarea.Value(); got != "queue me" {
		t.Fatalf("esc #2 composer = %q, want the recalled queue line", got)
	}
	if len(m.queued) != 0 {
		t.Fatalf("queue still holds the recalled line: %v", m.queued)
	}

	// Esc #3 — composer non-empty — falls through to turn cancel.
	nm, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	m = nm.(replModel)
	finished := awaitMsg(t, done)
	out := finished.(sendDoneMsg)
	if out.err == nil {
		t.Fatal("esc #3 must interrupt the running turn")
	}
}

// A steer delivered while the turn is already past its last provider
// call is never consumed — finishTurn requeues it as an ordinary
// message instead of dropping the line.
func TestSteerMissQueuesAsMessage(t *testing.T) {
	svc := &gatedTextSvc{entered: make(chan struct{}), release: make(chan struct{})}
	m, _ := steerModel(t, svc, nil)

	done := runTurnCmd(m.beginTurn("go"))
	<-svc.entered

	nm, _ := m.submit("/steer late input")
	m = nm.(replModel)
	if len(m.pendingSteer) != 1 {
		t.Fatalf("pendingSteer = %v", m.pendingSteer)
	}

	close(svc.release) // call 1 answers "done" — no fold point remains
	finished := awaitMsg(t, done)
	out := finished.(sendDoneMsg)
	nm, cmd := m.finishTurn(out.result, out.err)
	m = nm.(replModel)
	if len(m.pendingSteer) != 0 {
		t.Fatalf("missed steer still tracked: %v", m.pendingSteer)
	}
	if !m.turnActive {
		t.Fatal("the requeued steer must launch a new turn")
	}
	next := awaitMsg(t, runTurnCmd(cmd))
	if _, ok := next.(sendDoneMsg); !ok {
		t.Fatalf("requeued turn ended with %T", next)
	}
	if svc.prevSeen != "done" || svc.lastSeen != "late input" {
		t.Fatalf("requeued steer reached provider as prev=%q last=%q, want the answer then the steer text", svc.prevSeen, svc.lastSeen)
	}
}

// With no turn running, /steer degrades to an ordinary message — the
// intent still reaches the model — and bare /steer prints usage.
func TestSteerWithoutTurnDegrades(t *testing.T) {
	svc := &gatedTextSvc{entered: make(chan struct{}), release: make(chan struct{})}
	close(svc.release) // no gate — calls answer immediately
	m, _ := steerModel(t, svc, nil)

	cmd := m.dispatch("/steer hello there")
	finished := awaitMsg(t, runTurnCmd(cmd))
	out := finished.(sendDoneMsg)
	if out.err != nil {
		t.Fatalf("degraded steer send: %v", out.err)
	}
	if svc.prevSeen != "hello there" || svc.lastSeen != "" {
		t.Fatalf("provider saw prev=%q last=%q, want the steered text then its context record", svc.prevSeen, svc.lastSeen)
	}

	m.outbox = nil
	m.dispatch("/steer")
	if len(m.outbox) != 1 || m.outbox[0] == "" {
		t.Fatalf("bare /steer must print usage, outbox=%v", m.outbox)
	}
}
