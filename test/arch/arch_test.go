// Package arch guards the module's import boundaries. A document explaining
// who may import whom decays; this test reads the real import graph with
// `go list -json ./...` and fails when an edge crosses a line.
//
// `rules()` below is the single source of truth: each rule names its boundary,
// and a rule that is not yet clean carries an allowlist of admitted edges, one
// ticket per edge. Nothing else lists rules — a header that repeats them drifts
// from the code, which is the exact failure this test exists to catch. A
// failure prints the rule name and, for an admitted edge, its ticket.
//
// To add a rule, add an entry to `rules()`. To admit an edge temporarily, add
// it to that rule's allowlist with the ticket that will remove it; a stale
// entry fails the test, so the ticket must close in the same change that
// removes the edge. A rule with `hard: true` may not carry an allowlist.
//
// cmd/** is free (composition root), and web/**, assets/** are not Go imports.
//
// The scan reads each package's direct Imports — the same set `go list -json`
// reports — not TestImports/XTestImports. Test-only coupling is not a runtime
// boundary, and the counts this test started from were direct imports.
package arch_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// pkg is the subset of `go list -json` this test needs.
type pkg struct {
	ImportPath string
	Imports    []string
	Standard   bool
	Module     *struct{ Path string }
}

type edge struct{ from, to string }

// allow is one admitted boundary violation: the edge and the ticket that will
// remove it. Every entry must match a real edge; a stale entry fails the test
// so the list shrinks with the work instead of outliving it.
type allow struct {
	from, to string
	ticket   string
}

type rule struct {
	name   string
	from   func(string) bool
	breaks func(string) bool
	hard   bool // hard rules may not carry allowlist entries
	allow  []allow
}

func within(prefix string) func(string) bool {
	return func(p string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }
}

func anyOf(prefixes ...string) func(string) bool {
	return func(p string) bool {
		for _, prefix := range prefixes {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				return true
			}
		}
		return false
	}
}

func rules() []rule {
	return []rule{
		{
			// Zero today; keep it that way without exceptions.
			name:   "domain-isolated",
			from:   within("infrastructure/domain"),
			breaks: func(to string) bool { return !within("infrastructure/domain")(to) },
			hard:   true,
		},
		{
			name:   "application-no-storage-transport",
			from:   within("application"),
			breaks: anyOf("infrastructure/repository", "infrastructure/service", "infrastructure/delivery"),
			hard:   true,
		},
		{
			// The worker is a mechanism behind the job-executor port:
			// application takes the port, cmd picks the implementation.
			// T-21 moved the last two callers; the rule is hard now.
			name:   "application-no-worker",
			from:   within("application"),
			breaks: anyOf("infrastructure/worker"),
			hard:   true,
		},
		{
			name:   "repository-no-application-delivery",
			from:   within("infrastructure/repository"),
			breaks: anyOf("application", "infrastructure/delivery"),
			hard:   true,
		},
		{
			// The slash-command adapters moved to delivery/cli/commands in
			// T-23; service keeps the provider/protocol adapters only, and the
			// rule is hard now.
			name:   "service-no-application",
			from:   within("infrastructure/service"),
			breaks: anyOf("application"),
			hard:   true,
		},
		{
			// CLI administration wires the repositories directly today; the
			// transport belongs behind the use cases (T-24).
			name:   "delivery-no-repository",
			from:   within("infrastructure/delivery"),
			breaks: anyOf("infrastructure/repository"),
			allow: []allow{
				{from: "infrastructure/delivery/cli/admin", to: "infrastructure/repository/config", ticket: "T-24"},
				{from: "infrastructure/delivery/cli/admin", to: "infrastructure/repository/conversation", ticket: "T-24"},
				{from: "infrastructure/delivery/cli/admin", to: "infrastructure/repository/gateway", ticket: "T-24"},
				{from: "infrastructure/delivery/cli/admin", to: "infrastructure/repository/secret", ticket: "T-24"},
			},
		},
	}
}

// moduleRoot walks up from the test's working directory (the package dir) to
// the directory holding go.mod. `go list ./...` runs there: a package-scoped
// test would otherwise see only itself.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's directory")
		}
		dir = parent
	}
}

