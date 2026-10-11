// Package wsconversation is the WebUI's conversation turn boundary over
// WebSocket — the hub, sockets, frame protocol, and event projection for
// docs/api/websocket.md. It serves `turn.send`/`turn.edit` plus
// `turn.cancel`/`turn.watch` turn control.
//
// Secrets never cross: stream frames carry deltas, phase markers, raw
// and summarized reasoning chunks, tool call metadata + input/output
// chunks, and sanitized usage — never key material, optionalContent, or
// error causes. Provider text is untrusted display text, not markup.
package wsconversation

import (
	"bytes"
	"encoding/json"
	"io"
)

// command is the client→server envelope. Every field is declared so the
// strict decoder can reject extras; per-type handlers validate which
// fields are meaningful for their command.
type command struct {
	Type           string          `json:"type"`
	RequestID      string          `json:"requestId"`
	ConversationID *string         `json:"conversationId"`
	TurnID         string          `json:"turnId"`
	AfterSeq       *int            `json:"afterSeq"`
	Payload        json.RawMessage `json:"payload"`
}

// decodeCommand parses one JSON text frame — a single document (the
// second decode must hit io.EOF; trailing garbage or extra documents
// are rejected), no unknown fields.
func decodeCommand(raw []byte) (command, bool) {
	var cmd command
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cmd); err != nil {
		return command{}, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return command{}, false
	}
	return cmd, true
}

func decodeStrict(raw []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return false
	}
	var extra any
	return dec.Decode(&extra) == io.EOF
}

// sendPayload carries the picker's per-turn selections beside the text:
// effort is an enum word, provider an aggregator upstream name (empty =
// the gateway routes), and environment/workspace bind the room's execution
// target. The selected driver validates the workspace path when the job runs.
type sendPayload struct {
	Route       string `json:"route"`
	ModelID     string `json:"modelId"`
	Content     string `json:"content"`
	Effort      string `json:"effort,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	Environment string `json:"environment,omitempty"`
}

type editPayload struct {
	MessageID string `json:"messageId"`
	Content   string `json:"content"`
}

// serverFrame is the server→client envelope. requestId rides command
// replies and the turn's opened/terminal frames — stream frames
// attribute by turnId + seq and carry no requestId.
type serverFrame struct {
	Type           string `json:"type"`
	RequestID      string `json:"requestId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	TurnID         string `json:"turnId,omitempty"`
	Seq            *int   `json:"seq,omitempty"`
	Payload        any    `json:"payload,omitempty"`
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
}

// errorFrame is the command rejection that belongs to no turn — the
// requestId echoes when it could be parsed.
func errorFrame(requestID, code, message string) serverFrame {
	return serverFrame{Type: "error", RequestID: requestID, Code: code, Message: message}
}

// cancelAckFrame confirms a turn.cancel was accepted — it answers the
// cancel command (its requestId), not the turn's stream.
func cancelAckFrame(requestID, convID, turnID string) serverFrame {
	return serverFrame{
		Type:           "turn.cancel_ack",
		RequestID:      requestID,
		ConversationID: convID,
		TurnID:         turnID,
	}
}

// resyncPayload is the turn.resync_required body. Turn is "active"
// while the turn still runs, the terminal verdict "completed"/"failed"
// for a retained finished turn (with the stored terminal body replayed
// under terminal), and "unknown" for an evicted or never-seen id — the
// client reconciles submitted steering against the snapshot and against
// history, and never auto-resends an unknown outcome.
type resyncPayload struct {
	Turn     string              `json:"turn"`
	Steering []steerStatePayload `json:"steering,omitempty"`
	Terminal *terminalPayload    `json:"terminal,omitempty"`
}

// resyncFrame answers a turn.watch that can't be satisfied from the
// bounded journal — the client re-reads persisted state over HTTP.
func resyncFrame(requestID, convID, turnID string, payload resyncPayload) serverFrame {
	return serverFrame{
		Type:           "turn.resync_required",
		RequestID:      requestID,
		ConversationID: convID,
		TurnID:         turnID,
		Payload:        payload,
	}
}

