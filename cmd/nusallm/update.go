package main

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"time"

	appupdate "github.com/jahrulnr/nusallm/application/update"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/admin"
	repobinary "github.com/jahrulnr/nusallm/infrastructure/repository/binary"
	reprelease "github.com/jahrulnr/nusallm/infrastructure/service/release"
	"github.com/jahrulnr/nusallm/infrastructure/system"
)

// Where the update check looks, and the one command that takes the latest
// release by hand. The installer is the only install path, so an in-app update
// and a fresh install place the same file.
const (
	releaseAPI     = "https://api.github.com"
	releaseRepo    = "jahrulnr/NusaLLM"
	installCommand = "curl -fsSL https://raw.githubusercontent.com/jahrulnr/NusaLLM/master/scripts/install.sh | bash"
	// updateTTL caps how often the release source is asked while the WebUI
	// runs; every browser poll in between is answered from the cache.
	updateTTL = time.Hour
	// checksumsAsset is the file the release publishes beside its binaries.
	checksumsAsset = "SHA256SUMS"
)

// newUpdates wires the update check and the in-app install for this build.
// The install replaces the installed executable and restarts the WebUI through
// the host service manager, or replaces the running process when none owns it.
// lifetime bounds the install job, so shutting the server down stops it.
func newUpdates(svc system.Service, lifetime context.Context) (*appupdate.App, error) {
	bin, err := admin.InstalledBinPath()
	if err != nil {
		return nil, err
	}
	// Release binaries are built for linux only. Any other platform keeps the
	// check but cannot install, so a darwin build never fetches a linux file.
	asset := ""
	if runtime.GOOS == "linux" {
		asset = "nusallm-linux-" + runtime.GOARCH
	}
	source := reprelease.NewGitHub(&http.Client{}, releaseAPI, releaseRepo, "nusallm/"+version)
	return appupdate.New(appupdate.Deps{
		Source:         source,
		Binary:         repobinary.NewInstalled(bin),
		Restarter:      system.NewRestarter(svc, admin.CLIName, bin, os.Args),
		BinaryAsset:    asset,
		ChecksumsAsset: checksumsAsset,
		Lifetime:       lifetime,
	}, version, installCommand, updateTTL), nil
}
