package admin

import (
	"fmt"
	"io"

	"github.com/jahrulnr/nusallm/application/setting"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
)

// Config is the `nusallm config <sub>` handler — show/path/set/unset/
// validate over the config file. It resolves only the file path here;
// each setting operation handles validation or repair of file contents.
func Config(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, configUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, configUsage)
		return 0
	}
	// Config file operations need its location, not a decoded storage.dir.
	// In particular, unset must work when storage.dir itself is malformed.
	settings := setting.New(repoconfig.NewLocal(), resolveConfigPath())
	switch args[0] {
	case "show":
		if len(args) != 1 {
			fmt.Fprint(stderr, configUsage)
			return 2
		}
		out, err := settings.Show()
		if err != nil {
			printErr(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
	case "path":
		if len(args) != 1 {
			fmt.Fprint(stderr, configUsage)
			return 2
		}
		if settings.Path() == "" {
			fmt.Fprintln(stdout, "(no config file — running on defaults)")
		} else {
			fmt.Fprintln(stdout, settings.Path())
		}
	case "validate":
		if len(args) != 1 {
			fmt.Fprint(stderr, configUsage)
			return 2
		}
		if err := settings.Validate(); err != nil {
			printErr(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "config at %s is valid\n", settings.Path())
	case "set":
		if len(args) != 3 {
			fmt.Fprint(stderr, configUsage)
			return 2
		}
		out, err := settings.Set(args[1], args[2])
		if err != nil {
			printErr(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
		fmt.Fprintln(stdout, "takes effect on next start")
	case "unset":
		if len(args) != 2 {
			fmt.Fprint(stderr, configUsage)
			return 2
		}
		out, err := settings.Unset(args[1])
		if err != nil {
			printErr(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
		fmt.Fprintln(stdout, "takes effect on next start")
	default:
		fmt.Fprintf(stderr, "nusallm: unknown config subcommand %q\n", args[0])
		fmt.Fprint(stderr, configUsage)
		return 2
	}
	return 0
}

const configUsage = `Usage: nusallm config <subcommand>

  show                 effective config — file values over defaults
  path                 resolved config file path
  set <key> <value>    patch a dotted leaf — e.g. service.idle_timeout 2m
  unset <key>          remove a key — repairs files validate rejects
  validate             check the file decodes strictly
`
