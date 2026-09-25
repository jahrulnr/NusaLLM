package appgateway

import (
	"context"
)

// Delete removes the gateway and its vaulted credential — 404 when
// absent. Conversations bound to the route are not touched: their
// stored reference dangles, and the next send surfaces the 404 the app
// layer already routes on, letting the caller rebind.
func (app *GatewayApp) Delete(ctx context.Context, id string) error {
	return app.gateways.Delete(ctx, id)
}
