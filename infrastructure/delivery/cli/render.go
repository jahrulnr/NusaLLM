package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
)

type turnRenderer struct {
	out               io.Writer
	err               error
	toolOutputHeaders map[string]struct{}
}

func newTurnRenderer(out io.Writer) *turnRenderer {
	return &turnRenderer{
		out:               out,
		toolOutputHeaders: make(map[string]struct{}),
	}
}

func (r *turnRenderer) Handle(e event.TurnEvent) {
	if r.err != nil {
		return
	}

	switch e.Kind() {
	case enum.TurnEventDelta:
		r.write(e.Delta())
	case enum.TurnEventToolCall:
		call := e.ToolCall()
		input := call.Input()
		var prettyInput bytes.Buffer
		if err := json.Indent(&prettyInput, []byte(input), "", "  "); err == nil {
			input = prettyInput.String()
		}
		r.write("\n[tool " + call.Name() + " " + call.ID() + "]\n" + input + "\n")
	case enum.TurnEventToolOutput:
		call := e.ToolCall()
		if _, ok := r.toolOutputHeaders[call.ID()]; !ok {
			r.toolOutputHeaders[call.ID()] = struct{}{}
			r.write("\n[tool output " + call.ID() + "]\n" + e.Delta())
			return
		}
		r.write(e.Delta())
	case enum.TurnEventFinished:
		usage := e.Usage()
		r.write(fmt.Sprintf(
			"\n[usage input=%d cached_read=%d cached_write=%d output=%d total=%d]\n",
			usage.InputTokens(),
			usage.CacheReadTokens(),
			usage.CacheWriteTokens(),
			usage.OutputTokens(),
			usage.Total(),
		))
	}
}

func (r *turnRenderer) Err() error {
	return r.err
}

func (r *turnRenderer) write(s string) {
	if r.err != nil || s == "" {
		return
	}
	_, r.err = writeTerminal(r.out, s)
}
