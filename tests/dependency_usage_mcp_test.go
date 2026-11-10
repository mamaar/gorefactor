package tests_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type depSymbolJSON struct {
	Symbol         string `json:"symbol"`
	References     int    `json:"references"`
	TestReferences int    `json:"test_references"`
}

type depPackageJSON struct {
	Package        string          `json:"package"`
	References     int             `json:"references"`
	TestReferences int             `json:"test_references"`
	Symbols        []depSymbolJSON `json:"symbols"`
}

type depModuleJSON struct {
	Module         string           `json:"module"`
	Version        string           `json:"version"`
	Indirect       bool             `json:"indirect"`
	References     int              `json:"references"`
	TestReferences int              `json:"test_references"`
	Files          int              `json:"files"`
	Packages       []depPackageJSON `json:"packages"`
}

type depReportJSON struct {
	Dependencies []depModuleJSON `json:"dependencies"`
	TotalCount   int             `json:"total_count"`
}

// callDependencyUsage runs dependency_usage over a workspace that requires
// example.com/dep, which is used, and example.com/idle, which is not.
func callDependencyUsage(t *testing.T, args map[string]any) depReportJSON {
	t.Helper()

	dep := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/dep\n\ngo 1.21\n",
		"dep.go": `package dep

type T struct{}

func (T) M() {}

func Foo() {}
`,
	})
	idle := writeInlineWorkspace(t, map[string]string{
		"go.mod":  "module example.com/idle\n\ngo 1.21\n",
		"idle.go": "package idle\n",
	})

	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n\n" +
			"require (\n\texample.com/dep v0.0.0\n\texample.com/idle v0.0.0\n)\n\n" +
			"replace example.com/dep => " + dep + "\n\nreplace example.com/idle => " + idle + "\n",
		"main.go": `package main

import "example.com/dep"

func main() {
	dep.Foo()
	var t dep.T
	t.M()
}
`,
		"main_test.go": `package main

import (
	"testing"

	"example.com/dep"
)

func TestFoo(t *testing.T) { dep.Foo() }
`,
	})

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "dependency_usage", Arguments: args})
	if err != nil {
		t.Fatalf("dependency_usage: %v", err)
	}

	if res.IsError {
		t.Fatalf("dependency_usage: %s", errText(res))
	}

	var out depReportJSON

	err = json.Unmarshal([]byte(errText(res)), &out)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}

	if out.TotalCount != len(out.Dependencies) {
		t.Errorf("total_count %d, want %d", out.TotalCount, len(out.Dependencies))
	}

	return out
}

func TestDependencyUsageMCP_CountsAndSorts(t *testing.T) {
	out := callDependencyUsage(t, map[string]any{})

	var names []string
	for _, m := range out.Dependencies {
		names = append(names, m.Module)
	}

	want := []string{"example.com/idle", "example.com/dep"}
	if !slices.Equal(names, want) {
		t.Fatalf("modules %v, want %v (references ascending)", names, want)
	}

	idle, dep := out.Dependencies[0], out.Dependencies[1]
	if idle.References != 0 || len(idle.Packages) != 0 {
		t.Errorf("idle = %+v, want unreferenced direct requirement", idle)
	}

	if dep.References != 4 || dep.TestReferences != 1 || dep.Files != 2 {
		t.Errorf("dep references/test/files = %d/%d/%d, want 4/1/2", dep.References, dep.TestReferences, dep.Files)
	}

	if len(dep.Packages) != 1 || dep.Packages[0].Package != "example.com/dep" {
		t.Fatalf("dep packages = %+v", dep.Packages)
	}

	got := map[string]depSymbolJSON{}
	for _, s := range dep.Packages[0].Symbols {
		got[s.Symbol] = s
	}

	wantSyms := map[string]depSymbolJSON{
		"Foo": {"Foo", 2, 1},
		"T":   {"T", 1, 0},
		"T.M": {"T.M", 1, 0},
	}
	for name, w := range wantSyms {
		if got[name] != w {
			t.Errorf("symbol %s = %+v, want %+v", name, got[name], w)
		}
	}
}

func TestDependencyUsageMCP_MaxReferences(t *testing.T) {
	out := callDependencyUsage(t, map[string]any{"max_references": 0})

	if len(out.Dependencies) != 1 || out.Dependencies[0].Module != "example.com/idle" {
		t.Errorf("max_references 0 = %+v, want only example.com/idle", out.Dependencies)
	}

	out = callDependencyUsage(t, map[string]any{"max_references": 4})
	if len(out.Dependencies) != 2 {
		t.Errorf("max_references 4 kept %d modules, want 2", len(out.Dependencies))
	}

	out = callDependencyUsage(t, map[string]any{"max_references": 3})
	if len(out.Dependencies) != 1 {
		t.Errorf("max_references 3 kept %d modules, want 1", len(out.Dependencies))
	}
}

// dependencyUsageAt runs dependency_usage in dir and returns the report, or
// the error text when the call fails.
func dependencyUsageAt(t *testing.T, dir string, args map[string]any) (depReportJSON, string) {
	t.Helper()

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "dependency_usage", Arguments: args})
	if err != nil {
		return depReportJSON{}, err.Error()
	}

	if res.IsError {
		return depReportJSON{}, errText(res)
	}

	var out depReportJSON

	err = json.Unmarshal([]byte(errText(res)), &out)
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}

	return out, ""
}

func findDepModule(t *testing.T, out depReportJSON, module string) depModuleJSON {
	t.Helper()

	for _, m := range out.Dependencies {
		if m.Module == module {
			return m
		}
	}

	t.Fatalf("module %s not in %+v", module, out.Dependencies)

	return depModuleJSON{}
}

