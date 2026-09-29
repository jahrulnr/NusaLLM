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
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/application/setting"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/admin"
	httpdelivery "github.com/jahrulnr/nusallm/infrastructure/delivery/http"
	wsdelivery "github.com/jahrulnr/nusallm/infrastructure/delivery/websocket"
	wsconversation "github.com/jahrulnr/nusallm/infrastructure/delivery/websocket/conversation"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
	repofiletree "github.com/jahrulnr/nusallm/infrastructure/repository/filetree"
	"github.com/jahrulnr/nusallm/web"
)

const serveUsage = `Usage: ` + admin.CLIName + ` serve [--port N]

Local WebUI — embedded browser UI, HTTP API, and WebSocket turn channel.
Binds 127.0.0.1 only; port defaults to server.port in config.

Flags:
  --port N   loopback port for this process (1..65535)
`

// serve hosts the WebUI — embedded browser UI, HTTP API, and the
// WebSocket turn channel — on 127.0.0.1. The port and accepted HTTP
// hostnames come from server settings; --port can override the configured
// port for one process invocation.
func serve(args []string) (serveErr error) {
	if serveHelpRequested(args) {
		fmt.Print(serveUsage)
		return nil
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
	port, err := servePort(args, b.Config().Server.Port)
	if err != nil {
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

	// Model catalogs refresh on an hourly cadence, first pass at boot —
	// newly advertised ids merge into gateway records so the WebUI picker
	// sees them without a manual import. Outcomes land in the diagnostic
	// log; the loop dies with the signal context on shutdown.
	go b.Gateways().StartAutoImport(ctx, time.Hour, func(r appgateway.AutoImportResult) {
		eng.logger.Append(context.Background(), "gateway.autoimport", r)
	})

	gin.SetMode(gin.ReleaseMode)
	portText := strconv.Itoa(port)
	listenAuthority := net.JoinHostPort("127.0.0.1", portText)
	authorities := make([]string, 0, len(b.Config().Server.Hosts))
	for _, host := range b.Config().Server.Hosts {
		authorities = append(authorities, net.JoinHostPort(host, portText))
	}
	// The hub's context ignores the signal — Hub.Shutdown owns turn
	// cancellation so terminal frames land before teardown, never on
	// signal arrival. Deferred so an early Serve failure still closes it.
	hub := wsconversation.NewHub(context.WithoutCancel(ctx), eng.conversations)
	defer hub.Shutdown()
	settings := setting.New(repoconfig.NewLocal(), b.ConfigPath())
	handler := httpdelivery.NewHandler(httpdelivery.Deps{
		Conversations:      eng.conversations,
		ConversationWrites: eng.conversations,
		Gateways:           b.Gateways(),
		GatewayWrites:      b.Gateways(),
		Settings:           settings,
		SettingsWriter:     settings,
		Tools:              eng.conversations,
		FileTree:           repofiletree.NewLocal(),
		// Accepted delete/compaction jobs ride the server lifecycle —
		// a client that abandons a request can't cancel them, but
		// shutdown does.
		Jobs:        ctx,
		WS:          wsdelivery.NewHandler(hub, authorities).Serve,
		Authorities: authorities,
		WebAssets:   web.Assets,
	})
	// Host aliases only affect request validation. The socket stays on the
	// IPv4 loopback interface, independent of the configured host list.
	ln, err := net.Listen("tcp", listenAuthority)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAuthority, err)
	}
	srv := &http.Server{Handler: handler}
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ln) }()

	for _, authority := range authorities {
		fmt.Fprintf(os.Stderr, "%s: WebUI available at http://%s (loopback only)\n", admin.CLIName, authority)
	}
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

// servePort parses the optional one-process port override. With no flag,
// the effective value comes from server.port in config.json.
func servePort(args []string, configuredPort int) (int, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	port := fs.Int("port", configuredPort, "loopback port to bind for this process")
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

// serveHelpRequested detects a bareword/flag help request. A token
// consumed as a flag value — the arg after --port/-port — is not a help
// token (`--port=help` already fails in flag.Int).
func serveHelpRequested(args []string) bool {
	for i, a := range args {
		if i > 0 && (args[i-1] == "--port" || args[i-1] == "-port") {
			continue
		}
		switch a {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}
