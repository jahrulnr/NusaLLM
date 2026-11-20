package appgateway

import (
	"strings"
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

// The picker offers only models that take and give text. Every declared
// type other than text is another surface — including image generators
// and realtime models that also emit text — and a declared modality list
// without text excludes the model. Undeclared metadata stays eligible.
func TestListedModelsRequiresTextInAndOut(t *testing.T) {
	model := func(id string, typ enum.ModelType, in, out []enum.Modality) entity.Model {
		m, _ := entity.NewModel(id, "gw")
		m.SetModelType(typ)
		m.SetInputModalities(in)
		m.SetOutputModalities(out)
		return m
	}
	text := []enum.Modality{enum.ModalityText}
	cases := []entity.Model{
		model("chat", enum.ModelTypeText, nil, nil),
		model("chat-vision", enum.ModelTypeText, []enum.Modality{enum.ModalityText, enum.ModalityImage}, text),
		model("unset", "", nil, nil),
		model("unset-text", "", text, text),
		model("image-gen", enum.ModelTypeImage, text, []enum.Modality{enum.ModalityText, enum.ModalityImage}),
		model("audio", enum.ModelTypeAudio, nil, nil),
		model("realtime", enum.ModelTypeRealtime, text, text),
		model("decision", enum.ModelTypeDecision, text, nil),
		model("embed", enum.ModelTypeEmbedding, nil, nil),
		model("rerank", enum.ModelTypeRerank, nil, nil),
		model("image-only-out", "", text, []enum.Modality{enum.ModalityImage}),
		model("no-text-in", enum.ModelTypeText, []enum.Modality{enum.ModalityAudio}, text),
	}
	var ids []string
	for _, m := range ListedModels(cases) {
		ids = append(ids, m.ID())
	}
	want := "chat chat-vision unset unset-text"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("listed = %q, want %q", got, want)
	}
}
