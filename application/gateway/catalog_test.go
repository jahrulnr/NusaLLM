package appgateway

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

func TestListedModelsFiltersSmallContext(t *testing.T) {
	big, _ := entity.NewModel("big", "gw")
	big.SetContextWindow(200_000)
	small, _ := entity.NewModel("small", "gw")
	small.SetContextWindow(125_999)
	edge, _ := entity.NewModel("edge", "gw")
	edge.SetContextWindow(MinListedContextWindow)
	unknown, _ := entity.NewModel("unknown", "gw")
	listed := ListedModels([]entity.Model{big, small, edge, unknown})
	if len(listed) != 3 || listed[0].ID() != "big" || listed[1].ID() != "edge" || listed[2].ID() != "unknown" {
		t.Fatalf("listed order/ids = %+v", listed)
	}
}

// A chat turn cannot run on a single-purpose surface — embedding,
// rerank, speech, and transcription models can only produce a provider
// error. Chat-capable and undeclared types stay eligible.
func TestListedModelsFiltersNonChatTypes(t *testing.T) {
	typed := func(id string, typ enum.ModelType) entity.Model {
		m, _ := entity.NewModel(id, "gw")
		m.SetModelType(typ)
		return m
	}
	chat := typed("chat", enum.ModelTypeText)
	audio := typed("audio", enum.ModelTypeAudio)
	embed := typed("embed", enum.ModelTypeEmbedding)
	rerank := typed("rerank", enum.ModelTypeRerank)
	unset, _ := entity.NewModel("unset", "gw")
	listed := ListedModels([]entity.Model{chat, audio, embed, rerank, unset})
	if len(listed) != 3 || listed[0].ID() != "chat" || listed[1].ID() != "audio" || listed[2].ID() != "unset" {
		t.Fatalf("listed order/ids = %+v", listed)
	}
}
