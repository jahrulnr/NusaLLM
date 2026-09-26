package config

type App struct {
	Name    string `json:"-" default:"NusaLLM"`
	Source  string `json:"-" default:"https://github.com/jahrulnr/NusaLLM"`
	Version string `json:"-" default:"0.0.0"`
}
