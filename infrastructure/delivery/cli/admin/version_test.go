package admin

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionPrintsBuildVersion(t *testing.T) {
	var out, errs bytes.Buffer
	if code := Version(nil, &out, &errs, "0.0.0"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs.String())
	}
	if out.String() != "nusallm 0.0.0\n" {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestVersionReportsSourceBuild(t *testing.T) {
	var out, errs bytes.Buffer
	Version(nil, &out, &errs, "dev")
	if out.String() != "nusallm dev\n" {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestVersionHelp(t *testing.T) {
	var out, errs bytes.Buffer
	if code := Version([]string{"--help"}, &out, &errs, "0.0.0"); code != 0 {
		t.Fatalf("help exit %d", code)
	}
	if !strings.Contains(out.String(), "Usage: nusallm version") {
		t.Fatalf("help text = %q", out.String())
	}
}

func TestVersionRejectsExtraArgs(t *testing.T) {
	var out, errs bytes.Buffer
	if code := Version([]string{"extra"}, &out, &errs, "0.0.0"); code != 2 {
		t.Fatalf("extra argument exit %d, want 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errs.String(), "Usage: nusallm version") {
		t.Fatalf("stdout %q stderr %q", out.String(), errs.String())
	}
}
