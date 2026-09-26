package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconversation "github.com/jahrulnr/llm-base/application/conversation"
	appgateway "github.com/jahrulnr/llm-base/application/gateway"
	"github.com/jahrulnr/llm-base/infrastructure/config"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	repoconversation "github.com/jahrulnr/llm-base/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/llm-base/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/llm-base/infrastructure/repository/secret"
	textcompletion "github.com/jahrulnr/llm-base/infrastructure/service/core/text/completion"
)

func TestREPLSendsListsAndResumesConversation(t *testing.T) {
	requests := make(chan []byte, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		requests <- raw
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "pong"},
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
	gatewayApplication := appgateway.New(gatewayRepository, secrets)
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
	if err := os.WriteFile(filepath.Join(workspace, "shot.png"), cliPNGFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	conversationRepository := repoconversation.NewConversationRepository(filepath.Join(storage, "conversations"))
	conversationApplication := appconversation.New(
		conversationRepository,
		gatewayRepository,
		map[enum.Style]dservice.TextModelService{
			enum.StyleCompletion: textcompletion.New(textcompletion.Options{FileRoot: workspace}),
		},
		nil,
		config.NewConfig(),
	)
	var output, stderr bytes.Buffer
	input := pipeInput(t, "/route gw1\n/model m1\n/attach shot.png\nhello\n/list\n/history 10\n/exit\n")
	repl, err := NewREPL(Options{
		Conversations: conversationApplication,
		Gateways:      gatewayApplication,
		Workspace:     workspace,
		HistoryDir:    filepath.Join(storage, "cli"),
		Stdin:         input,
		Stdout:        &output,
		Stderr:        &stderr,
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
	if repl.conversationID == "" {
		t.Fatal("send did not bind the minted conversation id")
	}
	stored, err := conversationRepository.Load(t.Context(), repl.conversationID)
	if err != nil {
		t.Fatalf("conversation not persisted: %v", err)
	}
	if len(stored.Messages()) == 0 || len(stored.Messages()[0].Attachments()) != 1 || stored.Messages()[0].Attachments()[0].SHA256() == "" {
		t.Fatalf("user attachment did not persist with its content pin: %+v", stored.Messages())
	}
	if request := string(<-requests); !strings.Contains(request, "data:image/png;base64,") {
		t.Fatalf("attached image not materialized in provider request: %s", request)
	}
	for _, want := range []string{"route set to gw1", "model set to m1", "attached shot.png", "pong", "messages=2", "user: hello", "assistant: pong", "[usage input=10"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("REPL output missing %q:\n%s", want, output.String())
		}
	}

	var resumedOutput, resumedError bytes.Buffer
	resumed, err := NewREPL(Options{
		Conversations: conversationApplication,
		Gateways:      gatewayApplication,
		Workspace:     workspace,
		HistoryDir:    filepath.Join(storage, "cli"),
		Stdin:         pipeInput(t, "/resume "+repl.conversationID+"\n/edit "+stored.Messages()[0].ID()+" updated user text\n/route gw2\n/new\n/model m2\n/attach shot.png\ntext-only turn\n/exit\n"),
		Stdout:        &resumedOutput,
		Stderr:        &resumedError,
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
	if !strings.Contains(resumedOutput.String(), "resumed "+repl.conversationID) {
		t.Fatalf("conversation resume failed: id=%q output=%q", resumed.conversationID, resumedOutput.String())
	}
	updated, err := conversationRepository.Load(t.Context(), repl.conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Messages()[0].Content() != "updated user text" {
		t.Fatalf("edit did not rewrite user message: %q", updated.Messages()[0].Content())
	}
	if resumed.conversationID == "" || resumed.conversationID == repl.conversationID || resumed.route != "gw2" || resumed.modelID != "m2" {
		t.Fatalf("new conversation binding = id:%q route:%q model:%q", resumed.conversationID, resumed.route, resumed.modelID)
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
