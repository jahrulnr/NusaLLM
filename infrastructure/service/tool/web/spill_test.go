package toolweb

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/searchwire"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
)

// spillTool wires a real conversation tool-output store and returns a ctx
// carrying its conversation id plus the tool bound to that store — the
// same reservation path exec uses.
func spillTool(t *testing.T, def searchwire.ToolDef) (context.Context, service.Tool) {
	t.Helper()
	root := t.TempDir()
	outputs, err := repoconversation.NewToolOutputRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "room"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool, err := New(def, outputs)
	if err != nil {
		t.Fatal(err)
	}
	return service.WithConversationID(context.Background(), "room"), tool
}

func TestTempFileArtifactRespillsToConversationStore(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "searchwire-abc.txt")
	if err := os.WriteFile(artifact, []byte("the full extracted text"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(artifact)
	def := stubDef("fetch", "", nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"text":"short","truncated":true,"tempFile":` + string(raw) + `}`), nil
	}
	ctx, tool := spillTool(t, def)
	call, _ := entity.NewToolCall("c1", "web_fetch", `{"url":"https://x"}`, enum.TooltypeOrigin)
	res, err := tool.Execute(ctx, call, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(res.Text), &doc); err != nil {
		t.Fatalf("text = %q: %v", res.Text, err)
	}
	newPath, _ := doc["tempFile"].(string)
	if newPath == "" || newPath == artifact {
		t.Fatalf("tempFile not repointed: %v", doc["tempFile"])
	}
	body, err := os.ReadFile(newPath)
	if err != nil || string(body) != "the full extracted text" {
		t.Fatalf("spill content = %q err=%v", body, err)
	}
	if !strings.Contains(newPath, "tool-output") {
		t.Fatalf("spill path = %q — must live under the conversation store", newPath)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("original artifact should be removed after respill: %v", err)
	}
}

func TestOversizedDocSpillsWithRecoveryPointer(t *testing.T) {
	big, _ := json.Marshal(strings.Repeat("x", 40000))
	def := stubDef("search", "", nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"query":"q","results":[],"blob":` + string(big) + `}`), nil
	}
	ctx, tool := spillTool(t, def)
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"q"}`, enum.TooltypeOrigin)
	res, err := tool.Execute(ctx, call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "bytes omitted") || !strings.Contains(res.Text, "full output: ") {
		t.Fatalf("missing spill pointer: %q…", res.Text[:200])
	}
	if len(res.Text) > call.OutputBudget()+100 {
		t.Fatalf("output = %d bytes — must stay near budget", len(res.Text))
	}
}

func TestOversizedDocWithoutStoreKeepsInline(t *testing.T) {
	big, _ := json.Marshal(strings.Repeat("x", 40000))
	def := stubDef("search", "", nil)
	def.Run = func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"query":"q","results":[],"blob":` + string(big) + `}`), nil
	}
	tool, err := New(def, nil)
	if err != nil {
		t.Fatal(err)
	}
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"q"}`, enum.TooltypeOrigin)
	res, err := tool.Execute(context.Background(), call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "bytes omitted") {
		t.Fatalf("nil store must not fake a spill: %q…", res.Text[:200])
	}
	if len(res.Text) < 40000 {
		t.Fatalf("inline doc should pass through whole: %d bytes", len(res.Text))
	}
}

func TestNineRouterKindUsesOmniRoutePath(t *testing.T) {
	var saw bool
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		saw = true
		return omniResponse(`{"provider":"context7","results":[{"title":"T","url":"https://x","snippet":"s"}],"answer":null}`), nil
	})}
	tool := toolWithClient(t, stubDef("search", `{"query":"x","results":[]}`, nil), client)
	ctx := service.WithWebProvider(context.Background(), service.WebProvider{
		Kind:     "9router",
		Endpoint: "http://router.local:20128/v1",
		APIKey:   "k",
	})
	call, _ := entity.NewToolCall("c1", "web_search", `{"query":"x"}`, enum.TooltypeOrigin)
	if _, err := tool.Execute(ctx, call, nil); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("9router kind did not route to the omniroute endpoint")
	}
}
