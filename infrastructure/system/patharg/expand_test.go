package patharg

import (
	"strings"
	"testing"
)

// One table covers both platforms: expansion depends on (GOOS, Sep, Home,
// Env), never on the runner, so the Windows %VAR% rules stay testable on
// Linux.
func TestExpandWith(t *testing.T) {
	linux := Host{GOOS: "linux", Sep: "/", Home: "/home/owner"}
	windows := Host{GOOS: "windows", Sep: `\`, Home: `C:\Users\Owner`, Env: map[string]string{
		"APPDATA":      `C:\Users\Owner\AppData\Roaming`,
		"USERPROFILE":  `C:\Users\Owner`,
		"LOCALAPPDATA": `C:\Users\Owner\AppData\Local`,
	}}
	homeless := Host{GOOS: "linux", Sep: "/"}

	cases := []struct {
		name    string
		host    Host
		in      string
		want    string
		wantErr string // substring
	}{
		{"empty", linux, "", "", ""},

		{"home alone", linux, "~", "/home/owner", ""},
		{"home slash", linux, "~/notes/a.md", "/home/owner/notes/a.md", ""},
		{"home slash only", linux, "~/", "/home/owner", ""},
		{"home backslash", linux, `~\notes\a.md`, `/home/owner/notes\a.md`, ""},
		{"home repeated slash", linux, "~//a", "/home/owner/a", ""},
		{"home unknown", homeless, "~/a", "", "home directory is not known"},
		{"tilde user is a name", linux, "~owner/a.md", "~owner/a.md", ""},
		{"tilde mid path is a name", linux, "a/~/b.md", "a/~/b.md", ""},
		{"tilde name", linux, "~notes.md", "~notes.md", ""},

		{"relative untouched", linux, "sub/a.md", "sub/a.md", ""},
		{"dot untouched", linux, "./a.md", "./a.md", ""},
		{"parent untouched", linux, "../a.md", "../a.md", ""},
		{"absolute untouched", linux, "/tmp/a.md", "/tmp/a.md", ""},
		{"dollar literal", linux, "$HOME/a.md", "$HOME/a.md", ""},
		{"braced dollar literal", linux, "${HOME}/a.md", "${HOME}/a.md", ""},
		{"percent literal on linux", linux, `%APPDATA%\a.md`, `%APPDATA%\a.md`, ""},
		{"backslash name on linux", linux, `a\b.md`, `a\b.md`, ""},

		{"file url", linux, "file:///tmp/a.md", "/tmp/a.md", ""},
		{"file url encoded", linux, "file:///tmp/a%20b.md", "/tmp/a b.md", ""},
		{"file url localhost", linux, "file://localhost/tmp/a.md", "/tmp/a.md", ""},
		{"file url remote host", linux, "file://server/share/a.md", "", "not a local path"},
		{"file url no path", linux, "file://localhost", "", "names no path"},
		{"file url empty", linux, "file:", "", "names no path"},
		{"file url keeps percent it cannot decode", linux, "file:///tmp/50%off.md", "/tmp/50%off.md", ""},
		{"file url relative form", linux, "file:notes/a.md", "notes/a.md", ""},
		{"file url drive is a linux path", linux, "file:///C:/tmp/a.md", "/C:/tmp/a.md", ""},

		{"windows home", windows, `~\docs\a.md`, `C:\Users\Owner\docs\a.md`, ""},
		{"windows home slash", windows, "~/docs/a.md", `C:\Users\Owner\docs/a.md`, ""},
		{"windows drive url", windows, "file:///C:/Users/Owner/a.md", `C:/Users/Owner/a.md`, ""},
		{"windows appdata", windows, `%APPDATA%\app\a.md`, `C:\Users\Owner\AppData\Roaming\app\a.md`, ""},
		{"windows case insensitive", windows, `%appdata%\a.md`, `C:\Users\Owner\AppData\Roaming\a.md`, ""},
		{"windows userprofile", windows, `%USERPROFILE%\.memory\a.md`, `C:\Users\Owner\.memory\a.md`, ""},
		{"windows localappdata", windows, `%LOCALAPPDATA%\Temp\a.md`, `C:\Users\Owner\AppData\Local\Temp\a.md`, ""},
		{"windows unset variable", windows, `%NOPE%\a.md`, "", "cannot expand %NOPE%"},
		{"windows literal percent", windows, `50% off\a.md`, `50% off\a.md`, ""},
		{"windows doubled percent", windows, "%%", "%", ""},
		{"windows absolute untouched", windows, `C:\Temp\a.md`, `C:\Temp\a.md`, ""},
		{"windows unc untouched", windows, `\\server\share\a.md`, `\\server\share\a.md`, ""},
		{"windows dollar literal", windows, `%APPDATA%\$HOME\a.md`, `C:\Users\Owner\AppData\Roaming\$HOME\a.md`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandWith(tc.host, tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ExpandWith(%q) = %q, %v; want error containing %q", tc.in, got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandWith(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ExpandWith(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Expand reads the running host's facts, so a leading ~ on this host names
// this host's home — the rule the tools rely on.
func TestExpandUsesProcessHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Expand("~/probe.txt")
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if want := home + "/probe.txt"; got != want {
		t.Fatalf("Expand(~/probe.txt) = %q, want %q", got, want)
	}
}
