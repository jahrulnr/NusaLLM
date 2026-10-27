package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

const (
	ttl      = time.Hour
	binName  = "nusallm-linux-amd64"
	binURL   = "https://dl.test/bin"
	sumsURL  = "https://dl.test/sums"
	binPath  = "/home/u/.local/bin/nusallm"
	relNotes = "### Added\n\n- thing"
)

// fakeSource answers lookups from a scripted release and serves files by URL.
type fakeSource struct {
	mu        sync.Mutex
	calls     int
	sawCancel bool
	rel       valueobject.Release
	relErr    error
	files     map[string][]byte
	fetchErr  map[string]error
	gate      chan struct{} // when set, the binary fetch waits for it to close
}

func (f *fakeSource) Latest(ctx context.Context) (valueobject.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if ctx.Err() != nil {
		f.sawCancel = true
	}
	return f.rel, f.relErr
}

func (f *fakeSource) Fetch(_ context.Context, url string, dst io.Writer, limit int64) (int64, error) {
	if f.gate != nil && url == binURL {
		<-f.gate
	}
	f.mu.Lock()
	err, body := f.fetchErr[url], f.files[url]
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if int64(len(body)) > limit {
		return 0, errors.New("file exceeds limit")
	}
	n, err := dst.Write(body)
	return int64(n), err
}

func (f *fakeSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeBinary stages in memory and records what was committed or discarded.
type fakeBinary struct {
	mu        sync.Mutex
	installed []byte
	stagings  []*fakeStaging
}

func (b *fakeBinary) Path() string { return binPath }

func (b *fakeBinary) Stage() (drepo.BinaryStaging, error) {
	s := &fakeStaging{owner: b}
	b.mu.Lock()
	b.stagings = append(b.stagings, s)
	b.mu.Unlock()
	return s, nil
}

// fakeStaging buffers writes; Commit publishes the buffer as the installed file.
type fakeStaging struct {
	owner     *fakeBinary
	buf       bytes.Buffer
	committed bool
	discarded bool
}

func (s *fakeStaging) Write(p []byte) (int, error) { return s.buf.Write(p) }

func (s *fakeStaging) Commit() error {
	s.committed = true
	s.owner.mu.Lock()
	s.owner.installed = s.buf.Bytes()
	s.owner.mu.Unlock()
	return nil
}

func (s *fakeStaging) Discard() error {
	s.discarded = true
	return nil
}

// fakeRestarter records which path a restart took.
type fakeRestarter struct {
	mode  string
	err   error
	mu    sync.Mutex
	calls []string
}

func (r *fakeRestarter) Mode() string { return r.mode }

func (r *fakeRestarter) Restart(mode string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, mode)
	return r.err
}

func (r *fakeRestarter) restarted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func version(t *testing.T, s string) valueobject.Version {
	t.Helper()
	v, err := valueobject.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// release builds a release whose binary and checksums are served by fakeSource.
func release(t *testing.T, v string, payload []byte, sums string) *fakeSource {
	t.Helper()
	bin, err := valueobject.NewAsset(binName, binURL, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	sumAsset, err := valueobject.NewAsset("SHA256SUMS", sumsURL, int64(len(sums)))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := valueobject.NewRelease(version(t, v), "https://gh.test/releases/"+v, relNotes,
		time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), []valueobject.Asset{bin, sumAsset})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSource{
		rel:      rel,
		files:    map[string][]byte{binURL: payload, sumsURL: []byte(sums)},
		fetchErr: map[string]error{},
	}
}

// goodSums is the SHA256SUMS line sha256sum would write for payload.
func goodSums(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]) + "  " + binName + "\n"
}

func newApp(t *testing.T, src *fakeSource, current string, bin *fakeBinary, rst *fakeRestarter) *App {
	t.Helper()
	app := New(Deps{
		Source:         src,
		Binary:         bin,
		Restarter:      rst,
		BinaryAsset:    binName,
		ChecksumsAsset: "SHA256SUMS",
		Lifetime:       context.Background(),
	}, current, "curl -fsSL install.sh | bash", ttl)
	app.restartLead = 0
	return app
}

