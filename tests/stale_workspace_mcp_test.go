package tests_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const staleGoMod = "module example.com/stale\n\ngo 1.21\n"

const staleLib = `package lib

func Fetch(a int) int { return a }

func Wrap(a int) int { return Fetch(a) + 1 }
`

const staleLibTest = `package lib

import "testing"

func TestFetch(t *testing.T) {
	_ = Fetch(1)
	_ = Fetch(2)
}
`

func writeStaleFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":          staleGoMod,
		"lib/lib.go":      staleLib,
		"lib/lib_test.go": staleLibTest,
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func callJSON(t *testing.T, sess *mcptest.Session, tool string, args map[string]any) map[string]any {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) error: %s", tool, errText(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(errText(res)), &out); err != nil {
		t.Fatalf("decode %s: %v", tool, err)
	}
	return out
}

func addParamArgs(fn string, params []map[string]any, dryRun bool) map[string]any {
	return map[string]any{
		"function_name": fn,
		"source_file":   "lib/lib.go",
		"subcommand":    "add_param",
		"params":        params,
		"default_value": "0",
		"position":      1,
		"dry_run":       dryRun,
	}
}

func changeCount(t *testing.T, r map[string]any) int {
	t.Helper()
	n, _ := r["change_count"].(float64)
	return int(n)
}

// A file edited on disk after load_workspace must be planned against its
// current content: dry run and apply agree and every call site is rewritten.
func TestChangeSignature_ExternalEditAfterLoad(t *testing.T) {
	dir := writeStaleFixture(t)
	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	testPath := filepath.Join(dir, "lib", "lib_test.go")
	edited := strings.Replace(staleLibTest, "func TestFetch", "var extra = 42\n\nvar extra2 = \"padding\"\n\nfunc TestFetch", 1)
	if err := os.WriteFile(testPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	params := []map[string]any{{"name": "a", "type": "int"}, {"name": "b", "type": "int"}}
	dry := callJSON(t, sess, "change_signature", addParamArgs("Fetch", params, true))
	applied := callJSON(t, sess, "change_signature", addParamArgs("Fetch", params, false))

	if changeCount(t, dry) != 4 {
		t.Fatalf("dry run change_count = %d, want 4 (signature + 1 + 2 calls)", changeCount(t, dry))
	}
	if changeCount(t, applied) != changeCount(t, dry) {
		t.Errorf("apply change_count = %d, dry run = %d", changeCount(t, applied), changeCount(t, dry))
	}
	got := readFile(t, testPath)
	if strings.Contains(got, "Fetch(1)") || strings.Contains(got, "Fetch(2)") || !strings.Contains(got, "var extra = 42") {
		t.Errorf("test file call sites not rewritten or external edit lost:\n%s", got)
	}
	if lib := readFile(t, filepath.Join(dir, "lib", "lib.go")); strings.Contains(lib, "Fetch(a)") {
		t.Errorf("lib.go call site not rewritten:\n%s", lib)
	}
}

// Two consecutive applies touching the same files must both see the previous
// apply's output.
func TestChangeSignature_ConsecutiveApplies(t *testing.T) {
	dir := writeStaleFixture(t)
	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	p1 := []map[string]any{{"name": "a", "type": "int"}, {"name": "b", "type": "int"}}
	first := callJSON(t, sess, "change_signature", addParamArgs("Fetch", p1, false))
	if changeCount(t, first) != 4 {
		t.Fatalf("first apply change_count = %d, want 4", changeCount(t, first))
	}

	p2 := []map[string]any{{"name": "a", "type": "int"}, {"name": "c", "type": "string"}, {"name": "b", "type": "int"}}
	args := addParamArgs("Fetch", p2, true)
	args["default_value"] = `""`
	dry := callJSON(t, sess, "change_signature", args)
	args["dry_run"] = false
	second := callJSON(t, sess, "change_signature", args)

	if changeCount(t, dry) != 4 || changeCount(t, second) != 4 {
		t.Fatalf("dry=%d apply=%d, want 4/4", changeCount(t, dry), changeCount(t, second))
	}
	for _, f := range []string{"lib.go", "lib_test.go"} {
		if got := readFile(t, filepath.Join(dir, "lib", f)); strings.Contains(got, "Fetch(1, 0)") || strings.Contains(got, "Fetch(a, 0)") {
			t.Errorf("%s: second apply missed call sites:\n%s", f, got)
		}
	}
}
