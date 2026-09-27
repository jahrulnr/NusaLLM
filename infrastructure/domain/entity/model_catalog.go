package entity

// ModelCatalogContains reports whether id is listed on a gateway catalog.
func ModelCatalogContains(models []Model, id string) bool {
	for _, model := range models {
		if model.ID() == id {
			return true
		}
	}
	return false
}