// clock is a settable time source for the release cache.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// waitDone polls until the install leaves the running state.
func waitDone(t *testing.T, app *App) Progress {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := app.Progress(); p.State != StateRunning {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("install did not finish")
	return Progress{}
}

// --- release check ---------------------------------------------------------

func TestDevBuildNeverAsksTheSource(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	app := newApp(t, src, "dev", &fakeBinary{}, &fakeRestarter{mode: "process"})
	info := app.Status(context.Background())
	if info.Enabled || info.Installable || info.Available || info.Latest != "" {
		t.Fatalf("dev build reported update state: %+v", info)
	}
	if info.Current != "dev" {
		t.Fatalf("current = %q, want dev", info.Current)
	}
	if n := src.count(); n != 0 {
		t.Fatalf("dev build asked the source %d times", n)
	}
}

func TestNonSemverCurrentDisablesCheck(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	app := newApp(t, src, "0.1", &fakeBinary{}, &fakeRestarter{})
	if info := app.Status(context.Background()); info.Enabled {
		t.Fatal("a non-MAJOR.MINOR.PATCH build was treated as a release")
	}
	if src.count() != 0 {
		t.Fatal("non-release build asked the source")
	}
}

func TestNewerReleaseIsAvailable(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	info := app.Status(context.Background())
	if !info.Enabled || !info.Available || !info.Installable {
		t.Fatalf("0.1.0 over 0.0.0 not available and installable: %+v", info)
	}
	if info.Latest != "0.1.0" || info.URL != "https://gh.test/releases/0.1.0" || info.Notes != relNotes {
		t.Fatalf("release fields lost: %+v", info)
	}
	if info.InstallCommand != "curl -fsSL install.sh | bash" {
		t.Fatalf("install command = %q", info.InstallCommand)
	}
}

func TestSameOrOlderReleaseIsNotAvailable(t *testing.T) {
	for _, tc := range []struct{ current, latest string }{
		{"0.1.0", "0.1.0"},
		{"0.10.0", "0.9.9"},
	} {
		src := release(t, tc.latest, []byte("x"), goodSums([]byte("x")))
		info := newApp(t, src, tc.current, &fakeBinary{}, &fakeRestarter{}).Status(context.Background())
		if info.Available {
			t.Fatalf("%s running, %s latest: available=true", tc.current, tc.latest)
		}
		if info.Latest != tc.latest {
			t.Fatalf("latest = %q, want %q", info.Latest, tc.latest)
		}
	}
}

func TestStatusCachesWithinTTL(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	c := &clock{t: time.Unix(1000, 0)}
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	app.now = c.now
	app.Status(context.Background())
	c.t = c.t.Add(ttl - time.Minute)
	app.Status(context.Background())
	if n := src.count(); n != 1 {
		t.Fatalf("lookups inside TTL = %d, want 1", n)
	}
	c.t = c.t.Add(2 * time.Minute)
	app.Status(context.Background())
	if n := src.count(); n != 2 {
		t.Fatalf("lookups after TTL = %d, want 2", n)
	}
}

func TestCheckBypassesCache(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	c := &clock{t: time.Unix(1000, 0)}
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	app.now = c.now
	app.Status(context.Background())
	app.Check(context.Background())
	if n := src.count(); n != 2 {
		t.Fatalf("Check inside TTL made %d lookups total, want 2", n)
	}
}

func TestFailureKeepsLastReleaseAndClearsOnRecovery(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	c := &clock{t: time.Unix(1000, 0)}
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	app.now = c.now
	app.Status(context.Background())

	src.mu.Lock()
	src.relErr = errors.New("release check: GitHub answered 503")
	src.mu.Unlock()
	c.t = c.t.Add(2 * ttl)
	info := app.Status(context.Background())
	if info.Error == "" || info.Latest != "0.1.0" || !info.Available {
		t.Fatalf("failed refresh lost the known update: %+v", info)
	}

	src.mu.Lock()
	src.relErr = nil
	src.mu.Unlock()
	c.t = c.t.Add(2 * ttl)
	if info := app.Status(context.Background()); info.Error != "" {
		t.Fatalf("error survived a successful lookup: %q", info.Error)
	}
}

func TestFailedLookupWaitsForTTLBeforeRetrying(t *testing.T) {
	src := &fakeSource{relErr: errors.New("offline")}
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	app.Status(context.Background())
	app.Status(context.Background())
	if n := src.count(); n != 1 {
		t.Fatalf("retried inside TTL after a failure: %d lookups", n)
	}
}

func TestConcurrentStatusSharesOneLookup(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			app.Status(context.Background())
		}()
	}
	wg.Wait()
	if n := src.count(); n != 1 {
		t.Fatalf("20 concurrent polls made %d lookups, want 1", n)
	}
}

