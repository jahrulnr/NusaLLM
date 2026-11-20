package appconversation

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Resume continues from the current tail — tool results the failed turn
// already produced stay in history. Edit would have dropped them.
func TestResumeKeepsPriorToolResults(t *testing.T) {
	app, _ := fixture(t, okHandler)

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "inspect cache.go", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	seed, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	userID := seed.Messages()[0].ID()
	// Rebuild as a mid-tool failure: keep the user line, append an
	// assistant that already answered a tool call.
	if err := app.deps.Convs.RewriteFrom(t.Context(), "room", userID, "inspect cache.go"); err != nil {
		t.Fatal(err)
	}
	asst, err := entity.NewMessage(enum.RoleAssistant, "reading")
	if err != nil {
		t.Fatal(err)
	}
	tc, err := entity.NewToolCall("call_1", "echo", `{}`, enum.TooltypeOrigin)
	if err != nil {
		t.Fatal(err)
	}
	tc.SetOutput("file contents the model already paid for")
	if err := asst.AddToolCall(tc); err != nil {
		t.Fatal(err)
	}
	asst.SetFinishReason(enum.FinishReasonToolCalls)
	if _, err := app.deps.Convs.AppendMessage(t.Context(), "room", asst); err != nil {
		t.Fatal(err)
	}
	seed, err = app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	before := len(seed.Messages())

	if err := app.Resume(t.Context(), "room", "", "", appjob.SendOptions{}, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	after, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Messages()) < before {
		t.Fatalf("resume truncated history: before=%d after=%d", before, len(after.Messages()))
	}
	found := false
	for _, m := range after.Messages() {
		for _, c := range m.ToolCalls() {
			if c.ID() == "call_1" && c.Output() == "file contents the model already paid for" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("resume dropped the prior tool result")
	}
}

// A failed send on the bound model must be retryable on another one via
// Resume — same escape hatch the Retry button needs.
func TestResumeRetriesWithNewModelSelection(t *testing.T) {
	var mu sync.Mutex
	var models []string
	app, gw := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		if body.Model == "m1" {
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		okHandler(w, r)
	})
	m1, _ := entity.NewModel("m1", "gw1")
	m2, _ := entity.NewModel("m2", "gw1")
	gw.SetModels([]entity.Model{m1, m2})
	if err := app.deps.Gateways.Save(t.Context(), gw); err != nil {
		t.Fatal(err)
	}

	if _, err := app.Send(t.Context(), "room", "gw1", "m1", "hi", nil, appjob.SendOptions{}, nil); err == nil {
		t.Fatal("m1 send must fail")
	}
	if err := app.Resume(t.Context(), "room", "gw1", "m2", appjob.SendOptions{}, nil); err != nil {
		t.Fatalf("resume retry on m2: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 2 || models[0] != "m1" || models[1] != "m2" {
		t.Fatalf("upstream models = %v, want [m1 m2]", models)
	}
	after, err := app.deps.Convs.Load(t.Context(), "room")
	if err != nil {
		t.Fatal(err)
	}
	if after.ModelID() != "m2" {
		t.Fatalf("room binding = %q, want m2 after a successful resume", after.ModelID())
	}
}

func TestResumeRejectsEmptyAndMissing(t *testing.T) {
	app, _ := fixture(t, okHandler)
	if err := app.Resume(t.Context(), "ghost", "gw1", "m1", appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 404 {
		t.Fatalf("missing room → %v, want 404", err)
	}
	empty, err := aggregate.NewConversation("empty")
	if err != nil {
		t.Fatal(err)
	}
	empty.SetRoute("gw1")
	empty.SetModelID("m1")
	if err := app.deps.Convs.Save(t.Context(), empty); err != nil {
		t.Fatal(err)
	}
	if err := app.Resume(t.Context(), "empty", "", "", appjob.SendOptions{}, nil); apperror.As(err).StatusCode() != 400 {
		t.Fatalf("empty room → %v, want 400", err)
	}
}
