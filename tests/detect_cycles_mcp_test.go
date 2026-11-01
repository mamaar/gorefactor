package tests_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const (
	dcA = "package a\n\nimport \"example.com/threadmcp/b\"\n\nfunc A() int { return b.B() }\n"
	dcB = "package b\n\nimport \"example.com/threadmcp/a\"\n\nfunc B() int { return a.A() }\n"
)

func TestDetectCycles_ReportsProductionCycleAsImportPaths(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"a/a.go": dcA, "b/b.go": dcB})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "detect_cycles", map[string]any{})

	if out["cycles_found"] != float64(1) {
		t.Fatalf("cycles_found = %v, want 1", out["cycles_found"])
	}
	cycles, _ := out["cycles"].([]any)
	if len(cycles) != 1 {
		t.Fatalf("cycles = %v, want one cycle", out["cycles"])
	}
	var path []string
	for _, p := range cycles[0].(map[string]any)["packages"].([]any) {
		path = append(path, p.(string))
	}
	a, b := "example.com/threadmcp/a", "example.com/threadmcp/b"
	if !reflect.DeepEqual(path, []string{a, b, a}) && !reflect.DeepEqual(path, []string{b, a, b}) {
		t.Errorf("cycle = %v, want a closed path through %s and %s", path, a, b)
	}
	if tc, ok := out["test_cycles"].([]any); !ok || len(tc) != 0 {
		t.Errorf("test_cycles = %#v, want an empty list", out["test_cycles"])
	}
	if _, err := os.Stat(filepath.Join(dir, "cycles_report.md")); !os.IsNotExist(err) {
		t.Errorf("cycles_report.md exists after detect_cycles (stat err = %v)", err)
	}
}

func TestDetectCycles_TestFileCycleIsReportedSeparately(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go":      "package a\n\nfunc A() int { return 1 }\n",
		"a/a_test.go": "package a\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/b\"\n)\n\nfunc TestA(t *testing.T) { _ = b.B() }\n",
		"b/b.go":      dcB,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "detect_cycles", map[string]any{})

	if out["cycles_found"] != float64(0) {
		t.Errorf("cycles_found = %v, want 0", out["cycles_found"])
	}
	if c, ok := out["cycles"].([]any); !ok || len(c) != 0 {
		t.Errorf("cycles = %#v, want an empty list", out["cycles"])
	}
	if tc, _ := out["test_cycles"].([]any); len(tc) != 1 {
		t.Errorf("test_cycles = %v, want one entry", out["test_cycles"])
	}
}

func TestDetectCycles_AcyclicWorkspaceGivesEmptyLists(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/threadmcp/b\"\n\nfunc A() int { return b.B() }\n",
		"b/b.go": "package b\n\nfunc B() int { return 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "detect_cycles", map[string]any{})

	if out["cycles_found"] != float64(0) {
		t.Errorf("cycles_found = %v, want 0", out["cycles_found"])
	}
	for _, key := range []string{"cycles", "test_cycles"} {
		if l, ok := out[key].([]any); !ok || len(l) != 0 {
			t.Errorf("%s = %#v, want an empty list", key, out[key])
		}
	}
}

func TestDetectCycles_RemovedFixToolIsNotRegistered(t *testing.T) {
	oldTool := "fix" + "_cycles"
	dir := writeInlineWorkspace(t, map[string]string{"a/a.go": "package a\n"})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: oldTool, Arguments: map[string]any{}})
	if err == nil && !res.IsError {
		t.Errorf("%s call succeeded, want an error", oldTool)
	}
}

const dcModule = "module example.com/cyc\n\ngo 1.21\n"

type cyclesJSON struct {
	Found  int `json:"cycles_found"`
	Cycles []struct {
		Packages []string `json:"packages"`
		Configs  []string `json:"configs"`
	} `json:"cycles"`
	TestCycles []struct {
		Packages []string `json:"packages"`
		Configs  []string `json:"configs"`
	} `json:"test_cycles"`
	BuildConfigs []string `json:"build_configs"`
}

func callDetectCycles(t *testing.T, files map[string]string) cyclesJSON {
	t.Helper()
	files["go.mod"] = dcModule
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), writeInlineWorkspace(t, files))
	defer sess.Close()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "detect_cycles", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("detect_cycles: %v %s", err, errText(res))
	}
	var out cyclesJSON
	if err := json.Unmarshal([]byte(errText(res)), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}
	return out
}

func TestDetectCycles_CycleOnlyUnderWindowsNamesItsConfigs(t *testing.T) {
	out := callDetectCycles(t, map[string]string{
		"a/a.go":         "package a\n\nfunc A() int { return 1 }\n",
		"a/a_windows.go": "package a\n\nimport \"example.com/cyc/b\"\n\nfunc W() int { return b.B() }\n",
		"b/b.go":         "package b\n\nimport \"example.com/cyc/a\"\n\nfunc B() int { return a.A() }\n",
	})

	if out.Found != 1 || len(out.Cycles) != 1 {
		t.Fatalf("cycles = %+v, want one", out.Cycles)
	}
	if len(out.Cycles[0].Configs) == 0 {
		t.Errorf("configs empty, want the windows configuration")
	}
	for _, c := range out.Cycles[0].Configs {
		if !strings.Contains(c, "windows") {
			t.Errorf("config %q is not windows", c)
		}
	}
	if len(out.BuildConfigs) < 2 {
		t.Errorf("build_configs = %v, want several", out.BuildConfigs)
	}
}

func TestDetectCycles_EdgesFromDifferentConfigsDoNotCloseACycle(t *testing.T) {
	out := callDetectCycles(t, map[string]string{
		"a/a.go":         "package a\n\nfunc A() int { return 1 }\n",
		"a/a_linux.go":   "package a\n\nimport \"example.com/cyc/b\"\n\nfunc L() int { return b.B() }\n",
		"b/b.go":         "package b\n\nfunc B() int { return 1 }\n",
		"b/b_windows.go": "package b\n\nimport \"example.com/cyc/a\"\n\nfunc W() int { return a.A() }\n",
	})

	if out.Found != 0 || len(out.Cycles) != 0 {
		t.Errorf("cycles = %+v, want none", out.Cycles)
	}
}

func TestDetectCycles_TestCycleThroughTaggedTestFile(t *testing.T) {
	out := callDetectCycles(t, map[string]string{
		"a/a.go":              "package a\n\nfunc A() int { return 1 }\n",
		"a/a_windows_test.go": "package a\n\nimport (\n\t\"testing\"\n\n\t\"example.com/cyc/b\"\n)\n\nfunc TestW(t *testing.T) { _ = b.B() }\n",
		"b/b.go":              "package b\n\nimport \"example.com/cyc/a\"\n\nfunc B() int { return a.A() }\n",
	})

	if len(out.TestCycles) != 1 || len(out.Cycles) != 0 {
		t.Fatalf("cycles = %+v, test_cycles = %+v, want one test cycle", out.Cycles, out.TestCycles)
	}
	if len(out.TestCycles[0].Configs) == 0 {
		t.Errorf("test cycle configs empty, want the windows configuration")
	}
}