func TestLookupSurvivesCallerCancel(t *testing.T) {
	src := release(t, "0.1.0", []byte("x"), goodSums([]byte("x")))
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	info := app.Status(ctx)
	if src.sawCancel {
		t.Fatal("a canceled caller canceled the shared lookup")
	}
	if !info.Available {
		t.Fatalf("lookup result lost: %+v", info)
	}
}

// --- install ----------------------------------------------------------------

func TestInstallCommitsVerifiedBinaryAndRestartsService(t *testing.T) {
	payload := []byte("new-nusallm-binary")
	src := release(t, "0.1.0", payload, goodSums(payload))
	bin := &fakeBinary{}
	rst := &fakeRestarter{mode: "service"}
	app := newApp(t, src, "0.0.0", bin, rst)
	app.Status(context.Background())

	if _, err := app.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	final := waitDone(t, app)
	if final.State != StateSucceeded {
		t.Fatalf("final = %+v, want succeeded", final)
	}
	if !bytes.Equal(bin.installed, payload) {
		t.Fatalf("installed %q, want the verified payload", bin.installed)
	}
	if got := rst.restarted(); len(got) != 1 || got[0] != "service" {
		t.Fatalf("restart calls = %v, want [service]", got)
	}
	if final.Restart != "service" || final.Version != "0.1.0" {
		t.Fatalf("progress lost restart mode or version: %+v", final)
	}
	if final.BytesTotal != int64(len(payload)) || final.BytesDone != int64(len(payload)) {
		t.Fatalf("bytes = %d/%d, want %d/%d", final.BytesDone, final.BytesTotal, len(payload), len(payload))
	}
}

func TestInstallRejectsChecksumMismatchWithoutTouchingBinary(t *testing.T) {
	payload := []byte("tampered")
	wrong := goodSums([]byte("expected"))
	src := release(t, "0.1.0", payload, wrong)
	bin := &fakeBinary{installed: []byte("running")}
	rst := &fakeRestarter{mode: "service"}
	app := newApp(t, src, "0.0.0", bin, rst)
	app.Status(context.Background())

	app.Install()
	final := waitDone(t, app)
	if final.State != StateFailed || final.Phase != PhaseVerify {
		t.Fatalf("final = %+v, want failed in verify", final)
	}
	if bytes.Equal(bin.installed, payload) || string(bin.installed) != "running" {
		t.Fatalf("installed binary replaced on a bad checksum: %q", bin.installed)
	}
	if len(bin.stagings) != 1 || bin.stagings[0].committed || !bin.stagings[0].discarded {
		t.Fatalf("staging not discarded: %+v", bin.stagings)
	}
	if len(rst.restarted()) != 0 {
		t.Fatalf("restarted after a failed verify: %v", rst.restarted())
	}
}

func TestInstallDownloadFailureKeepsBinary(t *testing.T) {
	payload := []byte("new")
	src := release(t, "0.1.0", payload, goodSums(payload))
	src.fetchErr[binURL] = errors.New("connection reset by peer")
	bin := &fakeBinary{installed: []byte("running")}
	app := newApp(t, src, "0.0.0", bin, &fakeRestarter{})
	app.Status(context.Background())

	app.Install()
	final := waitDone(t, app)
	if final.State != StateFailed || final.Phase != PhaseDownload {
		t.Fatalf("final = %+v, want failed in download", final)
	}
	if string(bin.installed) != "running" || !bin.stagings[0].discarded {
		t.Fatalf("download failure touched the installed binary: %q", bin.installed)
	}
}

