package tests_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestAnalyzeDependencies_SuggestedMovesNameSymbolAndReferrers(t *testing.T) {
	dir := mbdWorkspace(t, nil)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{"suggest_moves": true})

	moves, _ := out["suggested_moves"].([]any)
	if len(moves) != 1 {
		t.Fatalf("suggested_moves = %v, want one move", out["suggested_moves"])
	}
	raw, _ := json.Marshal(moves[0])
	for _, want := range []string{`"Shared"`, "threadmcp/src", "threadmcp/a", "threadmcp/b"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("move = %s, want it to mention %q", raw, want)
		}
	}
	if out["package_coupling"] == nil {
		t.Errorf("package_coupling missing from %v", out)
	}
	assertFileUnchanged(t, dir, "src/src.go", mbdSrc)
}

func TestAnalyzeDependencies_NoSharedSymbolsGivesEmptyList(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nfunc A() int { return 1 }\n",
		"b/b.go": "package b\n\nfunc B() int { return 2 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{"suggest_moves": true, "detect_backwards": true})

	for _, key := range []string{"suggested_moves", "backwards_dependencies"} {
		list, ok := out[key].([]any)
		if !ok || len(list) != 0 {
			t.Errorf("%s = %#v, want an empty list", key, out[key])
		}
	}
}

func TestAnalyzeDependencies_FlagsGateTheirSections(t *testing.T) {
	dir := mbdWorkspace(t, nil)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{})

	for _, key := range []string{"suggested_moves", "backwards_dependencies"} {
		if _, ok := out[key]; ok {
			t.Errorf("%s present without its flag: %v", key, out[key])
		}
	}
}

func TestAnalyzeDependencies_InnerImportingOuterIsBackwardsDependency(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"domain/d.go":  "package domain\n\nimport \"example.com/threadmcp/handler\"\n\nfunc D() int { return handler.H() }\n",
		"handler/h.go": "package handler\n\nfunc H() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{"detect_backwards": true})

	deps, _ := out["backwards_dependencies"].([]any)
	if len(deps) != 1 {
		t.Fatalf("backwards_dependencies = %v, want one", out["backwards_dependencies"])
	}
	raw, _ := json.Marshal(deps[0])
	for _, want := range []string{"threadmcp/domain", "threadmcp/handler"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("dependency = %s, want it to mention %q", raw, want)
		}
	}
}

func TestAnalyzeDependencies_CountsImportsAndSymbolsOfEveryConfig(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go":         "package a\n\nfunc A() int { return 1 }\n",
		"a/a_windows.go": "package a\n\nimport \"example.com/threadmcp/b\"\n\nfunc W() int { return b.B() }\n",
		"a/a_linux.go":   "package a\n\nfunc L() int { return 1 }\n",
		"b/b.go":         "package b\n\nfunc B() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{})

	raw, _ := json.Marshal(out)
	var got struct {
		Coupling map[string]struct {
			Outgoing int `json:"outgoing_deps"`
			Incoming int `json:"incoming_deps"`
			Symbols  int `json:"symbol_count"`
		} `json:"package_coupling"`
		Graph struct {
			PackageImports map[string][]string
			PackageDeps    map[string][]string
		} `json:"dependency_graph"`
		Configs []string `json:"build_configs"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	var aDir, bDir string
	for d := range got.Graph.PackageImports {
		if strings.HasSuffix(d, "/a") {
			aDir = d
		}
	}
	for d := range got.Coupling {
		if strings.HasSuffix(d, "/b") {
			bDir = d
		}
	}
	if !slices.Contains(got.Graph.PackageImports[aDir], "example.com/threadmcp/b") {
		t.Errorf("PackageImports[a] = %v, want the windows-only import", got.Graph.PackageImports[aDir])
	}
	if !slices.Contains(got.Graph.PackageDeps[aDir], bDir) {
		t.Errorf("PackageDeps[a] = %v, want %s", got.Graph.PackageDeps[aDir], bDir)
	}
	if c := got.Coupling[bDir]; c.Incoming != 1 {
		t.Errorf("b incoming_deps = %d, want 1", c.Incoming)
	}
	if c := got.Coupling[aDir]; c.Symbols != 3 || c.Outgoing != 1 {
		t.Errorf("a coupling = %+v, want 3 symbols and 1 outgoing", c)
	}
	if len(got.Configs) < 2 {
		t.Errorf("build_configs = %v, want several", got.Configs)
	}
}

func TestAnalyzeDependencies_CyclesAreObjectsNamingTheirConfigs(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go":         "package a\n\nfunc A() int { return 1 }\n",
		"a/a_windows.go": "package a\n\nimport \"example.com/threadmcp/b\"\n\nfunc W() int { return b.B() }\n",
		"b/b.go":         "package b\n\nimport \"example.com/threadmcp/a\"\n\nfunc B() int { return a.A() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{})

	graph, _ := out["dependency_graph"].(map[string]any)
	cycles, _ := graph["ImportCycles"].([]any)
	if len(cycles) != 1 {
		t.Fatalf("ImportCycles = %v, want one", graph["ImportCycles"])
	}
	cycle, _ := cycles[0].(map[string]any)
	if p, _ := cycle["packages"].([]any); len(p) != 3 {
		t.Errorf("packages = %v, want a closed path of 3", cycle["packages"])
	}
	if c, _ := cycle["configs"].([]any); len(c) == 0 {
		t.Errorf("configs = %v, want the windows configuration", cycle["configs"])
	}
	if tc, ok := graph["TestImportCycles"].([]any); !ok || len(tc) != 0 {
		t.Errorf("TestImportCycles = %#v, want an empty list", graph["TestImportCycles"])
	}
}

func TestAnalyzeDependencies_ImportOnlyInTagFileIsInPackageImports(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go":      "package a\n\nfunc A() int { return 1 }\n",
		"a/a_tagx.go": "//go:build tagx\n\npackage a\n\nimport \"example.com/threadmcp/b\"\n\nfunc X() int { return b.B() }\n",
		"b/b.go":      "package b\n\nfunc B() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "analyze_dependencies", map[string]any{})

	graph, _ := out["dependency_graph"].(map[string]any)
	imports, _ := graph["PackageImports"].(map[string]any)
	for d, v := range imports {
		if !strings.HasSuffix(d, "/a") {
			continue
		}
		if l, _ := v.([]any); len(l) != 1 || l[0] != "example.com/threadmcp/b" {
			t.Errorf("PackageImports[a] = %v, want the tagx-only import", v)
		}
		return
	}
	t.Errorf("PackageImports = %v, want an entry for a", imports)
}
