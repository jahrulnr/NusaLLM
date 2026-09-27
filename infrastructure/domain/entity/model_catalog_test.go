package entity

import "testing"

func TestListedModelsFiltersSmallContext(t *testing.T) {
	big, _ := NewModel("big", "gw")
	big.SetContextWindow(200_000)
	small, _ := NewModel("small", "gw")
	small.SetContextWindow(125_999)
	edge, _ := NewModel("edge", "gw")
	edge.SetContextWindow(MinListedContextWindow)
	unknown, _ := NewModel("unknown", "gw")

	listed := ListedModels([]Model{big, small, edge, unknown})
	if len(listed) != 3 {
		t.Fatalf("listed = %d models, want big+edge+unknown", len(listed))
	}
	if listed[0].ID() != "big" || listed[1].ID() != "edge" || listed[2].ID() != "unknown" {
		t.Fatalf("listed order/ids = %+v", listed)
	}
}
