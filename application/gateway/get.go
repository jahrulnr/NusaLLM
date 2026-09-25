package appgateway

import (
	"context"
)

// Get returns one gateway as a sanitized projection — 404 when absent.
// It reads through Inspect, which never resolves the credential, so a
// record whose key ref is broken still renders (marked Broken) instead
// of erroring — this is the view that must show what's wrong.
func (app *GatewayApp) Get(ctx context.Context, id string) (GatewayInfo, error) {
	snap, err := app.gateways.Inspect(ctx, id)
	if err != nil {
		return GatewayInfo{}, err
	}
	return app.toInfo(ctx, snap), nil
}
