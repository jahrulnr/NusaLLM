package appgateway

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

// The auto-selection rule picks the only possible answer — the sole
// usable gateway, then its sole listed model — and says which fields it
// filled so the transport can announce them.
func TestResolveSelectionAutoSelects(t *testing.T) {
	app, _, _ := newApp(t)
	off := false
	if err := app.Upsert(t.Context(), GatewayInput{
		ID: "gw1", Name: "n", Style: enum.StyleMessages, Endpoint: "https://x",
		Stream: &off, Models: []ModelInput{{ID: "m1"}},
	}); err != nil {
		t.Fatal(err)
	}
	// A broken sibling must not count against "the only usable gateway".
	if err := app.Upsert(t.Context(), GatewayInput{
		ID: "sick", Name: "n", Style: enum.StyleMessages, Endpoint: "https://x",
		ApiKey: strptr("env:DEFINITELY_UNSET_RESOLVE"),
	}); err != nil {
		t.Fatal(err)
	}

	sel, err := app.ResolveSelection(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Route != "gw1" || sel.ModelID != "m1" || !sel.AutoRoute || !sel.AutoModel {
		t.Fatalf("selection = %+v, want gw1/m1 both auto", sel)
	}

	// Explicit picks pass through untouched — validation is the send
	// path's job, not this rule's.
	sel, err = app.ResolveSelection(t.Context(), "gw1", "m9")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Route != "gw1" || sel.ModelID != "m9" || sel.AutoRoute || sel.AutoModel {
		t.Fatalf("explicit selection = %+v", sel)
	}

	// An explicit healthy route still gets its sole listed model filled.
	sel, err = app.ResolveSelection(t.Context(), "gw1", "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Route != "gw1" || sel.ModelID != "m1" || sel.AutoRoute || !sel.AutoModel {
		t.Fatalf("explicit route + sole model = %+v, want gw1/m1 model-only auto", sel)
	}

	// A broken route offers no model to auto-fill.
	sel, err = app.ResolveSelection(t.Context(), "sick", "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Route != "sick" || sel.ModelID != "" || sel.AutoModel {
		t.Fatalf("broken route selection = %+v", sel)
	}
}

// Ambiguity is not an error and not a guess: multiple usable gateways or
// multiple listed models leave the field empty for the caller to ask.
func TestResolveSelectionAmbiguous(t *testing.T) {
	app, _, _ := newApp(t)
	off := false
	for _, id := range []string{"a", "b"} {
		if err := app.Upsert(t.Context(), GatewayInput{
			ID: id, Name: "n", Style: enum.StyleMessages, Endpoint: "https://x",
			Stream: &off, Models: []ModelInput{{ID: "m1"}, {ID: "m2"}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	sel, err := app.ResolveSelection(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Route != "" || sel.ModelID != "" || sel.AutoRoute || sel.AutoModel {
		t.Fatalf("ambiguous setup must not auto-select: %+v", sel)
	}

	// A bound route with two listed models leaves the model open.
	sel, err = app.ResolveSelection(t.Context(), "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Route != "a" || sel.ModelID != "" || sel.AutoRoute || sel.AutoModel {
		t.Fatalf("two-model route must not auto-select a model: %+v", sel)
	}
}
