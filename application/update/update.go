// Package update is the use case behind "is a newer NusaLLM release out, and
// can this process take it?". Checking reads one cached answer: when to ask
// the release source is application policy here, and whether a release is
// newer than the running build is the domain's Version.NewerThan decision.
// Installing is one background job: download, verify, install, restart.
package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

const (
	// checkTimeout bounds one release lookup. The lookup is shared by every
	// caller, so it must not run unbounded when the network is stuck.
	checkTimeout = 15 * time.Second
	// installTimeout bounds one whole install, so a stalled download cannot
	// hold the single install slot forever.
	installTimeout = 20 * time.Minute
	// maxBinary and maxChecksums cap what an install accepts from a release.
	maxBinary    = 256 << 20
	maxChecksums = 64 << 10
	// restartLead gives the progress view time to show the restart phase
	// before the process goes away.
	restartLead = 1200 * time.Millisecond
)

var (
	// ErrBusy means an install is already running.
	ErrBusy = errors.New("an update is already running")
	// ErrDisabled means this build cannot check for or install releases.
	ErrDisabled = errors.New("updates are not available for this build")
	// ErrNoUpdate means the latest known release is not newer than this build.
	ErrNoUpdate = errors.New("no newer release is available")
	// ErrNoRelease means no release has been looked up yet.
	ErrNoRelease = errors.New("no release is known yet; check for updates first")
)

// Info is the update picture a caller renders. Current is the running build's
// version text ("dev" for a source build). Enabled is false when the build has
// no release version, so no lookup ever runs for it. Installable is false where
// this build cannot replace itself with a release file.
type Info struct {
	Current        string
	Enabled        bool
	Installable    bool
	Latest         string
	Available      bool
	PublishedAt    time.Time
	URL            string
	Notes          string
	InstallCommand string
	CheckedAt      time.Time
	Error          string
}

// Phase is the step an install is on. Restart is the last one.
type Phase string

const (
	PhaseDownload Phase = "download"
	PhaseVerify   Phase = "verify"
	PhaseInstall  Phase = "install"
	PhaseRestart  Phase = "restart"
)

// State is where an install stands.
type State string

const (
	StateIdle      State = "idle"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
)

// Progress is what an install reports while it runs and after it ends.
// Restart names the restart path once the restart phase begins: "service" when
// the host service manager restarts the WebUI, "process" for a foreground run
// that replaces itself.
type Progress struct {
	State      State
	Phase      Phase
	Version    string
	BytesDone  int64
	BytesTotal int64
	Restart    string
	Error      string
}

// Restarter replaces the running WebUI with the freshly installed executable.
// Mode says which path applies; Restart takes it. The "process" path does not
// return on success, because the process it replaces is the caller.
type Restarter interface {
	Mode() string
	Restart(mode string) error
}

// Deps are the outside capabilities an update needs. The asset names are
// release conventions the composition root owns. BinaryAsset is empty on a
// platform the release does not build for, which turns installing off there.
type Deps struct {
	Source         service.ReleaseSource
	Binary         drepo.InstalledBinary
	Restarter      Restarter
	BinaryAsset    string
	ChecksumsAsset string
	// Lifetime bounds background work; cancelling it stops an install.
	Lifetime context.Context
}

// App answers update queries from one cached release lookup and runs at most
// one install at a time.
type App struct {
	deps           Deps
	current        valueobject.Version
	currentText    string
	enabled        bool
	installCommand string
	ttl            time.Duration
	now            func() time.Time
	restartLead    time.Duration

	// cacheMu guards the release cache. A caller arriving while a lookup is in
	// flight waits and reads its result, so a burst of polls costs one request.
	cacheMu    sync.Mutex
	attempted  time.Time
	release    valueobject.Release
	hasRelease bool
	checkedAt  time.Time
	lastErr    string

	// jobMu guards the install. It is separate from cacheMu so a progress poll
	// never waits behind a release lookup.
	jobMu sync.Mutex
	job   Progress
}

// New binds the check and the install to their capabilities, the running
// build's version text, the command that installs the latest release, and how
// long a lookup stays fresh. A version that is not MAJOR.MINOR.PATCH disables
// checking.
func New(deps Deps, current, installCommand string, ttl time.Duration) *App {
	if deps.Lifetime == nil {
		deps.Lifetime = context.Background()
	}
	a := &App{
		deps:           deps,
		currentText:    current,
		installCommand: installCommand,
		ttl:            ttl,
		now:            time.Now,
		restartLead:    restartLead,
		job:            Progress{State: StateIdle},
	}
	if v, err := valueobject.ParseVersion(current); err == nil {
		a.current, a.enabled = v, true
	}
	return a
}

// Status returns the cached picture. It looks the source up first when no
// lookup has run yet or the last attempt is older than the TTL.
func (a *App) Status(ctx context.Context) Info {
	return a.refresh(ctx, false)
}

// Check looks the source up now, whatever the cache says.
func (a *App) Check(ctx context.Context) Info {
	return a.refresh(ctx, true)
}

func (a *App) refresh(ctx context.Context, force bool) Info {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if a.enabled && (force || a.attempted.IsZero() || a.now().Sub(a.attempted) >= a.ttl) {
		a.attempted = a.now()
		// The lookup is shared by every caller, so it must not die with the
		// request that happened to start it.
		lookup, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
		defer cancel()
		rel, err := a.deps.Source.Latest(lookup)
		if err != nil {
			a.lastErr = err.Error()
		} else {
			a.release, a.hasRelease, a.checkedAt, a.lastErr = rel, true, a.now(), ""
		}
	}
	return a.info()
}

