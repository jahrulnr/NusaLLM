package appgateway

import "context"

// Selection is ResolveSelection's answer: the route/model the next turn
// should use plus which of them the auto-selection rule picked itself,
// so a transport can announce the pick in its own words instead of the
// rule carrying CLI phrasing.
type Selection struct {
	Route     string
	ModelID   string
	AutoRoute bool // route was unbound; the sole usable gateway filled it
	AutoModel bool // model was unbound; the route's sole listed entry filled it
}

// ResolveSelection applies the route/model selection policy: explicit
// picks pass through untouched (the send path validates them), while an
// unbound route fills from the sole usable gateway and an unbound model
// fills from the route's sole listed model — an unambiguous setup
// selects itself rather than making the operator retype the only
// possible answer. Ambiguity is not an error: the field comes back
// empty and the caller decides how to ask.
func (app *GatewayApp) ResolveSelection(ctx context.Context, route, modelID string) (Selection, error) {
	sel := Selection{Route: route, ModelID: modelID}
	if sel.Route == "" {
		gateways, err := app.List(ctx)
		if err != nil {
			return Selection{}, err
		}
		var ready []GatewayInfo
		for _, g := range gateways {
			if !g.Broken {
				ready = append(ready, g)
			}
		}
		if len(ready) == 1 {
			sel.Route = ready[0].ID
			sel.AutoRoute = true
		}
	}
	if sel.ModelID == "" && sel.Route != "" {
		// A gateway that won't load has nothing to offer — leave the
		// model unbound rather than pinning one on a dead route.
		if gw, err := app.Get(ctx, sel.Route); err == nil && !gw.Broken {
			if listed := gw.ListedModels(); len(listed) == 1 {
				sel.ModelID = listed[0].ID()
				sel.AutoModel = true
			}
		}
	}
	return sel, nil
}