// activityPayload is the conversation.activity body — the room-level
// notice that a transport turn opened or reached its terminal. state
// mirrors the edge ("opened", or the terminal verdict "completed"/
// "failed"); kind is the command that opened the turn; requestId is
// the opening command's own correlation id so the issuing tab
// recognizes its notice — opaque to every other tab.
type activityPayload struct {
	State     string `json:"state"`
	Kind      string `json:"kind"`
	RequestID string `json:"requestId"`
}

// activityFrame projects a turn's open/terminal edge into the
// room-level notice broadcast to every socket. It is unsequenced and
// unjournaled — room state, not turn stream data — so a turn.watch
// replay must never reproduce it. The frame carries no top-level
// requestId: it answers no command.
func activityFrame(t *turn, state string) serverFrame {
	return serverFrame{
		Type:           "conversation.activity",
		ConversationID: t.conversationID,
		TurnID:         t.id,
		Payload: activityPayload{
			State:     state,
			Kind:      t.kind.String(),
			RequestID: t.requestID,
		},
	}
}

type phasePayload struct {
	Phase string `json:"phase"`
}

// reasoningPayload is one bounded chunk of provider reasoning text.
type reasoningPayload struct {
	Text string `json:"text"`
}

type toolCallPayload struct {
	CallID string `json:"callId"`
	Name   string `json:"name"`
}

type toolInputPayload struct {
	CallID string `json:"callId"`
	Text   string `json:"text"`
}

type toolOutputPayload struct {
	CallID string `json:"callId"`
	Text   string `json:"text"`
}

// steerPayload is one bounded chunk of a user steering line folded into
// the running turn — the text landed in history as an ordinary user
// message; the frame carries no message id because ids persist later.
// InputId correlates the fold back to the turn.steer requestId that
// submitted it (absent for receipt-free input, e.g. the CLI); Final
// marks the last chunk of one input so identical consecutive inputs
// never concatenate into one render segment.
type steerPayload struct {
	Text    string `json:"text"`
	InputID string `json:"inputId,omitempty"`
	Final   bool   `json:"final"`
}

// steerCommandPayload is the turn.steer request body.
type steerCommandPayload struct {
	Content string `json:"content"`
}

// steerRecallCommandPayload is the turn.steer_recall request body — the
// inputId is the requestId that originally submitted the input.
type steerRecallCommandPayload struct {
	InputID string `json:"inputId"`
}

// steerInputPayload is the journal payload for turn.steer_ack and
// turn.steer_recalled — the inputId is the turn.steer requestId.
type steerInputPayload struct {
	InputID string `json:"inputId"`
}

// steerStatePayload is one submitted input's receipt state — the bounded
// snapshot carried by turn.completed, turn.failed, and
// turn.resync_required. Content never re-crosses: the browser owns it.
type steerStatePayload struct {
	InputID string `json:"inputId"`
	State   string `json:"state"`
}

type stepFinishedPayload struct {
	FinishReason string `json:"finishReason"`
	Usage        any    `json:"usage"`
}

// terminalPayload is the completed/failed body — ResyncRequired is
// always true because live frames never carry persisted ids.
// UserMessagePersisted is a *bool: send terminals always carry it, edit
// terminals omit it (a rewrite never appends a user line). Steering is
// the bounded terminal receipt snapshot for send turns — every input
// submitted, with its state: persisted, claimed (drained but
// unconsumed), pending (never drained), or recalled.
type terminalPayload struct {
	Status               int                 `json:"status,omitempty"`
	Code                 string              `json:"code,omitempty"`
	Message              string              `json:"message,omitempty"`
	UserMessagePersisted *bool               `json:"userMessagePersisted,omitempty"`
	Steering             []steerStatePayload `json:"steering,omitempty"`
	ResyncRequired       bool                `json:"resyncRequired"`
}

// marshalFrame serializes a frame and enforces the outbound size cap —
// a single frame can never exceed maxFrameBytes regardless of how large
// provider-originated strings grow. ok=false means oversized/unencodable.
func marshalFrame(f serverFrame) ([]byte, bool) {
	raw, err := json.Marshal(f)
	if err != nil || len(raw) > maxFrameBytes {
		return nil, false
	}
	return raw, true
}
