package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	repohistory "github.com/jahrulnr/nusallm/infrastructure/repository/history"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	repoworkspace "github.com/jahrulnr/nusallm/infrastructure/repository/workspace"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	textcompletion "github.com/jahrulnr/nusallm/infrastructure/service/core/text/completion"
	toolexec "github.com/jahrulnr/nusallm/infrastructure/service/tool/terminal"
)

func TestREPLSendsListsAndResumesConversation(t *testing.T) {
	requests := make(chan []byte, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		requests <- raw
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "pong", "reasoning_content": "private chain of thought"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		}); err != nil {
			t.Errorf("write provider response: %v", err)
		}
	}))
	defer server.Close()

	storage := t.TempDir()
	secrets, err := reposecret.New(filepath.Join(storage, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	gatewayRepository := repogateway.NewGatewayRepository(filepath.Join(storage, "gateways"), secrets)
	gatewayApplication := appgateway.New(gatewayRepository, secrets, nil)
	key := "local-test-key"
	stream := false
	if err := gatewayApplication.Upsert(t.Context(), appgateway.GatewayInput{
		ID: "gw1", Name: "Test gateway", Style: enum.StyleCompletion, Endpoint: server.URL,
		ApiKey: &key, Stream: &stream,
		Models: []appgateway.ModelInput{{ID: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := gatewayApplication.Upsert(t.Context(), appgateway.GatewayInput{
		ID: "gw2", Name: "Alternate gateway", Style: enum.StyleCompletion, Endpoint: server.URL,
		Stream: &stream, Models: []appgateway.ModelInput{{ID: "m2", InputModalities: []enum.Modality{enum.ModalityText}}},
	}); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "shot.png"), pngFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(coretext.Options{FileRoot: workspace, FileStore: repoworkspace.NewLocal()})},
		}},
		config.NewConfig(),
		nil,
		"",
	)
	var output, stderr bytes.Buffer
	input := pipeInput(t, "/route gw1\n/model m1\n/attach shot.png\nhello\n/list\n/history 10\n/exit\n")
	repl, err := NewREPL(Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      workspace,
		WorkspaceStore: repoworkspace.NewLocal(),
		HistoryDir:     filepath.Join(storage, "cli"),
		HistoryStore:   repohistory.NewLocal(),
		Stdin:          input,
		Stdout:         &output,
		Stderr:         &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repl.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := repl.Close(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected CLI error: %s", stderr.String())
	}
	historyInfo, err := os.Stat(filepath.Join(storage, "cli", "history"))
	if err != nil {
		t.Fatal(err)
	}
	if historyInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("CLI history permissions = %04o, want private", historyInfo.Mode().Perm())
	}
	if repl.ConversationID == "" {
		t.Fatal("send did not bind the minted conversation id")
	}
	stored, err := conversationRepository.Load(t.Context(), repl.ConversationID)
	if err != nil {
		t.Fatalf("conversation not persisted: %v", err)
	}
	if len(stored.Messages()) == 0 || len(stored.Messages()[0].Attachments()) != 1 || stored.Messages()[0].Attachments()[0].SHA256() == "" {
		t.Fatalf("user attachment did not persist with its content pin: %+v", stored.Messages())
	}
	if request := string(<-requests); !strings.Contains(request, "data:image/png;base64,") {
		t.Fatalf("attached image not materialized in provider request: %s", request)
	}
	for _, want := range []string{"route set to gw1", "model set to m1", "attached shot.png", "private chain of thought", "pong", "messages=2", "> hello", "10 in · 5 out · 15 total"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("REPL output missing %q:\n%s", want, output.String())
		}
	}

	var resumedOutput, resumedError bytes.Buffer
	resumed, err := NewREPL(Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      workspace,
		WorkspaceStore: repoworkspace.NewLocal(),
		HistoryDir:     filepath.Join(storage, "cli"),
		HistoryStore:   repohistory.NewLocal(),
		Stdin:          pipeInput(t, "/resume "+repl.ConversationID+"\n/edit "+stored.Messages()[0].ID()+" updated user text\n/route gw2\n/new\n/model m2\n/attach shot.png\ntext-only turn\n/exit\n"),
		Stdout:         &resumedOutput,
		Stderr:         &resumedError,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumedOutput.String(), "resumed "+repl.ConversationID) {
		t.Fatalf("conversation resume failed: id=%q output=%q", resumed.ConversationID, resumedOutput.String())
	}
	for _, want := range []string{"> hello", "pong"} {
		if !strings.Contains(resumedOutput.String(), want) {
			t.Errorf("resumed output missing %q:\n%s", want, resumedOutput.String())
		}
	}
	if strings.Contains(resumedOutput.String(), "Assistant") || strings.Contains(resumedOutput.String(), "You") {
		t.Fatalf("chat transcript should not contain role labels:\n%s", resumedOutput.String())
	}
	updated, err := conversationRepository.Load(t.Context(), repl.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Messages()[0].Content() != "updated user text" {
		t.Fatalf("edit did not rewrite user message: %q", updated.Messages()[0].Content())
	}
	if resumed.ConversationID == "" || resumed.ConversationID == repl.ConversationID || resumed.Route != "gw2" || resumed.ModelID != "m2" {
		t.Fatalf("new conversation binding = id:%q route:%q model:%q", resumed.ConversationID, resumed.Route, resumed.ModelID)
	}
	<-requests // edit regeneration
	unsupported := string(<-requests)
	if strings.Contains(unsupported, "data:image/png;base64,") || !strings.Contains(unsupported, "attachment omitted") {
		t.Fatalf("unsupported media should be a placeholder: %s", unsupported)
	}
	if !strings.Contains(resumedError.String(), "will be omitted") {
		t.Fatalf("CLI must warn when model cannot accept attached media: %s", resumedError.String())
	}
	if err := conversationApplication.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// safeBuffer collects renderer output across goroutines.
type safeBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (w *safeBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *safeBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}

// TestAutoSelectNotifyDoesNotDeadlock guards the self-send deadlock: the
// session Notify hook fires inside Update (ResolveRoute auto-select runs
// on the loop), and a Notify that channel-sends into the program would
// block the loop on a channel only it reads — the first submit on a TTY
// froze the whole REPL, ctrl+c included. Notes must queue on the runtime
// and drain through the outbox instead.
func TestAutoSelectNotifyDoesNotDeadlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "pong"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer server.Close()

	storage := t.TempDir()
	secrets, err := reposecret.New(filepath.Join(storage, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	gatewayRepository := repogateway.NewGatewayRepository(filepath.Join(storage, "gateways"), secrets)
	gatewayApplication := appgateway.New(gatewayRepository, secrets, nil)
	key := "local-test-key"
	stream := false
	// Exactly one gateway with one model — the first send auto-selects
	// both, firing Notify twice on the update loop.
	if err := gatewayApplication.Upsert(t.Context(), appgateway.GatewayInput{
		ID: "gw1", Name: "Test gateway", Style: enum.StyleCompletion, Endpoint: server.URL,
		ApiKey: &key, Stream: &stream,
		Models: []appgateway.ModelInput{{ID: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(coretext.Options{FileRoot: workspace, FileStore: repoworkspace.NewLocal()})},
		}},
		config.NewConfig(),
		nil,
		"",
	)

	sess, err := session.New(session.Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      workspace,
		WorkspaceStore: repoworkspace.NewLocal(),
	})
	if err != nil {
		t.Fatal(err)
	}
	repl := &REPL{Session: sess, historyFile: filepath.Join(storage, "cli", "history"), historyStore: repohistory.NewLocal()}
	m := newModel(repl, true)
	m.ctx = t.Context()
	runtime := &tuiRuntime{}
	m.runtime = runtime
	sess.Notify = func(s string) { runtime.note(term.Dim + s + term.Reset) }

	var out safeBuffer
	inR, inW := io.Pipe()
	defer inW.Close()
	program := tea.NewProgram(m, tea.WithInput(inR), tea.WithOutput(&out))
	runtime.program = program
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	t.Cleanup(func() { program.Kill() })

	program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	program.Send(tea.KeyMsg{Type: tea.KeyEnter})

	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(out.String(), "pong") {
		if time.Now().After(deadline) {
			t.Fatalf("turn never completed — update loop deadlocked; output=%q", out.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "auto-selected route gw1") ||
		!strings.Contains(out.String(), "auto-selected model m1") {
		t.Fatalf("auto-select notices never reached scrollback: %q", out.String())
	}

	// "pong" reaching the output is not proof the turn finished — the
	// live region repaints deltas while the send is still in flight, and
	// a ctrl+c that lands then is consumed as cancel-turn, not quit. The
	// footer's context badge only exists once finishTurn has bound the
	// conversation and refreshed the stats, so it is the deterministic
	// idle marker to wait on before interrupting.
	for !strings.Contains(out.String(), " Tokens") {
		if time.Now().After(deadline) {
			t.Fatalf("turn never reached idle — output=%q", out.String())
		}
		time.Sleep(25 * time.Millisecond)
	}

	// ctrl+c on an idle composer still quits — input stays live.
	program.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("program did not exit on ctrl+c")
	}
	if err := conversationApplication.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestPipedCRNeverTriggersPicker guards the non-interactive contract:
// palette state must not exist off a TTY. A piped "\r" maps to KeyEnter,
// and if arg-mode rows answered it the submitted text would be the top
// fuzzy match — "/delete \r" would delete a DIFFERENT conversation than
// the registry's bare-form target. Piped input must submit the literal
// buffer.
func TestPipedCRNeverTriggersPicker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "pong"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer server.Close()

	storage := t.TempDir()
	secrets, err := reposecret.New(filepath.Join(storage, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	gatewayRepository := repogateway.NewGatewayRepository(filepath.Join(storage, "gateways"), secrets)
	gatewayApplication := appgateway.New(gatewayRepository, secrets, nil)
	stream := false
	if err := gatewayApplication.Upsert(t.Context(), appgateway.GatewayInput{
		ID: "gw1", Name: "Test gateway", Style: enum.StyleCompletion, Endpoint: server.URL,
		Stream: &stream, Models: []appgateway.ModelInput{{ID: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(coretext.Options{FileRoot: t.TempDir(), FileStore: repoworkspace.NewLocal()})},
		}},
		config.NewConfig(),
		nil,
		"",
	)
	t.Cleanup(func() { _ = conversationApplication.Shutdown(context.Background()) })

	// Seed one saved conversation, then close that session.
	var seedOutput bytes.Buffer
	seed, err := NewREPL(Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      t.TempDir(),
		WorkspaceStore: repoworkspace.NewLocal(),
		HistoryDir:     filepath.Join(storage, "cli"),
		HistoryStore:   repohistory.NewLocal(),
		Stdin:          pipeInput(t, "hello\n/exit\n"),
		Stdout:         &seedOutput,
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()
	convID := seed.ConversationID
	if convID == "" {
		t.Fatal("seed send did not bind a conversation")
	}

	// Fresh piped session: "/delete \r\n" — the \r maps to KeyEnter. With
	// the picker gated off, the literal buffer reaches /delete bare and
	// errors "no active conversation"; ungated, arg-mode rows would
	// submit the fuzzy top row and the seeded room would be gone.
	var output bytes.Buffer
	repl, err := NewREPL(Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      t.TempDir(),
		WorkspaceStore: repoworkspace.NewLocal(),
		HistoryDir:     filepath.Join(storage, "cli"),
		HistoryStore:   repohistory.NewLocal(),
		Stdin:          pipeInput(t, "/delete \r\n/exit\n"),
		Stdout:         &output,
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repl.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = repl.Close()

	if !strings.Contains(output.String(), "no active conversation") {
		t.Fatalf("piped /delete must hit the registry bare path: %q", output.String())
	}
	if _, err := conversationRepository.Info(t.Context(), convID); err != nil {
		t.Fatalf("piped \\r deleted a fuzzy-match conversation: %v", err)
	}
}

// Exit waits for scrollback: a print still travelling to the event loop
// must land before the quit, or the line that produced it vanishes
// (bubbletea stops the loop on QuitMsg and never handles the pending
// print). The exit is finished by the print ack instead.
func TestExitWaitsForPendingScrollback(t *testing.T) {
	repl := &REPL{Session: &session.Session{}}
	m := &replModel{repl: repl, live: &strings.Builder{}}
	m.println("queued line")
	repl.Exit = true
	if cmd := m.drain(); cmd == nil {
		t.Fatal("drain with a pending line must return the flush sequence")
	}
	if m.pendingPrints != 1 {
		t.Fatalf("pendingPrints = %d, want the scrollback print tracked", m.pendingPrints)
	}
	_, ack := m.Update(printAckMsg{})
	if ack == nil {
		t.Fatal("the acknowledged print must finish the exit")
	}
	if msg := ack(); msg == nil {
		t.Fatal("ack cmd returned no message")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("ack of the last print → %T, want tea.QuitMsg", msg)
	}
}

// TestConversationArgPicker guards the picker flow: a bare completable
// command on a TTY must not hard-fail with a usage error (or blindly
// delete the active room) — the composer parks at the argument position
// and the palette lists the candidates to pick.
func TestConversationArgPicker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "pong"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer server.Close()

	storage := t.TempDir()
	secrets, err := reposecret.New(filepath.Join(storage, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	gatewayRepository := repogateway.NewGatewayRepository(filepath.Join(storage, "gateways"), secrets)
	gatewayApplication := appgateway.New(gatewayRepository, secrets, nil)
	stream := false
	if err := gatewayApplication.Upsert(t.Context(), appgateway.GatewayInput{
		ID: "gw1", Name: "Test gateway", Style: enum.StyleCompletion, Endpoint: server.URL,
		Stream: &stream, Models: []appgateway.ModelInput{{ID: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "inner.md"), []byte("i"), 0o600); err != nil {
		t.Fatal(err)
	}
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(coretext.Options{FileRoot: workspace, FileStore: repoworkspace.NewLocal()})},
		}},
		config.NewConfig(),
		nil,
		"",
	)
	t.Cleanup(func() { _ = conversationApplication.Shutdown(context.Background()) })

	sess, err := session.New(session.Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      workspace,
		WorkspaceStore: repoworkspace.NewLocal(),
		Stderr:         io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := sess.Send(t.Context(), "hello", "gw1", "m1", nil)
	if err != nil || !result.UserMessagePersisted {
		t.Fatalf("seed send: %v", err)
	}
	sess.ConversationID = "" // fresh session, one saved conversation

	m := newModel(&REPL{Session: sess, historyFile: filepath.Join(storage, "cli", "history"), historyStore: repohistory.NewLocal()}, true)
	m.ctx = t.Context()

	// An in-progress draft must survive a parked picker — it lands in
	// history, recoverable via ↑.
	m.textarea.SetValue("unfinished draft")
	m.dispatch("/resume")
	if m.history[len(m.history)-1] != "unfinished draft" {
		t.Fatalf("parked picker clobbered the draft: history=%v", m.history)
	}
	if got := m.textarea.Value(); got != "/resume " {
		t.Fatalf("bare /resume composer = %q, want the picker-open state", got)
	}
	rows := m.palette.rows(t.Context(), "/resume ")
	if len(rows) != 1 || rows[0].Usage != "/resume "+result.ConversationID {
		t.Fatalf("picker rows = %+v, want the saved conversation", rows)
	}
	if !m.palette.arg {
		t.Fatal("picker rows must be argument completions, not command rows")
	}

	// Entering a row submits the full command line — the session binds.
	m.dispatch(rows[0].Usage)
	if sess.ConversationID != result.ConversationID {
		t.Fatalf("picked conversation did not bind: %q", sess.ConversationID)
	}

	// /route and /model park the same way — the picker replaces the
	// printed list on a TTY.
	m.dispatch("/route")
	if got := m.textarea.Value(); got != "/route " {
		t.Fatalf("bare /route composer = %q, want picker-open state", got)
	}
	m.dispatch("/route gw1")
	if sess.Route != "gw1" {
		t.Fatalf("picked route = %q", sess.Route)
	}
	m.dispatch("/model")
	m.dispatch("/model m1")
	if sess.ModelID != "m1" {
		t.Fatalf("picked model = %q", sess.ModelID)
	}

	// /edit lists editable messages; picking one only fills the line —
	// the replacement text still has to be typed.
	m.dispatch("/edit")
	if got := m.textarea.Value(); got != "/edit " {
		t.Fatalf("bare /edit composer = %q, want picker-open state", got)
	}
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(replModel)
	if got := m.textarea.Value(); !strings.HasPrefix(got, "/edit m_") || !strings.HasSuffix(got, " ") {
		t.Fatalf("edit pick composer = %q, want \"/edit <id> \" fill state", got)
	}

	// /attach on a directory row fills AND keeps listing — the palette
	// must recompute open, not dismiss into a dead end.
	m.textarea.SetValue("/attach d")
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(replModel)
	if got := m.textarea.Value(); got != "/attach docs/" {
		t.Fatalf("dir row fill = %q, want /attach docs/", got)
	}
	if rows := m.palette.rows(t.Context(), "/attach docs/"); len(rows) != 1 || rows[0].Usage != "/attach docs/inner.md" {
		t.Fatalf("descended attach rows = %+v, want docs/inner.md", rows)
	}

	// /delete gets the same picker instead of blindly deleting the
	// active conversation — choosing the row removes that record.
	m.dispatch("/delete")
	if got := m.textarea.Value(); got != "/delete " {
		t.Fatalf("bare /delete composer = %q, want the picker-open state", got)
	}
	rows = m.palette.rows(t.Context(), "/delete ")
	if len(rows) != 1 || rows[0].Usage != "/delete "+result.ConversationID {
		t.Fatalf("delete picker rows = %+v, want the saved conversation", rows)
	}
	m.dispatch(rows[0].Usage)
	if sess.ConversationID != "" {
		t.Fatal("deleting the bound conversation must unbind it")
	}
	if _, err := conversationRepository.Info(t.Context(), result.ConversationID); err == nil {
		t.Fatal("picked conversation still readable after /delete")
	}
}

// TestToolCallLoopRunsEndToEnd guards the full agent loop: the wire must
// carry the registered tool catalog, a provider tool_call must execute
// locally, and its output must ride the follow-up request. Without
// SetTools each turn the model invents names and no loop ever runs.
func TestToolCallLoopRunsEndToEnd(t *testing.T) {
	var requests [][]byte
	var reqMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqMu.Lock()
		requests = append(requests, raw)
		n := len(requests)
		reqMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First call asks for the exec tool — arguments are a real,
			// safe shell command the local executor will run.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{
						"role": "assistant", "content": "",
						"tool_calls": []any{map[string]any{
							"id": "call_1", "type": "function",
							"function": map[string]any{"name": "exec", "arguments": `{"command":"printf dG9vbC1vdXRwdXQtc2VjcmV0 | base64 -d"}`},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "tool said hi"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14},
		})
	}))
	defer server.Close()

	storage := t.TempDir()
	secrets, err := reposecret.New(filepath.Join(storage, "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secrets.Close() })
	gatewayRepository := repogateway.NewGatewayRepository(filepath.Join(storage, "gateways"), secrets)
	gatewayApplication := appgateway.New(gatewayRepository, secrets, nil)
	key := "local-test-key"
	stream := false
	if err := gatewayApplication.Upsert(t.Context(), appgateway.GatewayInput{
		ID: "gw1", Name: "Test gateway", Style: enum.StyleCompletion, Endpoint: server.URL,
		ApiKey: &key, Stream: &stream,
		Models: []appgateway.ModelInput{{ID: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	toolOutputs, err := repoconversation.NewToolOutputRepository(filepath.Join(storage, "conversations"))
	if err != nil {
		t.Fatal(err)
	}
	execTool, err := toolexec.NewExecTool(toolOutputs, toolexec.NewLocalRunner())
	if err != nil {
		t.Fatal(err)
	}
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Environment]appjob.EnvironmentDriver{enum.EnvironmentLocal: appjob.EnvironmentBundle{
			Services: map[enum.Style]dservice.TextModelService{enum.StyleCompletion: textcompletion.New(coretext.Options{FileRoot: workspace, FileStore: repoworkspace.NewLocal()})},
			Tools:    map[string]dservice.Tool{"exec": execTool},
		}},
		config.NewConfig(),
		nil,
		"",
	)

	var output, stderr bytes.Buffer
	repl, err := NewREPL(Options{
		Conversations:  conversationApplication,
		Gateways:       gatewayApplication,
		Workspace:      workspace,
		WorkspaceStore: repoworkspace.NewLocal(),
		HistoryDir:     filepath.Join(storage, "cli"),
		HistoryStore:   repohistory.NewLocal(),
		Stdin:          pipeInput(t, "run echo\n/exit\n"),
		Stdout:         &output,
		Stderr:         &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repl.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := repl.Close(); err != nil {
		t.Fatal(err)
	}

	reqMu.Lock()
	defer reqMu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected 2 provider calls (answer + tool follow-up), got %d", len(requests))
	}
	var first map[string]any
	if err := json.Unmarshal(requests[0], &first); err != nil {
		t.Fatal(err)
	}
	tools, _ := first["tools"].([]any)
	var names []string
	for _, tl := range tools {
		if fn, ok := tl.(map[string]any)["function"].(map[string]any); ok {
			names = append(names, fmt.Sprint(fn["name"]))
		}
	}
	if !slices.Contains(names, "exec") {
		t.Fatalf("provider request missing tool catalog — names=%v", names)
	}
	// The second call must carry the tool result back — that is the loop.
	second := string(requests[1])
	if !strings.Contains(second, "tool-output-secret") || !strings.Contains(second, `"role":"tool"`) {
		t.Fatalf("tool output never returned to provider: %s", second[:min(len(second), 400)])
	}
	plainOutput := ansi.Strip(output.String())
	for _, want := range []string{"⏺ exec", `{"command":"printf dG9vbC1vdXRwdXQtc2VjcmV0 | base64 -d"}`, "tool-output-secret", "tool said hi"} {
		if !strings.Contains(plainOutput, want) {
			t.Errorf("output missing %q:\n%s", want, output.String())
		}
	}
	if err := conversationApplication.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// pngFixture is a minimal valid PNG — copied from attach tests so the
// /attach path materializes real image bytes.
var pngFixture = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
}

func pipeInput(t *testing.T, input string) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}
