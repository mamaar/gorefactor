package tests_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func analyzeSymbolIn(t *testing.T, files map[string]string, args map[string]any) (map[string]any, string) {
	t.Helper()
	files["go.mod"] = "module example.com/asym\n\ngo 1.21\n"
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "analyze_symbol", Arguments: args})
	if err != nil {
		t.Fatalf("analyze_symbol: %v", err)
	}
	if res.IsError {
		return nil, errText(res)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(errText(res)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out, ""
}

func TestAnalyzeSymbol_FindsSymbolDeclaredOnlyUnderAnotherConfig(t *testing.T) {
	out, errMsg := analyzeSymbolIn(t, map[string]string{
		"a/a.go":           "package a\n\nfunc Use() { OnlyWin() }\n",
		"a/win_windows.go": "package a\n\nfunc OnlyWin() {}\n",
	}, map[string]any{"symbol": "OnlyWin", "package": "a"})
	if errMsg != "" {
		t.Fatalf("symbol declared under windows not found: %s", errMsg)
	}
	if filepath.Base(out["file"].(string)) != "win_windows.go" {
		t.Errorf("file = %v, want win_windows.go", out["file"])
	}
	if _, ok := out["declarations"]; ok {
		t.Errorf("declarations present for a single declaration: %v", out["declarations"])
	}
}

func TestAnalyzeSymbol_PerOSDeclarationsAreListedWithConfigs(t *testing.T) {
	out, errMsg := analyzeSymbolIn(t, map[string]string{
		"a/a.go":           "package a\n\nfunc Use() { Sys() }\n",
		"a/sys_linux.go":   "package a\n\nfunc Sys() {}\n",
		"a/sys_windows.go": "package a\n\nfunc Sys() {}\n",
	}, map[string]any{"symbol": "Sys", "package": "a"})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	decls, _ := out["declarations"].([]any)
	if len(decls) != 2 {
		t.Fatalf("declarations = %v, want 2", out["declarations"])
	}
	for _, d := range decls {
		if cfgs, _ := d.(map[string]any)["configs"].([]any); len(cfgs) == 0 {
			t.Errorf("declaration %v has no configs", d)
		}
	}
	if n, _ := out["reference_count"].(float64); n != 1 {
		t.Errorf("reference_count = %v, want 1 (the caller seen by both configs counts once)", out["reference_count"])
	}
	if cfgs, _ := out["build_configs"].([]any); len(cfgs) < 2 {
		t.Errorf("build_configs = %v, want several", out["build_configs"])
	}
}

func TestAnalyzeSymbol_CountsCallerInTaggedFile(t *testing.T) {
	out, errMsg := analyzeSymbolIn(t, map[string]string{
		"a/a.go":      "package a\n\nfunc F() {}\n\nfunc Use() { F() }\n",
		"a/tagged.go": "//go:build tagx\n\npackage a\n\nfunc UseTagged() { F() }\n",
	}, map[string]any{"symbol": "F", "package": "a"})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if n, _ := out["reference_count"].(float64); n != 2 {
		t.Errorf("reference_count = %v, want 2", out["reference_count"])
	}
}

func TestAnalyzeSymbol_SingleConfigOmitsMultiConfigFields(t *testing.T) {
	out, errMsg := analyzeSymbolIn(t, map[string]string{
		"a/a.go": "package a\n\nfunc F() {}\n\nfunc Use() { F() }\n",
	}, map[string]any{"symbol": "F"})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	for _, k := range []string{"declarations", "build_configs", "uncovered_files"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s present in a single-config result", k)
		}
	}
	if n, _ := out["reference_count"].(float64); n != 1 {
		t.Errorf("reference_count = %v, want 1", out["reference_count"])
	}
}

func TestAnalyzeSymbol_UnknownSymbolStillErrors(t *testing.T) {
	_, errMsg := analyzeSymbolIn(t, map[string]string{"a/a.go": "package a\n"}, map[string]any{"symbol": "Nope"})
	if errMsg == "" {
		t.Fatal("want an error for an unknown symbol")
	}
}
