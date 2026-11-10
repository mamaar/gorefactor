package tests_test

import (
	"encoding/json"
	"slices"
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
