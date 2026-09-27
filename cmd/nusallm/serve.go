package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jahrulnr/nusallm/application/setting"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/admin"
	httpdelivery "github.com/jahrulnr/nusallm/infrastructure/delivery/http"
	wsdelivery "github.com/jahrulnr/nusallm/infrastructure/delivery/websocket"
	wsconversation "github.com/jahrulnr/nusallm/infrastructure/delivery/websocket/conversation"
)

// serve hosts the WebUI's HTTP API and WebSocket turn channel on
// loopback — reads, mutations, and `turn.send` streaming under the
// security envelope of docs/api/contract.md (turn edit/cancel/watch
// remain future work). The bind address is fixed to 127.0.0.1; only the
// port is a flag, matching the contract's "no remotely configurable
// listen" rule.
func serve(args []string) (serveErr error) {
	port, err := servePort(args)
	if err != nil {
		return err
	}
	b, err := admin.LoadBoot(true)
	if err != nil {
		return err
	}
	// Deferred before EnsureConfig so a config failure still closes the
	// secret store.
	defer func() {
		if closeErr := b.Close(); serveErr == nil {
			serveErr = closeErr
		}
	}()
	if err := admin.EnsureConfig(b.ConfigPath(), os.Stderr); err != nil {
		return err
	}
	eng, err := compose(b.Config(), b.Secrets(), b.ConfigPath())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := eng.logger.Close(); serveErr == nil {
			serveErr = closeErr
		}
	}()
	defer func() {
		// Registered before Listen/Serve so an early failure or a
		// shutdown error still retires the conversation workers.
		if shutdownErr := eng.conversations.Shutdown(context.Background()); serveErr == nil {
			serveErr = shutdownErr
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gin.SetMode(gin.ReleaseMode)
	authority := fmt.Sprintf("127.0.0.1:%d", port)
	// The hub's context ignores the signal — Hub.Shutdown owns turn
	// cancellation so terminal frames land before teardown, never on
	// signal arrival. Deferred so an early Serve failure still closes it.
	hub := wsconversation.NewHub(context.WithoutCancel(ctx), eng.conversations)
	defer hub.Shutdown()
	settings := setting.New(b.ConfigPath())
	handler := httpdelivery.NewHandler(httpdelivery.Deps{
		Conversations:      eng.conversations,
		ConversationWrites: eng.conversations,
		Gateways:           b.Gateways(),
		GatewayWrites:      b.Gateways(),
		Settings:           settings,
		SettingsWriter:     settings,
		Tools:              eng.conversations,
		// Accepted delete/compaction jobs ride the server lifecycle —
		// a client that abandons a request can't cancel them, but
		// shutdown does.
		Jobs:      ctx,
		WS:        wsdelivery.NewHandler(hub, authority).Serve,
		Authority: authority,
	})
	// Loopback only by construction — net.Listen binds the address, and
	// the handler's Host guard rejects anything else anyway.
	ln, err := net.Listen("tcp", authority)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", authority, err)
	}
	srv := &http.Server{Handler: handler}
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ln) }()

	fmt.Fprintf(os.Stderr, "%s: WebUI API listening on http://%s (loopback only)\n", admin.CLIName, authority)
	select {
	case err := <-serveDone:
		return err // Serve only returns early on a listener failure
	case <-ctx.Done():
	}

	// Hijacked WebSocket conns are invisible to http.Server.Shutdown —
	// the hub drains sockets and lands terminal frames first, on its own
	// context that outlived the signal.
	hub.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// servePort parses `serve`'s flags — currently just --port.
func servePort(args []string) (int, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	port := fs.Int("port", 8080, "loopback port to bind")
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	if fs.NArg() > 0 {
		return 0, fmt.Errorf("serve: unexpected argument %q", fs.Arg(0))
	}
	if *port < 1 || *port > 65535 {
		return 0, fmt.Errorf("serve: --port must be 1..65535, got %d", *port)
	}
	return *port, nil
}
