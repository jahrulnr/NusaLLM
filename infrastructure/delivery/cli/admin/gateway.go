package admin

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	"golang.org/x/term"
)

// Gateway is the `nusallm gateway <sub>` handler — list/show/add/set/delete
// over the gateway application. Secret material always goes through the
// SecretStore; records keep references only.
func Gateway(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, gatewayUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, gatewayUsage)
		return 0
	}
	b, err := LoadBoot(false)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	defer func() { _ = b.Close() }()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			fmt.Fprint(stderr, gatewayUsage)
			return 2
		}
		return gatewayList(b, stdout, stderr)
	case "show":
		if len(args) != 2 {
			fmt.Fprint(stderr, gatewayUsage)
			return 2
		}
		return gatewayShow(b, args[1], stdout, stderr)
	case "add":
		return gatewayWrite(b, false, args[1:], stdout, stderr, stdin)
	case "set":
		return gatewayWrite(b, true, args[1:], stdout, stderr, stdin)
	case "delete":
		if len(args) != 2 {
			fmt.Fprint(stderr, gatewayUsage)
			return 2
		}
		id := args[1]
		if err := b.gateways.Delete(context.Background(), id); err != nil {
			printErr(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "deleted gateway %s\n", id)
		// Deleting also removes the canonical vault key — warn when a
		// surviving gateway references it, so the break is not silent.
		if refs, err := b.gateways.List(context.Background()); err == nil {
			canonical := valueobject.SecretRef(drepo.GatewaySecretKey(id))
			for _, g := range refs {
				if g.KeyRef == canonical {
					fmt.Fprintf(stderr, "nusallm: warning: gateway %s references %s — now unresolvable\n",
						g.ID, canonical)
				}
			}
		}
	default:
		fmt.Fprintf(stderr, "nusallm: unknown gateway subcommand %q\n", args[0])
		fmt.Fprint(stderr, gatewayUsage)
		return 2
	}
	return 0
}

const gatewayUsage = `Usage: nusallm gateway <subcommand> [flags]

  list                          list gateways — sanitized, never key material
  show <id>                     gateway detail and its model catalog
  add <id> --style <s> [flags]  register a provider route
  set <id> [flags]              edit fields; key flags rotate the credential
  delete <id>                   remove the gateway and its vaulted key

add/set flags:
  --style <s>         completion | responses | messages | generatecontent
  --endpoint <url>    provider base URL
  --name <name>       display name (default: id)
  --models a,b,c      model ids to store; provider ids merge on save (route = model id)
  --model <m>         same as --models with one entry
  --stream            enable streaming
  --no-stream         disable streaming
  --key-env <VAR>     store an env:VAR reference — resolved at send time
  --key <literal>     vault the literal into the SecretStore (leaks to ps/history)
  --key-stdin         read the key from one stdin line (no argv, no history)
  --clear-key         drop the credential entirely (set only)
`

