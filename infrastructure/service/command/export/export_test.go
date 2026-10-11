package cmdexport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// sessionFakes are the minimal SlashSession pieces /export touches:
// bindings for the active conversation id, the conversation read, the
// workspace file port, and sanitized output.
type exportBindings struct{ convID string }

func (b *exportBindings) ConversationID() string                  { return b.convID }
func (b *exportBindings) SetConversationID(string)                {}
func (b *exportBindings) Route() string                           { return "" }
func (b *exportBindings) SetRoute(string)                         {}
func (b *exportBindings) ModelID() string                         { return "" }
func (b *exportBindings) SetModelID(string)                       {}
func (b *exportBindings) Attachments() []valueobject.Attachment   { return nil }
func (b *exportBindings) SetAttachments([]valueobject.Attachment) {}
func (b *exportBindings) AppendAttachment(valueobject.Attachment) {}
func (b *exportBindings) Interactive() bool                       { return false }
func (b *exportBindings) SetExit(bool)                            {}
func (b *exportBindings) RequestCancelTurn()                      {}
func (b *exportBindings) CancelTurnActive() bool                  { return false }
func (b *exportBindings) RequestClearScreen()                     {}

type exportConversationAPI struct{ history []entity.Message }

func (c *exportConversationAPI) Info(context.Context, string) (drepo.ConversationInfo, error) {
	return drepo.ConversationInfo{}, nil
}
func (c *exportConversationAPI) List(context.Context) ([]drepo.ConversationInfo, error) {
	return nil, nil
}
func (c *exportConversationAPI) History(context.Context, string, int, int) ([]entity.Message, error) {
	return c.history, nil
}
func (c *exportConversationAPI) Delete(context.Context, string) error { return nil }
func (c *exportConversationAPI) Rename(context.Context, string, string) error {
	return nil
}
func (c *exportConversationAPI) Compact(context.Context, string) error { return nil }
func (c *exportConversationAPI) Edit(context.Context, string, string, string, event.TurnEventHandler) error {
	return nil
}
func (c *exportConversationAPI) ToolSchemas(context.Context, string) ([]valueobject.ToolSchema, error) {
	return nil, nil
}

type exportWorkspace struct{ written *bytes.Buffer }

func (w *exportWorkspace) Path() string { return "/ws" }
func (w *exportWorkspace) FS() fs.FS    { return nil }
func (w *exportWorkspace) ResolveAttachment(string) (valueobject.Attachment, error) {
	return valueobject.Attachment{}, nil
}
func (w *exportWorkspace) CreateFile(string) (io.WriteCloser, error) {
	w.written = &bytes.Buffer{}
	return writeCloseBuffer{w.written}, nil
}

type writeCloseBuffer struct{ *bytes.Buffer }

func (writeCloseBuffer) Close() error { return nil }

type exportOutput struct{}

func (exportOutput) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}
func (exportOutput) SanitizeText(text string) string { return text }

