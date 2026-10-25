package job

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
)

// appendToolContext is the discovery phase before a provider call. The
// selected driver owns discovery; the application executes its system tool
// and records that exact result, never importing a concrete tool adapter.
// Comparing the newest live record refreshes changes and restores the catalog
// after compaction without repeating unchanged metadata on every tool step.
func (d Deps) appendToolContext(ctx context.Context, convID string, conv *aggregate.Conversation, driver EnvironmentDriver, name string) error {
	tool, ok := driver.Tool(name)
	if !ok {
		return nil
	}
	if !tool.Schema().CallableBySystem() {
		return fmt.Errorf("%s hydration tool is not system-callable", name)
	}
	raw, err := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: dservice.WorkspaceDir(ctx)})
	if err != nil {
		return err
	}
	id, err := valueobject.NewID("call", "")
	if err != nil {
		return err
	}
	call, err := entity.NewToolCall(id.String(), tool.Schema().Name(), string(raw), enum.TooltypeInjection)
	if err != nil {
		return err
	}
	result, err := tool.Execute(ctx, call, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		result.Text = "error: " + err.Error()
	}
scan:
	for i := len(conv.Messages()) - 1; i >= 0; i-- {
		for _, prior := range conv.Messages()[i].ToolCalls() {
			if prior.Type() != enum.TooltypeInjection || prior.Name() != call.Name() {
				continue
			}
			if prior.Input() == call.Input() && prior.HasOutput() && prior.Output() == result.Text {
				return nil
			}
			break scan
		}
	}
	call.SetStoredOutput(result.Text, "")
	msg, err := entity.NewMessage(enum.RoleSystem, "")
	if err != nil {
		return err
	}
	msg.AddToolCall(call)
	conv.Append(msg)
	_, err = d.Convs.AppendMessage(ctx, convID, msg)
	return err
}
