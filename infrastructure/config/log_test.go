package config

import "testing"

func TestLogConfigCaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   LogConfig
		want LogConfig
	}{
		{"defaults", LogConfig{}, LogConfig{MaxLines: 2000, MaxFiles: 9}},
		{"lower values", LogConfig{MaxLines: 50, MaxFiles: 3}, LogConfig{MaxLines: 50, MaxFiles: 3}},
		{"upper bounds", LogConfig{MaxLines: 5000, MaxFiles: 50}, LogConfig{MaxLines: 2000, MaxFiles: 9}},
		{"negative values", LogConfig{MaxLines: -1, MaxFiles: -1}, LogConfig{MaxLines: 2000, MaxFiles: 9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in
			got.Normalize()
			if got != tc.want {
				t.Fatalf("Normalize() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