func TestDependencyUsageMCP_SymbolsPackagesAndIndirect(t *testing.T) {
	dep := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/dep\n\ngo 1.21\n",
		"dep.go": `package dep

type A struct{ Name string }

type B struct{ Name string }

type G[T any] struct{ V T }

var Name string

var Anon struct{ X int }

func Foo() {}

func Bar() {}
`,
		"sub/sub.go": "package sub\n\nfunc S() {}\n",
	})
	ind := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/ind\n\ngo 1.21\n",
		"ind.go": "package ind\n\nfunc I() {}\n",
	})
	unusedInd := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/unusedind\n\ngo 1.21\n",
		"u.go":   "package unusedind\n",
	})

	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n\n" +
			"require (\n\texample.com/dep v0.0.0\n\texample.com/ind v0.0.0 // indirect\n\texample.com/unusedind v0.0.0 // indirect\n)\n\n" +
			"replace example.com/dep => " + dep + "\n\nreplace example.com/ind => " + ind + "\n\nreplace example.com/unusedind => " + unusedInd + "\n",
		"main.go": `package main

import (
	"example.com/dep"
	"example.com/dep/sub"
	"example.com/ind"
)

func main() {
	a, b := dep.A{Name: "a"}, dep.B{}
	_ = a.Name
	_ = b.Name
	_ = b.Name
	_ = dep.Name
	g := dep.G[int]{V: 1}
	_ = g.V
	_ = dep.Anon.X
	dep.Foo()
	dep.Bar()
	dep.Bar()
	sub.S()
	ind.I()
}
`,
		"x_test.go": `package main_test

import "example.com/dep"

func TestX() { dep.Foo() }
`,
	})

	out, msg := dependencyUsageAt(t, dir, map[string]any{})
	if msg != "" {
		t.Fatal(msg)
	}

	for _, m := range out.Dependencies {
		if m.Module == "example.com/unusedind" {
			t.Errorf("unreferenced indirect module listed: %+v", m)
		}
	}

	if !findDepModule(t, out, "example.com/ind").Indirect {
		t.Error("referenced indirect module not marked indirect")
	}

	d := findDepModule(t, out, "example.com/dep")
	if d.TestReferences != 1 {
		t.Errorf("external test package references = %d, want 1", d.TestReferences)
	}

	var pkgs, syms []string

	for _, p := range d.Packages {
		pkgs = append(pkgs, p.Package)

		if p.Package == "example.com/dep" {
			for _, s := range p.Symbols {
				syms = append(syms, s.Symbol)
			}
		}
	}

	// Ascending by references, ties by name; a composite literal key counts as a use.
	wantPkgs := []string{"example.com/dep/sub", "example.com/dep"}
	if !slices.Equal(pkgs, wantPkgs) {
		t.Errorf("packages %v, want %v", pkgs, wantPkgs)
	}

	got := map[string]int{}

	for _, p := range d.Packages {
		for _, s := range p.Symbols {
			got[s.Symbol] = s.References
		}
	}

	for sym, n := range map[string]int{"A.Name": 2, "B.Name": 2, "G.V": 2, "Name": 1, "X": 1, "Foo": 2, "Bar": 2} {
		if got[sym] != n {
			t.Errorf("symbol %s = %d, want %d (got %v)", sym, got[sym], n, got)
		}
	}

	wantOrder := []string{"A", "Anon", "B", "G", "Name", "X", "A.Name", "B.Name", "Bar", "Foo", "G.V"}
	if !slices.Equal(syms, wantOrder) {
		t.Errorf("symbol order %v, want %v", syms, wantOrder)
	}
}

func TestDependencyUsageMCP_ExcludesWorkspaceModule(t *testing.T) {
	parent := writeInlineWorkspace(t, map[string]string{
		"go.mod":     "module example.com/repo\n\ngo 1.21\n",
		"repo.go":    "package repo\n",
		"tools/a.go": "package a\n",
	})

	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/repo/tools\n\ngo 1.21\n\n" +
			"require example.com/repo v0.0.0\n\nreplace example.com/repo => " + parent + "\n",
		"a/a.go":  "package a\n\nfunc A() {}\n",
		"main.go": "package main\n\nimport \"example.com/repo/tools/a\"\n\nfunc main() { a.A() }\n",
	})

	out, msg := dependencyUsageAt(t, dir, map[string]any{})
	if msg != "" {
		t.Fatal(msg)
	}

	if m := findDepModule(t, out, "example.com/repo"); m.References != 0 {
		t.Errorf("workspace package counted against parent module: %+v", m)
	}
}

func TestDependencyUsageMCP_Errors(t *testing.T) {
	noMod := writeInlineWorkspace(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.21\n", "x.go": "package x\n"})

	_, msg := dependencyUsageAt(t, noMod, map[string]any{"max_references": -1})
	if !strings.Contains(msg, "negative") {
		t.Errorf("negative max_references error = %q", msg)
	}

	bad := writeInlineWorkspace(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.21\n\nrequire (\n", "x.go": "package x\n"})

	_, msg = dependencyUsageAt(t, bad, map[string]any{})
	if msg == "" {
		t.Error("unparsable go.mod returned no error")
	}

	none := writeInlineWorkspace(t, map[string]string{"x.go": "package x\n"})
	_ = os.Remove(filepath.Join(none, "go.mod"))

	_, msg = dependencyUsageAt(t, none, map[string]any{})
	if msg == "" {
		t.Error("workspace without go.mod returned no error")
	}
}
