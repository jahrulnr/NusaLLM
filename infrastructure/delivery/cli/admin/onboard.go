package admin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"golang.org/x/term"
)

// Onboard is the `nusallm onboard` handler — the goclaw-style guided setup:
// a banner, numbered steps, prompts with defaults, and a summary. It
// produces exactly what `gateway add` would — the wizard is a friendlier
// front door, not a second provisioning path. Requires a TTY; scripted
// setup uses the gateway/config flags.
func Onboard(stdin *os.File, stdout, stderr io.Writer) int {
	if !term.IsTerminal(int(stdin.Fd())) {
		fmt.Fprintln(stderr, "nusallm: onboard needs an interactive terminal; use `nusallm gateway add` flags instead")
		return 2
	}
	b, err := LoadBoot(false)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	defer func() { _ = b.Close() }()
	out := bufio.NewWriter(stdout)
	defer out.Flush()
	in := bufio.NewReader(stdin)

	fmt.Fprintln(out, "╔══════════════════════════════════════════╗")
	fmt.Fprintln(out, "║       "+ProductName+" — provider setup        ║")
	fmt.Fprintln(out, "╚══════════════════════════════════════════╝")
	fmt.Fprintln(out)
	if existing, err := b.gateways.List(context.Background()); err == nil && len(existing) > 0 {
		fmt.Fprintf(out, "  %d gateway(s) already configured; adding another.\n\n", len(existing))
	}

	fmt.Fprintln(out, "── Provider ──")
	id, err := promptField(in, out, "  id        ", "openai")
	if err != nil {
		return promptAborted(out, err)
	}
	fmt.Fprintln(out, "  style     1) completion  2) responses  3) messages  4) generatecontent")
	stylePick, err := promptField(in, out, "  style     ", "1")
	if err != nil {
		return promptAborted(out, err)
	}
	style := enum.StyleCompletion
	switch strings.TrimSpace(stylePick) {
	case "1", "completion":
	case "2", "responses":
		style = enum.StyleResponses
	case "3", "messages":
		style = enum.StyleMessages
	case "4", "generatecontent":
		style = enum.StyleGenerateContent
	default:
		fmt.Fprintf(out, "  unknown style %q\n", stylePick)
		return 2
	}
	endpoint, err := promptField(in, out, "  endpoint  ", defaultEndpoint(style))
	if err != nil {
		return promptAborted(out, err)
	}
	modelsRaw, err := promptField(in, out, "  models    (comma-separated, empty to import the provider catalog)", "")
	if err != nil {
		return promptAborted(out, err)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "── Credential ──")
	fmt.Fprintln(out, "  Leave empty to skip; input is hidden and stored in the SecretStore,")
	fmt.Fprintln(out, "  never in the config file. Or set env:VAR later via `gateway set --key-env`.")
	key, err := promptSecret(stdin, out, "  API key   ", in)
	if err != nil {
		return promptAborted(out, err)
	}

	in2 := appgateway.GatewayInput{ID: id, Name: id, Style: style, Endpoint: endpoint}
	if key != "" {
		in2.ApiKey = &key
	}
	for _, m := range strings.Split(modelsRaw, ",") {
		if m = strings.TrimSpace(m); m != "" {
			in2.Models = append(in2.Models, appgateway.ModelInput{ID: m, Route: m})
		}
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, "  Writing gateway... ")
	gwOut, err := b.gateways.Upsert(context.Background(), in2)
	if err != nil {
		fmt.Fprintln(out, "FAILED")
		fmt.Fprintln(stderr, "nusallm:", err)
		return 1
	}
	fmt.Fprintln(out, "OK")
	if gwOut.CatalogError != "" {
		// The record saved — a catalog failure is a warning, never a
		// failed onboard.
		fmt.Fprintf(stderr, "nusallm: warning: model catalog import failed — %s\n", gwOut.CatalogError)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "╔══════════════════════════════════════════╗")
	fmt.Fprintln(out, "║           Setup complete                 ║")
	fmt.Fprintln(out, "╚══════════════════════════════════════════╝")
	fmt.Fprintln(out)
	keyNote := ""
	if key != "" {
		keyNote = " · key vaulted"
	}
	// The summary counts the stored catalog after the save-time merge —
	// imported provider ids included — not just the ids the operator
	// typed.
	modelCount := len(in2.Models) + len(gwOut.Added)
	if info, err := b.gateways.Get(context.Background(), id); err == nil {
		modelCount = len(info.Models)
	}
	fmt.Fprintf(out, "  gateway  %s (%s) · %d model(s)%s\n", id, style, modelCount, keyNote)
	if len(gwOut.Added) > 0 {
		fmt.Fprintf(out, "           imported %d model(s) from the provider catalog\n", len(gwOut.Added))
	}
	fmt.Fprintln(out, "  next:    "+CLIName+"           — start the REPL")
	fmt.Fprintf(out, "           /route %s then /model <id> — inside the session\n", id)
	return 0
}