// gatewayWrite implements add and set — same flag surface; set
// read-modify-writes the existing record through InputFromInfo so unset
// flags keep their stored values. add refuses an existing healthy id —
// a silent full replace would erase models and stream settings the
// operator never typed — but permits it on a broken record, where add is
// the repair path.
func gatewayWrite(b *Boot, edit bool, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	// The id is positional and precedes the flags — flag.Parse stops at
	// the first non-flag, so split it off before parsing.
	if len(args) == 0 {
		fmt.Fprint(stderr, gatewayUsage)
		return 2
	}
	id := args[0]
	// Path safety is the domain's identity rule; the transport adds two
	// argv-boundary constraints on top: a leading "-" means a flag landed
	// in the positional id slot, and whitespace would leave the gateway
	// unaddressable — /route and argv split on spaces.
	if strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \t\n") {
		fmt.Fprintf(stderr, "nusallm: invalid gateway id %q — no flags or whitespace\n", id)
		return 2
	}
	if err := valueobject.ValidID(id); err != nil {
		fmt.Fprintf(stderr, "nusallm: invalid gateway id %q — %s\n", id, err)
		return 2
	}
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, gatewayUsage) }
	style := fs.String("style", "", "completion | responses | messages | generatecontent")
	endpoint := fs.String("endpoint", "", "provider base URL")
	name := fs.String("name", "", "display name (default: id)")
	models := fs.String("models", "", "comma-separated model ids")
	fs.StringVar(models, "model", "", "alias for --models")
	streamOn := fs.Bool("stream", false, "enable streaming")
	noStream := fs.Bool("no-stream", false, "disable streaming")
	keyEnv := fs.String("key-env", "", "store an env:VAR key reference")
	keyLiteral := fs.String("key", "", "vault a literal key (leaks to ps/history)")
	keyStdin := fs.Bool("key-stdin", false, "read the key from stdin")
	clearKey := fs.Bool("clear-key", false, "drop the credential (set only)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(stderr, gatewayUsage)
		return 2
	}
	// Contradictory key flags are a usage error, not silent last-wins.
	var keyFlags []string
	modelsSet := false
	modelsFlagSet := false
	modelFlagSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "key-env", "key", "key-stdin", "clear-key":
			keyFlags = append(keyFlags, "--"+f.Name)
		case "models":
			modelsSet = true
			modelsFlagSet = true
		case "model":
			modelsSet = true
			modelFlagSet = true
		}
	})
	if modelsFlagSet && modelFlagSet {
		fmt.Fprintln(stderr, "nusallm: --models and --model conflict — pass exactly one")
		return 2
	}
	if len(keyFlags) > 1 {
		fmt.Fprintf(stderr, "nusallm: %s conflict — pass exactly one key flag\n",
			strings.Join(keyFlags, " and "))
		return 2
	}
	if *streamOn && *noStream {
		fmt.Fprintln(stderr, "nusallm: --stream and --no-stream conflict — pick one")
		return 2
	}
	if !edit && *clearKey {
		fmt.Fprintln(stderr, "nusallm: --clear-key is a `gateway set` flag — a new gateway has no key to clear")
		return 2
	}

	in := appgateway.GatewayInput{ID: id}
	if edit {
		info, err := b.gateways.Get(context.Background(), id)
		if err != nil {
			printErr(stderr, err)
			return 1
		}
		if info.Broken {
			fmt.Fprintf(stderr, "nusallm: gateway %s is corrupt — repair it with a full `gateway add %s ...` or delete it\n", id, id)
			return 1
		}
		in = appgateway.InputFromInfo(info)
	} else if existing, err := b.gateways.Get(context.Background(), id); err == nil && !existing.Broken {
		fmt.Fprintf(stderr, "nusallm: gateway %s already exists — edit it with `gateway set %s [flags]`\n", id, id)
		return 1
	}
	if *style != "" {
		s, ok := enum.ParseStyle(*style)
		if !ok {
			fmt.Fprintf(stderr, "nusallm: unknown style %q — completion | responses | messages | generatecontent\n", *style)
			return 2
		}
		in.Style = s
	}
	if *endpoint != "" {
		in.Endpoint = *endpoint
	}
	if *name != "" {
		in.Name = *name
	}
	if in.Name == "" {
		in.Name = id
	}
	if modelsSet {
		in.Models = nil
		for _, mid := range strings.Split(*models, ",") {
			mid = strings.TrimSpace(mid)
			if mid == "" {
				continue
			}
			in.Models = append(in.Models, appgateway.ModelInput{ID: mid, Route: mid})
		}
	}
	if *streamOn {
		stream := true
		in.Stream = &stream
	}
	if *noStream {
		stream := false
		in.Stream = &stream
	}
	// Required-field validation precedes any credential read — a failure
	// must not have already consumed stdin.
	if !edit && in.Style == "" {
		if !isTTY(stdin) {
			fmt.Fprintln(stderr, "nusallm: --style is required — completion | responses | messages | generatecontent (or run: nusallm onboard)")
			return 2
		}
	}
	if !edit && in.Endpoint == "" && !isTTY(stdin) {
		fmt.Fprintln(stderr, "nusallm: --endpoint is required — e.g. https://api.openai.com/v1 (or run: nusallm onboard)")
		return 2
	}
	if *clearKey {
		empty := ""
		in.ApiKey = &empty
	}
	if *keyEnv != "" {
		if strings.HasPrefix(*keyEnv, valueobject.KeyRefEnv) ||
			strings.HasPrefix(*keyEnv, valueobject.KeyRefSecret) {
			fmt.Fprintf(stderr, "nusallm: --key-env wants a bare variable name, not %q\n", *keyEnv)
			return 2
		}
		ref := valueobject.KeyRefEnv + *keyEnv
		in.ApiKey = &ref
	}
	if *keyLiteral != "" {
		fmt.Fprintln(stderr, "nusallm: note: a literal --key is visible in shell history and ps; prefer --key-stdin or --key-env")
		in.ApiKey = keyLiteral
	}
	if *keyStdin {
		k, err := readSecretLine(stdin, stderr, "API key: ")
		if err != nil {
			printErr(stderr, err)
			return 1
		}
		if k == "" {
			fmt.Fprintln(stderr, "nusallm: empty key on stdin")
			return 1
		}
		in.ApiKey = &k
	}
	// On a TTY, a bare `add <id>` drops into the same prompts onboard
	// uses — flags are the non-interactive path, not a wall.
	if f, tty := stdin.(*os.File); !edit && tty && term.IsTerminal(int(f.Fd())) {
		prompt := bufio.NewReader(f)
		if in.Style == "" {
			fmt.Fprintln(stderr, "  style  1) completion  2) responses  3) messages  4) generatecontent")
			pick, err := promptField(prompt, bufio.NewWriter(stderr), "  style", "1")
			if err != nil {
				fmt.Fprintln(stderr, "cancelled.")
				return 1
			}
			switch strings.TrimSpace(pick) {
			case "1", "completion":
				in.Style = enum.StyleCompletion
			case "2", "responses":
				in.Style = enum.StyleResponses
			case "3", "messages":
				in.Style = enum.StyleMessages
			case "4", "generatecontent":
				in.Style = enum.StyleGenerateContent
			default:
				fmt.Fprintf(stderr, "nusallm: unknown style %q\n", pick)
				return 2
			}
		}
		if in.Endpoint == "" {
			v, err := promptField(prompt, bufio.NewWriter(stderr), "  endpoint", defaultEndpoint(in.Style))
			if err != nil {
				fmt.Fprintln(stderr, "cancelled.")
				return 1
			}
			in.Endpoint = v
		}
		if !modelsSet {
			v, err := promptField(prompt, bufio.NewWriter(stderr), "  models (comma-separated, empty to import the provider catalog)", "")
			if err != nil {
				fmt.Fprintln(stderr, "cancelled.")
				return 1
			}
			for _, mid := range strings.Split(v, ",") {
				if mid = strings.TrimSpace(mid); mid != "" {
					in.Models = append(in.Models, appgateway.ModelInput{ID: mid, Route: mid})
				}
			}
		}
		// Interactive add with no key flag asks once, hidden — the key
		// never lands in argv, history, or scrollback. Bytes the field
		// reader already buffered (a multi-line paste) are the key line.
		if in.ApiKey == nil && !*clearKey {
			if k, err := readSecretLine(stdin, stderr, "API key (input hidden, empty to skip): ", prompt); err == nil && k != "" {
				in.ApiKey = &k
			}
		}
	}
	if in.Endpoint == "" {
		fmt.Fprintln(stderr, "nusallm: --endpoint is required — e.g. https://api.openai.com/v1 (or run: nusallm onboard)")
		return 2
	}
	out, err := b.gateways.Upsert(context.Background(), in)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	verb := "added"
	if edit {
		verb = "updated"
	}
	fmt.Fprintf(stdout, "%s gateway %s\n", verb, id)
	if len(out.Added) > 0 {
		fmt.Fprintf(stdout, "imported %d model(s) from the provider catalog\n", len(out.Added))
	}
	if out.CatalogError != "" {
		// The record saved — the import failure is a warning, not a
		// failure of the command itself.
		fmt.Fprintf(stderr, "nusallm: warning: model catalog import failed — %s\n", out.CatalogError)
	}
	return 0
}

