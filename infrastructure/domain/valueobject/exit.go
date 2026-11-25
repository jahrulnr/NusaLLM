package valueobject

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ExitStatus is how the process behind a tool call ended. It is a value
// object because two layers hold it — the adapter that ran the process
// (on ToolResult) and the call record that stores it — and one type keeps
// them from drifting into parallel structs.
//
// The wire form is one answer, not a bag: {"code":N} for a normal exit
// (0 included) or {"signal":"X"} for a signal death, never both. A reader
// that renders a footer asks exactly one question, and a signal death has no
// exit code to report — the old "-1" was a Go artifact, not a status.
type ExitStatus struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}

// NewExitStatus builds the status of a finished process: a signal name when
// one reaped it, else the exit code. A signal death drops the code — the -1 a
// Go process reports is an artifact of wait(), not a status to show anyone.
func NewExitStatus(code int, signal string) ExitStatus {
	if signal != "" {
		code = 0
	}
	return ExitStatus{Code: code, Signal: signal}
}

// Failed reports whether the process ended badly: a nonzero exit or a signal.
func (e ExitStatus) Failed() bool { return e.Code != 0 || e.Signal != "" }

// MarshalJSON emits exactly one of the two shapes.
func (e ExitStatus) MarshalJSON() ([]byte, error) {
	if e.Signal != "" {
		return json.Marshal(struct {
			Signal string `json:"signal"`
		}{Signal: e.Signal})
	}
	return json.Marshal(struct {
		Code int `json:"code"`
	}{Code: e.Code})
}

// UnmarshalJSON accepts the same two shapes and rejects anything else — a
// record carrying both, or neither, is not a status this version wrote.
func (e *ExitStatus) UnmarshalJSON(data []byte) error {
	var raw struct {
		Code   *int    `json:"code"`
		Signal *string `json:"signal"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	switch {
	case raw.Signal != nil && raw.Code != nil:
		return fmt.Errorf("exit status carries both code and signal")
	case raw.Signal != nil:
		*e = ExitStatus{Signal: *raw.Signal}
		return nil
	case raw.Code != nil:
		*e = ExitStatus{Code: *raw.Code}
		return nil
	}
	return fmt.Errorf("exit status carries neither code nor signal")
}
