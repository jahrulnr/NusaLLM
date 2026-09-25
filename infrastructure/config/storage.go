package config

// StorageConfig locates the on-disk state — every repository roots under
// one data dir so a deployment moves it in a single place.
type StorageConfig struct {
	Dir string `json:"dir" default:"./data"`
}

// Normalize fills unset fields from the `default` tags.
func (c *StorageConfig) Normalize() {
	ApplyDefaults(c)
}
