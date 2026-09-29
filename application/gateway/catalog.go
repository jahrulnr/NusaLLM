package appgateway

import "github.com/jahrulnr/nusallm/infrastructure/domain/entity"

// MinListedContextWindow is the model selection floor. Unknown windows remain visible.
const MinListedContextWindow = 126_000

// ModelCatalogContains reports whether a gateway advertises an id.
func ModelCatalogContains(models []entity.Model, id string) bool {
	for _, model := range models {
		if model.ID() == id {
			return true
		}
	}
	return false
}

// ModelListedInPicker applies the selection floor to a catalog entry.
func ModelListedInPicker(m entity.Model) bool {
	w := m.ContextWindow()
	return w == 0 || w >= MinListedContextWindow
}

// ListedModels returns selectable catalog entries in gateway order.
func ListedModels(models []entity.Model) []entity.Model {
	out := make([]entity.Model, 0, len(models))
	for _, m := range models {
		if ModelListedInPicker(m) {
			out = append(out, m)
		}
	}
	return out
}
