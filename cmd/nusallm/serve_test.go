package main

import "testing"

func TestServeHelpRequested(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		if !serveHelpRequested(args) {
			t.Fatalf("serveHelpRequested(%v) = false, want true", args)
		}
	}
	for _, args := range [][]string{nil, {"--port", "1"}, {"--port", "help"}, {"--port=help"}, {"-port", "-h"}} {
		if serveHelpRequested(args) {
			t.Fatalf("serveHelpRequested(%v) = true, want false", args)
		}
	}
}

func TestServePort(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    int
		wantErr bool
	}{
		{"default", nil, 10998, false},
		{"flag", []string{"--port", "9090"}, 9090, false},
		{"flag equals", []string{"--port=9090"}, 9090, false},
		{"edge low", []string{"--port", "1"}, 1, false},
		{"edge high", []string{"--port", "65535"}, 65535, false},
		{"zero", []string{"--port", "0"}, 0, true},
		{"negative", []string{"--port", "-1"}, 0, true},
		{"too high", []string{"--port", "65536"}, 0, true},
		{"not a number", []string{"--port", "abc"}, 0, true},
		{"help as port value", []string{"--port", "help"}, 0, true},
		{"help as port equals", []string{"--port=help"}, 0, true},
		{"positional", []string{"extra"}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := servePort(tc.args, 10998)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got port %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("want %d, got %d", tc.want, got)
			}
		})
	}
}
