package config

import "testing"

func TestToolsPathsConfiguration(t *testing.T) {
	cfg, err := Validate([]byte(`{"tools":{"paths":["./commands","~/shared/bin","/opt/custom/bin"]}}`))
	if err != nil || len(cfg.Tools.Paths) != 3 {
		t.Fatalf("tools=%+v %v", cfg.Tools, err)
	}
	if NewConfig().Tools.Paths == nil {
		t.Fatal("default paths must encode as an array for settings editing")
	}
	for _, raw := range []string{`{"tools":{"paths":"/opt/custom"}}`, `{"tools":{"paths":[3]}}`, `{"tools":{"typo":[]}}`} {
		if _, err := Validate([]byte(raw)); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}
