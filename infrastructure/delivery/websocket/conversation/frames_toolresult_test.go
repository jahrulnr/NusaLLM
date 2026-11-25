package wsconversation

// T-27 — the tool_result frame is the call's terminal verdict. Its shape is a
// contract the client deep-equals: exit is {"code":N} or {"signal":"X"},
// never both, and absent when no process ran.

import (
	"encoding/json"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

func TestToolResultFrameShape(t *testing.T) {
	cases := []struct {
		label   string
		payload toolResultPayload
		want    string
	}{
		{"clean exit", toolResultPayload{CallID: "call_1", Status: "ok", Exit: &valueobject.ExitStatus{Code: 0}}, `{"callId":"call_1","status":"ok","exit":{"code":0}}`},
		{"failed exit", toolResultPayload{CallID: "call_2", Status: "failed", Exit: &valueobject.ExitStatus{Code: 3}}, `{"callId":"call_2","status":"failed","exit":{"code":3}}`},
		{"signal death", toolResultPayload{CallID: "call_3", Status: "failed", Exit: &valueobject.ExitStatus{Signal: "killed"}}, `{"callId":"call_3","status":"failed","exit":{"signal":"killed"}}`},
		{"go error", toolResultPayload{CallID: "call_4", Status: "failed"}, `{"callId":"call_4","status":"failed"}`},
		{"canceled", toolResultPayload{CallID: "call_5", Status: "canceled"}, `{"callId":"call_5","status":"canceled"}`},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		if string(raw) != tc.want {
			t.Fatalf("%s: %s, want %s", tc.label, raw, tc.want)
		}
	}
}
