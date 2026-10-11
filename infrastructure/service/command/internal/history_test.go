package commandinternal

import (
	"fmt"
	"strings"
	"testing"

	appcommand "github.com/jahrulnr/nusallm/application/command"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

// passthroughOutput is the OutputPort the transcript tests need — the
// terminal's sanitization is a delivery detail, not part of Write's
// layout contract.
type passthroughOutput struct{}

func (passthroughOutput) Write(emitter dservice.CommandEmitter, format string, args ...any) {
	if emitter != nil {
		emitter(fmt.Sprintf(format, args...))
	}
}
func (passthroughOutput) SanitizeText(text string) string { return text }

// workspaceContextRecord builds the injection record the turn loop writes
// after an editable line — system user, no content, one workspace call.
func workspaceContextRecord(t *testing.T, dir string) entity.Message {
	t.Helper()
	msg, err := entity.NewMessage(enum.RoleUser, "")
	if err != nil {
		t.Fatal(err)
	}
	msg.SetUserType(enum.LLMUserTypeSystem)
	call, err := entity.NewToolCall("call_ws", "workspace", fmt.Sprintf(`{"path":%q}`, dir), enum.TooltypeInjection)
	if err != nil {
		t.Fatal(err)
	}
	call.SetStoredOutput("The active workspace is "+dir+".", "")
	msg.AddToolCall(call)
	return msg
}

// An injection-only user record renders as one plain context line per
// call — never a \> quote or a ⏺ dump — while everything around it keeps
// the ordinary transcript shape.
func TestWriteRendersInjectionContextLines(t *testing.T) {
	user, _ := entity.NewMessage(enum.RoleUser, "hi")
	contextMsg := workspaceContextRecord(t, "/srv/ws")
	assistant, _ := entity.NewMessage(enum.RoleAssistant, "answer")
	call, _ := entity.NewToolCall("c1", "read_file", `{"path":"x"}`, enum.TooltypeOrigin)
	call.SetOutput("file body")
	assistant.AddToolCall(call)
	// The compaction checkpoint — system user with content, no calls —
	// keeps its quoted rendering.
	checkpoint, _ := entity.NewMessage(enum.RoleUser, "earlier turns summarized")
	checkpoint.SetUserType(enum.LLMUserTypeSystem)

	var out strings.Builder
	emitter := dservice.CommandEmitter(func(chunk string) { out.WriteString(chunk) })
	h := appcommand.NewFacade(nil, nil, nil, nil, passthroughOutput{})
	Write(h, emitter, []entity.Message{user, contextMsg, assistant, checkpoint})
	got := out.String()

	for _, want := range []string{
		"\\> hi",
		"The active workspace is /srv/ws.",
		"answer",
		"⏺ `read_file` `{\"path\":\"x\"}`",
		"\\> earlier turns summarized",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "⏺ `workspace`") {
		t.Fatalf("injection call rendered as a tool dump:\n%s", got)
	}
	// Exactly one quote block — the real user line. The context record
	// must not leave an empty \> behind.
	if n := strings.Count(got, "\\>"); n != 2 {
		t.Fatalf("quote blocks = %d, want the user line + checkpoint only:\n%s", n, got)
	}
}

// A mixed record still quotes nothing empty: injection calls print their
// context line, origin calls keep the ⏺ dump — keyed by call type, not
// by the pure-message shape. An injection call with no recorded output
// emits no line at all.
func TestWriteMixedCallsFilterByType(t *testing.T) {
	mixed, _ := entity.NewMessage(enum.RoleUser, "")
	inj, _ := entity.NewToolCall("c1", "workspace", `{"path":"/x"}`, enum.TooltypeInjection)
	inj.SetStoredOutput("injected context", "")
	bare, _ := entity.NewToolCall("c3", "workspace", `{"path":"/y"}`, enum.TooltypeInjection)
	orig, _ := entity.NewToolCall("c2", "run_shell", `{"cmd":"ls"}`, enum.TooltypeOrigin)
	mixed.AddToolCall(inj)
	mixed.AddToolCall(bare)
	mixed.AddToolCall(orig)

	var out strings.Builder
	emitter := dservice.CommandEmitter(func(chunk string) { out.WriteString(chunk) })
	h := appcommand.NewFacade(nil, nil, nil, nil, passthroughOutput{})
	Write(h, emitter, []entity.Message{mixed})
	got := out.String()

	if !strings.Contains(got, "injected context") {
		t.Fatalf("injection output missing:\n%s", got)
	}
	if !strings.Contains(got, "⏺ `run_shell`") {
		t.Fatalf("origin call dump missing:\n%s", got)
	}
	if strings.Contains(got, "\\>") {
		t.Fatalf("a call-bearing message must not print an empty user quote:\n%s", got)
	}
	// The bare injection sits between the two — no output, no line.
	if !strings.Contains(got, "injected context\n\n⏺") {
		t.Fatalf("bare injection call left an empty line:\n%s", got)
	}
}
