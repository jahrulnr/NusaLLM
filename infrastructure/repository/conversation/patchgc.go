package repoconversation

import (
	"encoding/json"
	"fmt"
)

// toolCallIDsFromMessageLines collects tool-call ids from message jsonl lines.
func toolCallIDsFromMessageLines(msgLines [][]byte) (map[string]struct{}, error) {
	ids := make(map[string]struct{})
	for _, l := range msgLines {
		var lr lineRecord
		if err := json.Unmarshal(l, &lr); err != nil {
			return nil, fmt.Errorf("corrupt line: %w", err)
		}
		if lr.Type != lineMessage {
			continue
		}
		for _, tc := range lr.Data.ToolCalls {
			ids[tc.ID] = struct{}{}
		}
	}
	return ids, nil
}

// dropDeadToolOutputPatches removes tool_output lines whose callId is not
// present on a retained message line. Message lines and live patches keep
// their relative order.
func dropDeadToolOutputPatches(lines [][]byte, live map[string]struct{}) ([][]byte, error) {
	out := make([][]byte, 0, len(lines))
	for _, l := range lines {
		var lr lineRecord
		if err := json.Unmarshal(l, &lr); err != nil {
			return nil, fmt.Errorf("corrupt line: %w", err)
		}
		if lr.Type == lineToolOutput {
			if _, ok := live[lr.CallID]; !ok {
				continue
			}
		}
		out = append(out, l)
	}
	return out, nil
}