// The workspace context record — user role, no text, injection calls
// only — exports as a context block: its stored output lines, no empty
// `## user` heading and no [tool …] dump.
func TestExportRendersContextRecord(t *testing.T) {
	user, _ := entity.NewMessage(enum.RoleUser, "hi")
	contextMsg := workspaceContextRecord(t, "/srv/ws")
	assistant, _ := entity.NewMessage(enum.RoleAssistant, "answer")
	call, _ := entity.NewToolCall("c1", "read_file", `{"path":"x"}`, enum.TooltypeOrigin)
	call.SetOutput("file body")
	assistant.AddToolCall(call)
	checkpoint, _ := entity.NewMessage(enum.RoleUser, "earlier turns summarized")
	checkpoint.SetUserType(enum.LLMUserTypeSystem)

	workspace := &exportWorkspace{}
	h := appcommand.NewFacade(
		&exportConversationAPI{history: []entity.Message{user, contextMsg, assistant, checkpoint}},
		nil, &exportBindings{convID: "room"}, workspace, exportOutput{})
	cmd := New(h)
	var out strings.Builder
	err := cmd.Execute(context.Background(), "", dservice.CommandEmitter(func(c string) { out.WriteString(c) }), "transcript.md")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.written == nil {
		t.Fatal("export wrote no file")
	}
	got := workspace.written.String()

	want := "## context · " + contextMsg.ID()
	if !strings.Contains(got, want) {
		t.Fatalf("context heading %q missing:\n%s", want, got)
	}
	if !strings.Contains(got, "workspace changed to /srv/ws") {
		t.Fatalf("context output missing:\n%s", got)
	}
	if strings.Contains(got, "## user · "+contextMsg.ID()) || strings.Contains(got, "[tool workspace") {
		t.Fatalf("context record rendered as an ordinary message:\n%s", got)
	}
	if n := strings.Count(got, "## user ·"); n != 2 {
		t.Fatalf("user headings = %d, want the real user line + checkpoint only:\n%s", n, got)
	}
	for _, w := range []string{
		"## assistant · ", "[tool read_file c1]", "[tool result c1] file body",
	} {
		if !strings.Contains(got, w) {
			t.Fatalf("expected export content %q missing:\n%s", w, got)
		}
	}
	if !strings.Contains(out.String(), "exported 4 messages") {
		t.Fatalf("export summary = %q", out.String())
	}
}

// A mixed message keeps its ordinary heading; the injection call exports
// as a context line inside it while the origin call keeps the tool form.
// An injection call with no recorded output exports no line.
func TestExportMixedMessageKeepsHeading(t *testing.T) {
	mixed, _ := entity.NewMessage(enum.RoleUser, "real words")
	inj, _ := entity.NewToolCall("c1", "workspace", `{"path":"/x"}`, enum.TooltypeInjection)
	inj.SetStoredOutput("injected context", "")
	bare, _ := entity.NewToolCall("c3", "workspace", `{"path":"/y"}`, enum.TooltypeInjection)
	orig, _ := entity.NewToolCall("c2", "run_shell", `{"cmd":"ls"}`, enum.TooltypeOrigin)
	mixed.AddToolCall(inj)
	mixed.AddToolCall(bare)
	mixed.AddToolCall(orig)

	workspace := &exportWorkspace{}
	h := appcommand.NewFacade(
		&exportConversationAPI{history: []entity.Message{mixed}},
		nil, &exportBindings{convID: "room"}, workspace, exportOutput{})
	if err := New(h).Execute(context.Background(), "", nil, "transcript.md"); err != nil {
		t.Fatal(err)
	}
	got := workspace.written.String()
	want := "## user · " + mixed.ID()
	if !strings.Contains(got, want) || !strings.Contains(got, "injected context") || !strings.Contains(got, "[tool run_shell c2]") {
		t.Fatalf("mixed export wrong:\n%s", got)
	}
	if strings.Contains(got, "[tool workspace") {
		t.Fatalf("injection call exported as a tool dump:\n%s", got)
	}
	// The bare injection sits between the two — no output, no line.
	if !strings.Contains(got, "injected context\n[tool run_shell") {
		t.Fatalf("bare injection call exported an empty line:\n%s", got)
	}
}

// workspaceContextRecord builds the injection record the turn loop
// writes after an editable line — duplicated here rather than shared so
// the renderer test stays independent of the producer.
func workspaceContextRecord(t *testing.T, dir string) entity.Message {
	t.Helper()
	msg, err := entity.NewMessage(enum.RoleUser, "")
	if err != nil {
		t.Fatal(err)
	}
	msg.SetUserType(enum.LLMUserTypeSystem)
	call, err := entity.NewToolCall("call_ws", "workspace", `{"path":"`+dir+`"}`, enum.TooltypeInjection)
	if err != nil {
		t.Fatal(err)
	}
	call.SetStoredOutput("workspace changed to "+dir, "")
	msg.AddToolCall(call)
	return msg
}