func gatewayList(b *Boot, stdout, stderr io.Writer) int {
	list, err := b.gateways.List(context.Background())
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "no gateways configured — see nusallm gateway add --help")
		return 0
	}
	for _, g := range list {
		printGatewayRow(stdout, g)
	}
	return 0
}

func gatewayShow(b *Boot, id string, stdout, stderr io.Writer) int {
	g, err := b.gateways.Get(context.Background(), id)
	if err != nil {
		printErr(stderr, err)
		return 1
	}
	printGatewayRow(stdout, g)
	fmt.Fprintf(stdout, "  endpoint  %s\n  stream    %v\n", g.Endpoint, g.Stream)
	if len(g.Models) == 0 {
		fmt.Fprintln(stdout, "  models    (none)")
		return 0
	}
	fmt.Fprintln(stdout, "  models:")
	for _, m := range g.Models {
		printModelRow(stdout, m)
	}
	return 0
}

func printGatewayRow(w io.Writer, g appgateway.GatewayInfo) {
	key := "no-key"
	if g.KeySet {
		key = "key-set"
	}
	if g.KeyRef != "" {
		key = g.KeyRef
	}
	broken := ""
	if g.Broken {
		broken = " broken"
	}
	fmt.Fprintf(w, "%s  %-10s style=%-10s models=%d key=%s%s\n",
		g.ID, g.Name, g.Style, len(g.Models), key, broken)
}

func printModelRow(w io.Writer, m entity.Model) {
	var extra []string
	if m.ContextWindow() > 0 {
		extra = append(extra, fmt.Sprintf("ctx=%d", m.ContextWindow()))
	}
	if t := m.ModelType(); t != "" {
		extra = append(extra, "type="+t.String())
	}
	if mods := m.InputModalities(); len(mods) > 0 {
		var s []string
		for _, mod := range mods {
			s = append(s, mod.String())
		}
		extra = append(extra, "in="+strings.Join(s, ","))
	}
	fmt.Fprintf(w, "    %s  route=%s", m.ID(), m.Route())
	if len(extra) > 0 {
		fmt.Fprintf(w, "  %s", strings.Join(extra, " "))
	}
	fmt.Fprintln(w)
}