// loadPackages reads the module's import graph.
func loadPackages(t *testing.T) (module string, pkgs []pkg) {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = moduleRoot(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -json ./... failed: %v\n%s", err, stderr.String())
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("decode go list output: %v", err)
		}
		if p.Standard {
			continue
		}
		if p.Module != nil && module == "" {
			module = p.Module.Path
		}
		pkgs = append(pkgs, p)
	}
	if module == "" {
		t.Fatal("could not determine the module path from go list output")
	}
	return module, pkgs
}

// relative turns an import path into a module-relative one; ok is false for the
// standard library and anything outside this module.
func relative(module, path string) (string, bool) {
	if path == module {
		return "", true
	}
	if !strings.HasPrefix(path, module+"/") {
		return "", false
	}
	return strings.TrimPrefix(path, module+"/"), true
}

func TestImportBoundaries(t *testing.T) {
	module, pkgs := loadPackages(t)

	type violation struct {
		rule rule
		edge edge
	}
	var newViolations []violation
	var used = map[edge]bool{}

	for _, r := range rules() {
		if r.hard && len(r.allow) > 0 {
			t.Errorf("rule %s is hard but carries %d allowlist entr(ies): a boundary with no exceptions cannot quietly gain one", r.name, len(r.allow))
			continue
		}
		allowed := make(map[edge]allow, len(r.allow))
		for _, a := range r.allow {
			allowed[edge{a.from, a.to}] = a
		}
		seen, admitted := 0, 0
		for _, p := range pkgs {
			from, ok := relative(module, p.ImportPath)
			if !ok || !r.from(from) {
				continue
			}
			for _, imp := range p.Imports {
				to, ok := relative(module, imp)
				if !ok || !r.breaks(to) {
					continue
				}
				e := edge{from, to}
				seen++
				if _, ok := allowed[e]; ok {
					admitted++
					used[e] = true
					continue
				}
				newViolations = append(newViolations, violation{rule: r, edge: e})
			}
		}
		t.Logf("rule %-35s edges=%d admitted=%d new=%d", r.name, seen, admitted, seen-admitted)
	}

	if len(newViolations) > 0 {
		sort.Slice(newViolations, func(i, j int) bool {
			if newViolations[i].edge.from != newViolations[j].edge.from {
				return newViolations[i].edge.from < newViolations[j].edge.from
			}
			return newViolations[i].edge.to < newViolations[j].edge.to
		})
		var b strings.Builder
		fmt.Fprintf(&b, "%d import edge(s) cross a boundary with no ticket:\n", len(newViolations))
		for _, v := range newViolations {
			fmt.Fprintf(&b, "  %s -> %s   (rule %s)\n", v.edge.from, v.edge.to, v.rule.name)
		}
		b.WriteString("\nmove the dependency to its owner layer, or record it as an admitted edge with a ticket in test/arch/arch_test.go.\n")
		b.WriteString("list them yourself with: go list -f '{{.ImportPath}}: {{join .Imports \" \"}}' ./...")
		t.Error(b.String())
	}

	// A stale allowlist entry is a rule that already got its wish: deleting it
	// in the same change is what keeps the list honest.
	var stale []string
	for _, r := range rules() {
		for _, a := range r.allow {
			if !used[edge{a.from, a.to}] {
				stale = append(stale, fmt.Sprintf("  allowlist entry gone: %s -> %s (%s, rule %s) — the move landed, delete the entry",
					a.from, a.to, a.ticket, r.name))
			}
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("%d stale allowlist entr(ies):\n%s", len(stale), strings.Join(stale, "\n"))
	}
}

// TestRulesCoverEveryGoPackage keeps the scanner honest about its own reach: a
// new top-level Go tree would otherwise be invisible to every rule.
func TestRulesCoverEveryGoPackage(t *testing.T) {
	module, pkgs := loadPackages(t)
	known := []string{"application", "cmd", "infrastructure", "test", "web", "assets"}
	for _, p := range pkgs {
		rel, ok := relative(module, p.ImportPath)
		if !ok || rel == "" {
			continue
		}
		top := strings.SplitN(rel, "/", 2)[0]
		found := false
		for _, k := range known {
			if top == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("package %s sits under %q, which no boundary rule or exemption names — add it to the rule set", rel, top)
		}
	}
}
