// Package admin is the argv boundary for operator commands — gateway,
// config, and onboard are the "handlers" main.go dispatches to, the way
// an http server's mux routes to delivery handlers. Each handler returns
// the process exit code; cmd/ owns no admin logic of its own.
package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	appgateway "github.com/jahrulnr/nusallm/application/gateway"
	"github.com/jahrulnr/nusallm/application/setting"
	"github.com/jahrulnr/nusallm/application/setup"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
	repogateway "github.com/jahrulnr/nusallm/infrastructure/repository/gateway"
	reposecret "github.com/jahrulnr/nusallm/infrastructure/repository/secret"
	"github.com/jahrulnr/nusallm/infrastructure/service/core/text/catalog"
	"golang.org/x/term"
)

// Boot resolves the config path, loads it, and opens the secret store +
// gateway application — the slice of wiring the admin subcommands and
// the REPL share. The conversation stack (tools, model services,
// workers) stays with the REPL.
type Boot struct {
	cfg        config.Config
	configPath string
	secrets    *reposecret.Store
	gateways   *appgateway.GatewayApp
	settings   *setting.SettingApp
}

func (b *Boot) Config() config.Config            { return b.cfg }
func (b *Boot) ConfigPath() string               { return b.configPath }
func (b *Boot) Secrets() *reposecret.Store       { return b.secrets }
func (b *Boot) Gateways() *appgateway.GatewayApp { return b.gateways }

// Close releases the secret store held by the boot wiring.
func (b *Boot) Close() error {
	return b.secrets.Close()
}

// LoadConfig resolves the config path and loads the file. A missing file
// is NOT created here — read commands must not mutate the filesystem —
// the process falls back to defaults. With strict=false (store-backed admin
// commands), invalid fields outside storage.dir use defaults while storage.dir
// must still resolve unambiguously. File-only config commands resolve the
// path separately so validate/unset can repair even a broken storage.dir.
// With strict=true (the REPL) an invalid file fails loudly.
func LoadConfig(strict bool) (config.Config, string, error) {
	configPath := resolveConfigPath()
	cfg := config.NewConfig()
	if configPath == "" {
		return cfg, configPath, nil
	}
	raw, err := repoconfig.NewLocal().Read(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, configPath, nil
	}
	if err != nil {
		return config.Config{}, "", apperror.NewInternalError("config load failed", err)
	}
	fresh, validationErr := config.Validate(raw)
	if validationErr == nil {
		return fresh, configPath, nil
	}
	if strict {
		return config.Config{}, "", validationErr
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return config.Config{}, "", validationErr
	}
	// Store-backed admin commands need only storage.dir from an invalid
	// document. Decode it independently: a custom decoder for another field
	// must not stop before the store path, and no partially decoded values
	// should leak into the effective admin config.
	var location struct {
		Storage config.StorageConfig `json:"storage"`
	}
	if err := json.Unmarshal(raw, &location); err != nil {
		return config.Config{}, "", apperror.NewInternalError("config load failed", err)
	}
	if location.Storage.Dir != "" {
		cfg.Storage.Dir = location.Storage.Dir
	}
	return cfg, configPath, nil
}

func resolveConfigPath() string {
	loadEnvFile(".env")
	return os.Getenv("CONFIG_PATH")
}

// EnsureConfig generates the config file when missing — a write command
// or the REPL, never a read.
func EnsureConfig(path string, stderr io.Writer) error {
	created, err := setup.EnsureConfig(repoconfig.NewLocal(), path)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(stderr, "nusallm: no config found; wrote defaults to %s\n", path)
	}
	return nil
}

// LoadBoot wires the admin-command slice. It must not create the config
// file — `gateway list` or `config validate` on a fresh box is a read,
// not a provisioning step; the REPL creates it via EnsureConfig.
func LoadBoot(strict bool) (*Boot, error) {
	cfg, configPath, err := LoadConfig(strict)
	if err != nil {
		return nil, err
	}
	secrets, err := reposecret.New(filepath.Join(cfg.Storage.Dir, "secrets.db"))
	if err != nil {
		return nil, err
	}
	return &Boot{
		cfg:        cfg,
		configPath: configPath,
		secrets:    secrets,
		gateways: appgateway.New(
			repogateway.NewGatewayRepository(filepath.Join(cfg.Storage.Dir, "gateways"), secrets),
			secrets, catalog.New(nil)),
		settings: setting.New(repoconfig.NewLocal(), configPath),
	}, nil
}

// loadEnvFile reads KEY=VALUE pairs into the process env. This step is a
// bootstrap concern, not config.go's: the .env locates the config file,
// and config cannot load a file it has not found. Real env vars win — a
// key already exported is never overwritten.
func loadEnvFile(path string) {
	raw, err := repoconfig.NewLocal().Read(path)
	if err != nil {
		return // a missing .env means "no overrides", not a failure
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		_ = os.Setenv(k, v)
	}
}

// printErr renders a use-case error for an operator — the public message
// plus the wrapped cause, so "invalid gateway" also says *why*.
func printErr(w io.Writer, err error) {
	if appErr, ok := errors.AsType[apperror.IAppError](err); ok {
		fmt.Fprintln(w, "nusallm:", appErr.Caused())
		return
	}
	fmt.Fprintln(w, "nusallm:", err)
}

func isTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
