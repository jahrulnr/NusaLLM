// Package admin is the argv boundary for operator commands — gateway,
// config, onboard, and install are the "handlers" main.go dispatches to,
// the way an http server's mux routes to delivery handlers. Each handler
// returns the process exit code; cmd/ owns no admin logic of its own.
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
	cliterm "github.com/jahrulnr/nusallm/infrastructure/delivery/cli/term"
	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
	repoconversation "github.com/jahrulnr/nusallm/infrastructure/repository/conversation"
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

// runningFromInstall reports whether this process is the copy placed at
// ~/.local/bin by `nusallm install` / `make install`. Tests replace it.
var runningFromInstall = func() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	bin, err := userBinPath()
	if err != nil {
		return false
	}
	exeInfo, err := os.Stat(exe)
	if err != nil {
		return false
	}
	binInfo, err := os.Stat(bin)
	if err != nil {
		return false
	}
	return os.SameFile(exeInfo, binInfo)
}

// resolveConfigPath locates the JSON config. A CONFIG_PATH already in the
// environment wins; a .env in the working directory can set it for a
// source build, and an exported variable is never overwritten. The
// installed binary defaults to the user config path even when that file
// does not exist yet and even when .env names a different file. With
// neither, an existing user config is still used; otherwise the process
// runs on tag defaults and storage stays ./data. Nothing is created here.
func resolveConfigPath() string {
	if os.Getenv("CONFIG_PATH") == "" && runningFromInstall() {
		if path, err := userConfigPath(); err == nil {
			// Pin before .env loads so a checkout CONFIG_PATH cannot
			// redirect the installed binary at its own store.
			_ = os.Setenv("CONFIG_PATH", path)
		}
	}
	loadEnvFile(".env")
	if path := os.Getenv("CONFIG_PATH"); path != "" {
		return path
	}
	path, err := userConfigPath()
	if err != nil {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return ""
	}
	return path
}

// EnsureConfig generates the config file when missing — a write command
// or the REPL, never a read. The user config path gets an absolute
// storage.dir; any other path is the tag-default document.
func EnsureConfig(path string, stderr io.Writer) error {
	var created bool
	var err error
	if userPath, pathErr := userConfigPath(); pathErr == nil && path == userPath {
		_, created, err = ensureInstalledConfig()
	} else {
		created, err = setup.EnsureConfig(repoconfig.NewLocal(), path)
	}
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

// conversationStore wires the conversation repository for the command
// that works on it alone. Repair is offline recovery — it deliberately
// skips LoadBoot so a wedged or missing secret store can never block it,
// and it must not create the vault as a side effect. The storage layout
// ("conversations" under storage.dir) stays here with the rest of the
// admin wiring, out of the handler.
func conversationStore(cfg config.Config) drepo.ConversationRepairer {
	return repoconversation.NewConversationRepository(filepath.Join(cfg.Storage.Dir, "conversations"))
}

// loadEnvFile reads KEY=VALUE pairs into the process env. This step is a
// bootstrap concern, not config.go's: the .env locates the config file,
// and config cannot load a file it has not found. Real env vars win — a
// key already exported is never overwritten.
//
// The file rides whatever directory the binary launches in — including a
// cloned repo a user opens nusallm inside — so keys that steer child
// processes, path resolution, or the provider calls that carry API keys
// are refused: an unset LD_* would load into every tool subprocess, a
// BASH_ENV into every non-interactive shell, XDG_* would redirect config
// and data lookup, and a proxy or CA override would put an attacker on
// the wire the gateway secrets travel.
var dotenvDeny = map[string]bool{
	"BASH_ENV": true, "ENV": true, "SHELLOPTS": true, "BASHOPTS": true,
	"PROMPT_COMMAND": true, "PS4": true, "IFS": true, "CDPATH": true, "GLOBIGNORE": true,
	"NODE_OPTIONS": true, "NODE_PATH": true, "PYTHONSTARTUP": true, "PYTHONINSPECT": true,
	"PYTHONPATH": true, "PERL5OPT": true, "PERL5DB": true, "PERL5LIB": true,
	"RUBYOPT": true, "RUBYLIB": true, "GEM_HOME": true,
	"JAVA_TOOL_OPTIONS": true, "_JAVA_OPTIONS": true, "JDK_JAVA_OPTIONS": true,
	"GIT_SSH_COMMAND": true, "SSH_ASKPASS": true, "LESSOPEN": true, "LESSCLOSE": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true,
	"XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true,
	"PATH": true, "HOME": true, "TMPDIR": true,
	"EDITOR": true, "VISUAL": true, "PAGER": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
	"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "CURL_CA_BUNDLE": true,
	"REQUESTS_CA_BUNDLE": true, "NODE_EXTRA_CA_CERTS": true,
}

// dotenvDenied reports keys refused from .env — the explicit list plus
// the dynamic-loader prefixes, which grow per platform faster than an
// enumeration stays honest.
func dotenvDenied(k string) bool {
	return dotenvDeny[k] || strings.HasPrefix(k, "LD_") || strings.HasPrefix(k, "DYLD_")
}

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
		if k == "" || dotenvDenied(k) || os.Getenv(k) != "" {
			continue
		}
		_ = os.Setenv(k, v)
	}
}

// printErr renders a use-case error for an operator — the public message
// plus the wrapped cause, so "invalid gateway" also says *why*. The cause
// can carry provider-controlled bytes (an upstream response body), so it
// goes through term.Sanitize — an error reply must not inject escape
// sequences into the operator's terminal.
func printErr(w io.Writer, err error) {
	if appErr, ok := errors.AsType[apperror.IAppError](err); ok {
		fmt.Fprintln(w, "nusallm:", cliterm.Sanitize(appErr.Caused()))
		return
	}
	fmt.Fprintln(w, "nusallm:", cliterm.Sanitize(err.Error()))
}

func isTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
