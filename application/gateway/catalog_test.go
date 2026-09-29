package appgateway

import (
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
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
