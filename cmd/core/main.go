// Command core is the application entrypoint: it discovers the JSON
// config via .env (the config file cannot locate itself), loads it, and
// wires every repository, service adapter, and tool into the
// conversation application.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconversation "github.com/jahrulnr/llm-base/application/conversation"
	appgateway "github.com/jahrulnr/llm-base/application/gateway"
	"github.com/jahrulnr/llm-base/infrastructure/config"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/redact"
	repoconversation "github.com/jahrulnr/llm-base/infrastructure/repository/conversation"
	repogateway "github.com/jahrulnr/llm-base/infrastructure/repository/gateway"
	repolog "github.com/jahrulnr/llm-base/infrastructure/repository/log"
	reposecret "github.com/jahrulnr/llm-base/infrastructure/repository/secret"
	textcompletion "github.com/jahrulnr/llm-base/infrastructure/service/core/text/completion"
	toolfile "github.com/jahrulnr/llm-base/infrastructure/service/tool/file"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "core:", err)
		os.Exit(1)
	}
}

func run() error {
	loadEnvFile(".env")
	cfg := config.NewConfig()
	if err := cfg.Load(os.Getenv("CONFIG_PATH")); err != nil {
		return err
	}

	dir := cfg.Storage.Dir
	secrets, err := reposecret.New(filepath.Join(dir, "secrets.db"))
	if err != nil {
		return err
	}
	defer secrets.Close()
	gateways := repogateway.NewGatewayRepository(filepath.Join(dir, "gateways"), secrets)
	convs := repoconversation.NewConversationRepository(filepath.Join(dir, "conversations"))
	logOpts := repolog.Options{MaxLines: cfg.Log.MaxLines, MaxFiles: cfg.Log.MaxFiles}
	if !cfg.Log.Bodies {
		// Payload logging is opt-in: entries keep the kind tag and any
		// non-body fields, but request/response content never lands.
		logOpts.Redact = func(_ string, e any) any {
			e = redact.Value(e) // credential masking stays regardless
			switch v := e.(type) {
			case map[string]any:
				delete(v, "body")
				return v
			case []json.RawMessage:
				return map[string]any{"chunks": len(v)}
			case []any:
				return map[string]any{"chunks": len(v)}
			default:
				return map[string]any{"omitted": true}
			}
		}
	}
	logger, err := repolog.New(filepath.Join(dir, "logs"), logOpts)
	if err != nil {
		return err
	}

	services := map[enum.Style]dservice.TextModelService{
		enum.StyleCompletion: textcompletion.New(textcompletion.Options{
			Logger:        logger,
			HeaderTimeout: time.Duration(cfg.Service.HeaderTimeout),
			IdleTimeout:   time.Duration(cfg.Service.IdleTimeout),
		}),
	}
	tools := map[string]dservice.Tool{}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	readFile, err := toolfile.NewFileReadTool(wd) // workspace root = cwd
	if err != nil {
		return err
	}
	tools[readFile.Schema().Name()] = readFile

	_ = appconversation.New(convs, gateways, services, tools, cfg)
	_ = appgateway.New(gateways, secrets)
	// Transport wiring (HTTP/console) lands here — the application is
	// fully constructed and ready to serve.
	return nil
}

// loadEnvFile reads KEY=VALUE pairs into the process env. This step is a
// bootstrap concern, not config.go's: the .env locates the config file,
// and config cannot load a file it has not found. Real env vars win — a
// key already exported is never overwritten.
func loadEnvFile(path string) {
	raw, err := os.ReadFile(path)
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
