package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	coretext "github.com/jahrulnr/nusallm/infrastructure/service/core/text"
	textcompletion "github.com/jahrulnr/nusallm/infrastructure/service/core/text/completion"
	toolsummary "github.com/jahrulnr/nusallm/infrastructure/service/tool/summary"
)

func TestAttachmentPathAcceptsQuotedSpaces(t *testing.T) {
	if got, err := AttachmentPath(`"images/a photo.png"`); err != nil || got != "images/a photo.png" {
		t.Fatalf("attachment path = %q, %v", got, err)
	}
	if _, err := AttachmentPath(`"unterminated`); err == nil {
		t.Fatal("invalid quoted attachment path should fail")
	}
}

// commandFixture wires a session+registry against a stub provider the way
// the integration test does, minus the piped program loop — commands
// execute straight through the registry.
type commandFixture struct {
	sess      *session.Session
	reg       *Registry
	convs     drepo.Conversation
	app       *appconversation.ConversationApp
	workspace string
}

func newCommandFixture(t *testing.T) *commandFixture {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "pong"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6},
		})
	}))
	t.Cleanup(server.Close)

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
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Style]dservice.TextModelService{
			enum.StyleCompletion: textcompletion.New(coretext.Options{FileRoot: workspace}),
		},
		map[string]dservice.Tool{toolsummary.Name: toolsummary.NewSummaryTool()},
		config.NewConfig(),
		"",
	)
	t.Cleanup(func() { _ = conversationApplication.Shutdown(context.Background()) })
	sess, err := session.New(session.Options{
		Conversations: conversationApplication,
		Gateways:      gatewayApplication,
		Workspace:     workspace,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	sess.Route, sess.ModelID = "gw1", "m1"
	return &commandFixture{sess: sess, reg: New(sess), convs: conversationRepository, app: conversationApplication, workspace: workspace}
}

func (f *commandFixture) run(t *testing.T, line string) string {
	t.Helper()
	name, args, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	var out strings.Builder
	emitter := dservice.CommandEmitter(func(chunk string) { out.WriteString(chunk) })
	if err := f.reg.Execute(t.Context(), name, emitter, args); err != nil {
		return "error: " + err.Error()
	}
	return out.String()
}

func (f *commandFixture) send(t *testing.T, text string) {
	t.Helper()
	route, modelID, err := f.sess.ResolveRoute(t.Context())
	if err != nil {
		t.Fatalf("resolve %q: %v", text, err)
	}
	result, err := f.sess.Send(t.Context(), text, route, modelID, nil)
	if err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
	if result.UserMessagePersisted {
		f.sess.ConversationID = result.ConversationID // finishTurn's bind, replayed
	}
}

func TestSessionCommandsReportState(t *testing.T) {
	f := newCommandFixture(t)
	if out := f.run(t, "/status"); !strings.Contains(out, "no active conversation") || !strings.Contains(out, "gw1") {
		t.Fatalf("status without conversation = %q", out)
	}
	if out := f.run(t, "/pwd"); !strings.Contains(out, f.workspace) {
		t.Fatalf("pwd = %q, want workspace", out)
	}
	if out := f.run(t, "/tools"); !strings.Contains(out, toolsummary.Name) {
		t.Fatalf("tools = %q, want the summary tool listed", out)
	}
	if out := f.run(t, "/attach"); !strings.Contains(out, "no pending attachments") {
		t.Fatalf("attach list = %q", out)
	}
	if out := f.run(t, "/cancel"); !strings.Contains(out, "no active turn") {
		t.Fatalf("cancel without a turn = %q", out)
	}
}

func TestExportAndTitleAndDeleteLifecycle(t *testing.T) {
	f := newCommandFixture(t)
	f.send(t, "hello")
	f.send(t, "again")
	convID := f.sess.ConversationID
	if convID == "" {
		t.Fatal("send did not bind a conversation")
	}

	if out := f.run(t, "/status"); !strings.Contains(out, convID) || !strings.Contains(out, "messages    4") {
		t.Fatalf("status = %q, want conversation + message count", out)
	}
	if out := f.run(t, "/title renamed room"); !strings.Contains(out, "title set") {
		t.Fatalf("title = %q", out)
	}
	if info, err := f.app.Info(t.Context(), convID); err != nil || info.Title != "renamed room" {
		t.Fatalf("title did not persist: %+v %v", info, err)
	}
	if out := f.run(t, "/usage"); !strings.Contains(out, "total        12") {
		t.Fatalf("usage = %q, want two turns of tokens", out)
	}
	if out := f.run(t, "/export transcript.md"); !strings.Contains(out, "exported 4 messages") {
		t.Fatalf("export = %q", out)
	}
	raw, err := os.ReadFile(filepath.Join(f.workspace, "transcript.md"))
	if err != nil || !strings.Contains(string(raw), "## user") || !strings.Contains(string(raw), "pong") {
		t.Fatalf("exported transcript = %v %q", err, raw)
	}
	if out := f.run(t, "/export ../escape.md"); !strings.HasPrefix(out, "error:") {
		t.Fatalf("export escape = %q, want confinement error", out)
	}

	// /edit with no replacement text must refuse — an empty content would
	// erase the message body before regenerating.
	stored, err := f.convs.Load(t.Context(), convID)
	if err != nil {
		t.Fatal(err)
	}
	firstID := stored.Messages()[0].ID()
	if out := f.run(t, "/edit "+firstID); !strings.HasPrefix(out, "error:") || !strings.Contains(out, "usage: /edit") {
		t.Fatalf("edit without text = %q, want usage error", out)
	}
	reloaded, err := f.convs.Load(t.Context(), convID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Messages()[0].Content() == "" {
		t.Fatal("bare /edit erased the message content")
	}

	if out := f.run(t, "/delete"); !strings.Contains(out, "deleted "+convID) {
		t.Fatalf("delete = %q", out)
	}
	if f.sess.ConversationID != "" {
		t.Fatal("deleting the active conversation must unbind it")
	}
	if _, err := f.convs.Info(t.Context(), convID); err == nil {
		t.Fatal("deleted conversation still readable")
	}
}

func TestCompactFoldsHistoryOnDemand(t *testing.T) {
	f := newCommandFixture(t)
	for _, text := range []string{"one", "two", "three", "four"} {
		f.send(t, text)
	}
	convID := f.sess.ConversationID

	out := f.run(t, "/compact")
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("compact failed: %s", out)
	}
	info, err := f.app.Info(t.Context(), convID)
	if err != nil {
		t.Fatal(err)
	}
	if info.SealedSegments != 1 {
		t.Fatalf("sealed segments = %d, want 1", info.SealedSegments)
	}
	if info.ActiveCount != 4+1 { // keep-tail 4 plus the checkpoint head
		t.Fatalf("active messages = %d, want keep-tail + checkpoint", info.ActiveCount)
	}

	// A fresh room has nothing foldable — the command refuses.
	f.run(t, "/new")
	f.send(t, "short")
	if out := f.run(t, "/compact"); !strings.Contains(out, "nothing to compact") {
		t.Fatalf("compact on thin history = %q, want refusal", out)
	}
}

func TestResumePrintsRecentConversationHistory(t *testing.T) {
	f := newCommandFixture(t)
	f.send(t, "prior question")
	f.send(t, "latest question")
	convID := f.sess.ConversationID
	f.sess.ConversationID = ""

	out := f.run(t, "/resume "+convID)
	for _, want := range []string{"resumed " + convID, `\> prior question`, `\> latest question`, "pong"} {
		if !strings.Contains(out, want) {
			t.Errorf("resume output missing %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "pong"); got != 2 {
		t.Fatalf("assistant history entries = %d, want 2:\n%s", got, out)
	}
}

func TestResumeArgumentCompletion(t *testing.T) {
	f := newCommandFixture(t)
	f.send(t, "hello")
	convID := f.sess.ConversationID
	if out := f.run(t, "/title bug hunt"); !strings.Contains(out, "title set") {
		t.Fatalf("title = %q", out)
	}

	rows := f.reg.Complete(t.Context(), "/resume ")
	if len(rows) != 1 || rows[0].Usage != "/resume "+convID {
		t.Fatalf("resume completion = %+v, want the saved conversation", rows)
	}
	if !strings.Contains(rows[0].Desc, "bug hunt") {
		t.Fatalf("completion desc = %q, want the conversation title", rows[0].Desc)
	}
	if rows := f.reg.Complete(t.Context(), "/resume bug"); len(rows) != 1 {
		t.Fatalf("title-matching completion = %+v, want the conversation", rows)
	}
	if rows := f.reg.Complete(t.Context(), "/resume nope"); rows != nil {
		t.Fatalf("non-matching completion = %+v, want nil", rows)
	}
	if rows := f.reg.Complete(t.Context(), "/status x"); rows != nil {
		t.Fatal("a command without a completer must not complete arguments")
	}
	if rows := f.reg.Complete(t.Context(), "/resume"); rows != nil {
		t.Fatal("a bare token completes command names, not arguments")
	}
	if rows := f.reg.Complete(t.Context(), "/resume\nx"); rows != nil {
		t.Fatal("a multi-line buffer must not complete")
	}
}

func TestConversationPickerUsesFirstUserMessageForUntitled(t *testing.T) {
	f := newCommandFixture(t)
	f.send(t, "Find the order status\nand explain its transitions")
	convID := f.sess.ConversationID

	for _, storedTitle := range []string{"", "untitled"} {
		conv, err := f.convs.Load(t.Context(), convID)
		if err != nil {
			t.Fatal(err)
		}
		conv.SetTitle(storedTitle)
		if err := f.convs.Save(t.Context(), conv); err != nil {
			t.Fatal(err)
		}

		rows := f.reg.Complete(t.Context(), "/resume ")
		if len(rows) != 1 || !strings.Contains(rows[0].Desc, "Find the order status and explain its transition…") {
			t.Fatalf("resume label for title %q = %+v", storedTitle, rows)
		}
		if strings.Contains(rows[0].Desc, "untitled") {
			t.Fatalf("resume label still says untitled: %+v", rows[0])
		}
		if rows := f.reg.Complete(t.Context(), "/resume transitions"); len(rows) != 1 || rows[0].Usage != "/resume "+convID {
			t.Fatalf("first-message title did not filter resume rows: %+v", rows)
		}
		if rows := f.reg.Complete(t.Context(), "/delete transitions"); len(rows) != 1 || rows[0].Usage != "/delete "+convID {
			t.Fatalf("first-message title did not filter delete rows: %+v", rows)
		}
	}
	if out := f.run(t, "/list"); !strings.Contains(out, `title="Find the order status and explain its transitions"`) {
		t.Fatalf("list did not show the first user message as title: %s", out)
	}
}

func TestRouteModelEditAttachCompletion(t *testing.T) {
	f := newCommandFixture(t)
	f.send(t, "hello world")
	convID := f.sess.ConversationID

	// /route completes gateway ids with status details.
	rows := f.reg.Complete(t.Context(), "/route ")
	if len(rows) != 1 || rows[0].Usage != "/route gw1" || rows[0].FillOnly {
		t.Fatalf("route completion = %+v, want gw1", rows)
	}
	if !strings.Contains(rows[0].Desc, "no-key") {
		t.Fatalf("route desc = %q, want key status", rows[0].Desc)
	}

	// /model completes the active route's models.
	if rows := f.reg.Complete(t.Context(), "/model "); len(rows) != 1 || rows[0].Usage != "/model m1" {
		t.Fatalf("model completion = %+v, want m1", rows)
	}
	if rows := f.reg.Complete(t.Context(), "/model x"); rows != nil {
		t.Fatalf("non-matching model completion = %+v, want nil", rows)
	}

	// /edit completes editable (user) message ids — fills, never submits.
	stored, err := f.convs.Load(t.Context(), convID)
	if err != nil {
		t.Fatal(err)
	}
	userID := stored.Messages()[0].ID()
	rows = f.reg.Complete(t.Context(), "/edit ")
	if len(rows) != 1 || rows[0].Usage != "/edit "+userID+" " || !rows[0].FillOnly {
		t.Fatalf("edit completion = %+v, want the user message fill row", rows)
	}
	if rows := f.reg.Complete(t.Context(), "/edit hello"); len(rows) != 1 {
		t.Fatalf("content-matching edit completion = %+v", rows)
	}
	if rows := f.reg.Complete(t.Context(), "/edit "+userID+" text"); rows != nil {
		t.Fatal("past the id, /edit waits for replacement text — no completion")
	}

	// /attach completes workspace paths: dirs fill-and-descend, files
	// submit. Bare /attach keeps its pending-attachment list.
	if err := os.WriteFile(filepath.Join(f.workspace, "note.txt"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.workspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspace, "docs", "inner.md"), []byte("i"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspace, ".env"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows := f.reg.Complete(t.Context(), "/attach "); rows != nil {
		t.Fatalf("bare /attach keeps the pending list, got %+v", rows)
	}
	rows = f.reg.Complete(t.Context(), "/attach ")
	if rows != nil {
		t.Fatal("empty arg must not complete — bare /attach lists pending")
	}
	rows = f.reg.Complete(t.Context(), "/attach n")
	if len(rows) != 1 || rows[0].Usage != "/attach note.txt" || rows[0].FillOnly {
		t.Fatalf("file completion = %+v, want a submittable note.txt row", rows)
	}
	rows = f.reg.Complete(t.Context(), "/attach d")
	if len(rows) != 1 || rows[0].Usage != "/attach docs/" || !rows[0].FillOnly {
		t.Fatalf("dir completion = %+v, want a fill-only docs/ row", rows)
	}
	if rows := f.reg.Complete(t.Context(), "/attach docs/"); len(rows) != 1 || rows[0].Usage != "/attach docs/inner.md" {
		t.Fatalf("nested completion = %+v", rows)
	}
	// Dotfiles stay out unless the prefix asks for them.
	if rows := f.reg.Complete(t.Context(), "/attach ."); len(rows) != 1 || rows[0].Usage != "/attach .env" {
		t.Fatalf("dotfile completion = %+v", rows)
	}
	for _, row := range f.reg.Complete(t.Context(), "/attach n") {
		if strings.HasPrefix(row.Usage, "/attach .") {
			t.Fatal("dotfiles must not appear without a dot prefix")
		}
	}
}

// The /edit picker must apply the domain's editability rule — after
// /compact the synthetic checkpoint head is a RoleUser message that is
// NOT editable; listing it would offer a row guaranteed to fail.
func TestEditCompletionExcludesCheckpoint(t *testing.T) {
	f := newCommandFixture(t)
	for _, text := range []string{"one", "two", "three", "four"} {
		f.send(t, text)
	}
	if out := f.run(t, "/compact"); strings.HasPrefix(out, "error:") {
		t.Fatalf("compact failed: %s", out)
	}
	stored, err := f.convs.Load(t.Context(), f.sess.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID := stored.Messages()[0].ID() // the sealed checkpoint head

	rows := f.reg.Complete(t.Context(), "/edit ")
	if len(rows) == 0 {
		t.Fatal("edit completion empty after compact — user messages exist")
	}
	for _, row := range rows {
		if strings.Contains(row.Usage, checkpointID) {
			t.Fatalf("checkpoint %s offered as an edit candidate", checkpointID)
		}
	}
}

// A single healthy gateway with a single model needs no /route+/model —
// send binds the only possible answer and says so.
func TestSendAutoSelectsSingleGatewayAndModel(t *testing.T) {
	f := newCommandFixture(t)
	f.sess.Route, f.sess.ModelID = "", ""
	var notes []string
	f.sess.Notify = func(s string) { notes = append(notes, s) }

	f.send(t, "hello")
	if f.sess.Route != "gw1" || f.sess.ModelID != "m1" {
		t.Fatalf("auto-select bound route=%q model=%q, want gw1/m1", f.sess.Route, f.sess.ModelID)
	}
	joined := strings.Join(notes, " ")
	if !strings.Contains(joined, "auto-selected route gw1") ||
		!strings.Contains(joined, "auto-selected model m1") {
		t.Fatalf("notify = %q, want both auto-select notes", joined)
	}
}
