package appgateway

import (
	"context"
)

// List returns every configured gateway, sorted by id — backs the model
// picker and the provider management view. Unlike the repository's
// picker-facing List, InspectList surfaces broken records too: an
// unresolvable key or corrupt file appears marked Broken rather than
// vanishing, since this is the view an operator fixes things from.
func (app *GatewayApp) List(ctx context.Context) ([]GatewayInfo, error) {
	snaps, err := app.gateways.InspectList(ctx)
	if err != nil {
		return nil, err
	}
	var out []GatewayInfo
	for _, snap := range snaps {
		out = append(out, app.toInfo(ctx, snap))
	}
	return out, nil
}