func TestRestartFailureReportsInstalledVersion(t *testing.T) {
	payload := []byte("new")
	src := release(t, "0.1.0", payload, goodSums(payload))
	bin := &fakeBinary{}
	rst := &fakeRestarter{mode: "service", err: errors.New("systemd bus is down")}
	app := newApp(t, src, "0.0.0", bin, rst)
	app.Status(context.Background())

	app.Install()
	final := waitDone(t, app)
	if final.State != StateFailed || final.Phase != PhaseRestart {
		t.Fatalf("final = %+v, want failed in restart", final)
	}
	if !bytes.Equal(bin.installed, payload) {
		t.Fatal("the new binary was not kept after a restart failure")
	}
	if want := "installed 0.1.0, but restarting failed"; !strings.Contains(final.Error, want) {
		t.Fatalf("error = %q, want it to say %q", final.Error, want)
	}
}

func TestProcessModeRestartsByReplacement(t *testing.T) {
	payload := []byte("new")
	src := release(t, "0.1.0", payload, goodSums(payload))
	rst := &fakeRestarter{mode: "process"}
	app := newApp(t, src, "0.0.0", &fakeBinary{}, rst)
	app.Status(context.Background())

	app.Install()
	final := waitDone(t, app)
	if final.State != StateSucceeded || final.Restart != "process" {
		t.Fatalf("final = %+v", final)
	}
	if got := rst.restarted(); len(got) != 1 || got[0] != "process" {
		t.Fatalf("restart calls = %v, want [process]", got)
	}
}

func TestInstallIsSingleFlight(t *testing.T) {
	payload := []byte("new")
	src := release(t, "0.1.0", payload, goodSums(payload))
	src.gate = make(chan struct{})
	app := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{mode: "service"})
	app.Status(context.Background())

	if _, err := app.Install(); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if _, err := app.Install(); err != ErrBusy {
		t.Fatalf("second Install error = %v, want ErrBusy", err)
	}
	close(src.gate)
	if final := waitDone(t, app); final.State != StateSucceeded {
		t.Fatalf("final = %+v", final)
	}
}

func TestInstallNeedsKnownNewerRelease(t *testing.T) {
	payload := []byte("new")
	src := release(t, "0.1.0", payload, goodSums(payload))

	fresh := newApp(t, src, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	if _, err := fresh.Install(); err != ErrNoRelease {
		t.Fatalf("Install before any check = %v, want ErrNoRelease", err)
	}

	same := newApp(t, release(t, "0.0.0", payload, goodSums(payload)), "0.0.0", &fakeBinary{}, &fakeRestarter{})
	same.Status(context.Background())
	if _, err := same.Install(); err != ErrNoUpdate {
		t.Fatalf("Install when current = %v, want ErrNoUpdate", err)
	}
}

func TestDevBuildCannotInstall(t *testing.T) {
	payload := []byte("new")
	app := newApp(t, release(t, "0.1.0", payload, goodSums(payload)), "dev", &fakeBinary{}, &fakeRestarter{})
	if _, err := app.Install(); err != ErrDisabled {
		t.Fatalf("Install on a dev build = %v, want ErrDisabled", err)
	}
}

func TestPlatformWithoutBinaryAssetCannotInstall(t *testing.T) {
	payload := []byte("new")
	src := release(t, "0.1.0", payload, goodSums(payload))
	app := New(Deps{Source: src, Binary: &fakeBinary{}, Restarter: &fakeRestarter{}, Lifetime: context.Background()},
		"0.0.0", "install", ttl)
	info := app.Status(context.Background())
	if !info.Enabled || info.Installable || !info.Available {
		t.Fatalf("platform without a binary asset: %+v", info)
	}
	if _, err := app.Install(); err != ErrDisabled {
		t.Fatalf("Install = %v, want ErrDisabled", err)
	}
}

func TestProgressIsIdleBeforeAnyInstall(t *testing.T) {
	app := newApp(t, &fakeSource{}, "0.0.0", &fakeBinary{}, &fakeRestarter{})
	if p := app.Progress(); p.State != StateIdle {
		t.Fatalf("progress before any install = %+v", p)
	}
}
