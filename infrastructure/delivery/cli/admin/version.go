package admin

import (
	"fmt"
	"io"
)

const versionUsage = `Usage: ` + CLIName + ` version

Prints the version this binary was built as. A source build without a
release version prints "dev".
`

// Version prints the build's version. It reads no config and makes no
// network call, so it answers on a machine that has nothing set up yet.
func Version(args []string, stdout, stderr io.Writer, version string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, versionUsage)
		return 0
	}
	if len(args) != 0 {
		fmt.Fprint(stderr, versionUsage)
		return 2
	}
	fmt.Fprintf(stdout, "%s %s\n", CLIName, version)
	return 0
}
