package entity

// MinListedContextWindow hides catalog entries below this total context
// from model pickers — unknown (zero) windows stay visible.
const MinListedContextWindow = 126_000

// ModelCatalogContains reports whether id is listed on a gateway catalog.
func ModelCatalogContains(models []Model, id string) bool {
	for _, model := range models {
		if model.ID() == id {
			return true
		}
	}
	return false
}

// ModelListedInPicker reports whether a catalog entry appears in /model
// listings and completions.
func ModelListedInPicker(m Model) bool {
	w := m.ContextWindow()
	return w == 0 || w >= MinListedContextWindow
}

// ListedModels returns picker-visible catalog entries in gateway order.
func ListedModels(models []Model) []Model {
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if ModelListedInPicker(m) {
			out = append(out, m)
		}
	}
	return out
}
