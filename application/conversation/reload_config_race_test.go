package appconversation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	appjob "github.com/jahrulnr/nusallm/application/job"
	"github.com/jahrulnr/nusallm/infrastructure/config"
	repoconfig "github.com/jahrulnr/nusallm/infrastructure/repository/config"
)

func writeConversationConfig(t *testing.T, path string, mailboxCap int) {
	t.Helper()
	body := fmt.Sprintf(`{"conversation":{"mailbox_cap":%d,"max_turn_steps":512}}`, mailboxCap)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureWithConfig(t *testing.T, configPath string) *ConversationApp {
	t.Helper()
	app, _ := fixture(t, okHandler)
	app.configPath = configPath
	app.configStore = repoconfig.NewLocal()
	return app
}

// Concurrent posts reload from disk while other goroutines read deps through
// Send and query entry points — must stay clean under -race.
func TestReloadConfigConcurrentWithSend(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeConversationConfig(t, cfgPath, 128)

	app := fixtureWithConfig(t, cfgPath)

	const workers = 6
	const rounds = 40
	var capSeq atomic.Int64
	capSeq.Store(128)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			select {
			case <-stop:
				return
			default:
			}
			next := int(capSeq.Add(1))
			if next > 256 {
				next = 64
				capSeq.Store(int64(next))
			}
			writeConversationConfig(t, cfgPath, next)
		}
	}()

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = app.Send(t.Context(), "race-room", "gw1", "m1", "ping", nil, appjob.SendOptions{}, nil)
				_, _ = app.CanCompact(t.Context(), "race-room")
				_, _ = app.ToolSchemas(context.Background(), "")
			}
		}()
	}

	wg.Wait()
	close(stop)

	// app.cfg reflects the reload of the last post — that may lag the
	// writer's final write. One more post forces a reload after all
	// writes, so the compare below is against the file's final state.
	if _, err := app.Send(t.Context(), "race-room", "gw1", "m1", "final", nil, appjob.SendOptions{}, nil); err != nil {
		t.Fatal(err)
	}

	var c config.Config
	if err := c.Load(repoconfig.NewLocal(), cfgPath); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	got := app.cfg.MailboxCap
	app.mu.Unlock()
	if got != c.Conversation.MailboxCap {
		t.Fatalf("app cfg out of sync with file: app=%d file=%d", got, c.Conversation.MailboxCap)
	}
}