// info projects the cache. A failed later attempt keeps the last good release
// visible next to its error, so a flaky network never hides a known update.
func (a *App) info() Info {
	info := Info{
		Current:        a.currentText,
		Enabled:        a.enabled,
		Installable:    a.installable(),
		InstallCommand: a.installCommand,
		CheckedAt:      a.checkedAt,
		Error:          a.lastErr,
	}
	if a.hasRelease {
		v := a.release.Version()
		info.Latest = v.String()
		info.Available = a.enabled && v.NewerThan(a.current)
		info.PublishedAt = a.release.PublishedAt()
		info.URL = a.release.URL()
		info.Notes = a.release.Notes()
	}
	return info
}

func (a *App) installable() bool {
	return a.enabled && a.deps.BinaryAsset != ""
}

// Install starts the update to the latest known release and returns at once.
// The download, checksum check, install, and restart run in the background;
// Progress reports them. Only one install runs at a time.
func (a *App) Install() (Progress, error) {
	if !a.installable() {
		return a.Progress(), ErrDisabled
	}
	a.cacheMu.Lock()
	rel, has := a.release, a.hasRelease
	a.cacheMu.Unlock()
	if !has {
		return a.Progress(), ErrNoRelease
	}
	if !rel.Version().NewerThan(a.current) {
		return a.Progress(), ErrNoUpdate
	}
	a.jobMu.Lock()
	defer a.jobMu.Unlock()
	if a.job.State == StateRunning {
		return a.job, ErrBusy
	}
	a.job = Progress{State: StateRunning, Phase: PhaseDownload, Version: rel.Version().String()}
	go a.run(rel)
	return a.job, nil
}

// Progress reports the latest install, or an idle state before any has run.
func (a *App) Progress() Progress {
	a.jobMu.Lock()
	defer a.jobMu.Unlock()
	return a.job
}

// run is one install from download to restart. Each phase either hands the
// next one a verified file or stops with its error recorded. The installed
// executable is replaced only by a committed, checksum-matched file, so every
// failure before the commit leaves the running executable as it was.
func (a *App) run(rel valueobject.Release) {
	ctx, cancel := context.WithTimeout(a.deps.Lifetime, installTimeout)
	defer cancel()
	version := rel.Version().String()

	bin, ok := rel.Asset(a.deps.BinaryAsset)
	if !ok {
		a.fail(PhaseDownload, fmt.Errorf("release %s has no %s file", version, a.deps.BinaryAsset))
		return
	}
	sums, ok := rel.Asset(a.deps.ChecksumsAsset)
	if !ok {
		a.fail(PhaseDownload, fmt.Errorf("release %s has no %s file", version, a.deps.ChecksumsAsset))
		return
	}

	staged, err := a.deps.Binary.Stage()
	if err != nil {
		a.fail(PhaseDownload, err)
		return
	}
	a.setTotal(bin.Size())
	digest := sha256.New()
	counted := &byteCounter{a: a}
	if _, err := a.deps.Source.Fetch(ctx, bin.URL(), io.MultiWriter(staged, digest, counted), maxBinary); err != nil {
		_ = staged.Discard()
		a.fail(PhaseDownload, err)
		return
	}

	a.setPhase(PhaseVerify)
	var listing bytes.Buffer
	if _, err := a.deps.Source.Fetch(ctx, sums.URL(), &listing, maxChecksums); err != nil {
		_ = staged.Discard()
		a.fail(PhaseVerify, err)
		return
	}
	checks, err := valueobject.ParseChecksums(listing.String())
	if err == nil {
		err = checks.Verify(bin.Name(), hex.EncodeToString(digest.Sum(nil)))
	}
	if err != nil {
		_ = staged.Discard()
		a.fail(PhaseVerify, err)
		return
	}

	a.setPhase(PhaseInstall)
	if err := staged.Commit(); err != nil {
		a.fail(PhaseInstall, err)
		return
	}

	mode := a.deps.Restarter.Mode()
	a.setRestart(mode)
	select {
	case <-time.After(a.restartLead):
	case <-ctx.Done():
		a.fail(PhaseRestart, fmt.Errorf("installed %s; the restart was cancelled", version))
		return
	}
	if err := a.deps.Restarter.Restart(mode); err != nil {
		a.fail(PhaseRestart, fmt.Errorf("installed %s, but restarting failed: %w", version, err))
		return
	}
	a.succeed()
}

func (a *App) setTotal(total int64) {
	a.jobMu.Lock()
	a.job.BytesTotal, a.job.BytesDone = total, 0
	a.jobMu.Unlock()
}

func (a *App) setPhase(p Phase) {
	a.jobMu.Lock()
	a.job.Phase = p
	a.jobMu.Unlock()
}

func (a *App) setRestart(mode string) {
	a.jobMu.Lock()
	a.job.Phase, a.job.Restart = PhaseRestart, mode
	a.jobMu.Unlock()
}

func (a *App) addBytes(n int64) {
	a.jobMu.Lock()
	a.job.BytesDone += n
	a.jobMu.Unlock()
}

func (a *App) fail(phase Phase, err error) {
	a.jobMu.Lock()
	a.job.State, a.job.Phase, a.job.Error = StateFailed, phase, err.Error()
	a.jobMu.Unlock()
}

func (a *App) succeed() {
	a.jobMu.Lock()
	a.job.State = StateSucceeded
	a.jobMu.Unlock()
}

// byteCounter feeds downloaded bytes into the install's progress.
type byteCounter struct{ a *App }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.a.addBytes(int64(len(p)))
	return len(p), nil
}
