package appconversation

import (
	"context"
	"fmt"
	"strings"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

// Steer injects user input into the conversation's running turn. The
// text lands at the next inference safe point — after tool results
// persist, before the following provider call — as an ordinary user
// message in history. A turn that ends without reaching a safe point
// never consumes the input; the caller decides what it becomes (the TUI
// queues it as a new message so the line is never dropped).
//
// 404 — no running turn, or the running job does not take input.
func (app *ConversationApp) Steer(ctx context.Context, convID string, message string) error {
	if strings.TrimSpace(message) == "" {
		return apperror.New(400, "empty steer", fmt.Errorf("steer has no text"))
	}
	app.mu.Lock()
	w := app.workers[convID]
	app.mu.Unlock()
	if w == nil || !w.Steer(message) {
		return apperror.NewNotFoundError("in-flight turn "+convID, fmt.Errorf("no steerable turn running"))
	}
	return nil
}

// RecallSteer withdraws the newest unconsumed steering input so the
// caller can hand the text back — the composer restore path on Esc.
// ok=false — nothing pending, already folded into history, or no
// running turn.
func (app *ConversationApp) RecallSteer(convID string) (string, bool) {
	app.mu.Lock()
	w := app.workers[convID]
	app.mu.Unlock()
	if w == nil {
		return "", false
	}
	return w.RecallSteer()
}
