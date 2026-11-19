package coretext

import (
	"encoding/json"
	"strings"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
	"github.com/jahrulnr/nusallm/infrastructure/service/wire"
)

// EmptyAnswerError enforces the reply contract every wire's settled
// commit shares: a finished provider call must leave something
// deliverable — non-blank answer text, origin calls, attachments, or
// non-reasoning provider artifacts. Reasoning/thinking payloads are
// replay metadata and display chrome, never the answer. The gate only applies to
// settled commits — a partial (canceled) commit keeps whatever arrived.
// A suppressed answer is a definitive 400 — retrying the identical
// request cannot unblock it; a bare 200 carrying nothing is an upstream
// malfunction — 502, transient, so the turn's retry gate still applies.
// blocked carries a prompt-side suppression word where the wire has one
// (Gemini promptFeedback); finish-side filtering is read from the
// decoded verdict. Wire evidence marshals lazily — it only costs when
// the send already failed.
func EmptyAnswerError(msg entity.Message, artifacts bool, blocked string, logged []json.RawMessage) error {
	if msg.FinishReason() == "" {
		return nil // partial commit — not a settled reply
	}
	if strings.TrimSpace(msg.Content()) != "" || len(msg.ToolCalls()) > 0 ||
		len(msg.Attachments()) > 0 || artifacts {
		return nil
	}
	raw, _ := json.Marshal(logged)
	if blocked != "" {
		return apperror.New(400, "prompt blocked: "+blocked, wire.UpstreamError(raw))
	}
	if msg.FinishReason() == enum.FinishReasonContentFilter {
		return apperror.New(400, "response blocked by content filter", wire.UpstreamError(raw))
	}
	return apperror.New(502, "upstream returned an empty answer", wire.UpstreamError(raw))
}
