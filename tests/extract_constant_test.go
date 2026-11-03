package tests_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	files["go.mod"] = "module example.com/ec\n\ngo 1.21\n"
	for name, content := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func callExtractConstant(ctx context.Context, t *testing.T, sess *mcptest.Session, args map[string]any) (*mcpsdk.CallToolResult, string) {
	t.Helper()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "extract_constant", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(extract_constant): %v", err)
	}
	var text strings.Builder

	for _, c := range result.Content {
		tc, ok := c.(*mcpsdk.TextContent)
		if ok {
			text.WriteString(tc.Text)
		}
	}

	return result, text.String()
}

// The MCP server's engine skips its own compilation check, so the test runs
// go vet on the result: a broken file fails it.
func TestExtractConstantMCP_ReplacesAcrossPackageAndStillCompiles(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"a.go":      "package p\n\ntype Color string\n\nvar C Color = \"red\"\n",
		"b.go":      "package p\n\nvar S string = \"red\"\n\nfunc F() string {\n\tRed := 1\n\t_ = Red\n\treturn \"red\"\n}\n",
		"a_test.go": "package p\n\nimport \"testing\"\n\nfunc TestC(t *testing.T) {\n\tif C != \"red\" {\n\t}\n}\n",
	})
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcptest.InProcess(), dir)
	defer sess.Close()

	result, text := callExtractConstant(ctx, t, sess, map[string]any{
		"source_file": "a.go", "line": 5, "column": 16, "constant_name": "Red",
	})
	if result.IsError {
		t.Fatalf("extract_constant failed: %s", text)
	}

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}

		return string(b)
	}

	got := read("a.go")
	if !strings.Contains(got, "const Red = \"red\"") || !strings.Contains(got, "var C Color = Red") {
		t.Errorf("a.go:\n%s", got)
	}

	got = read("b.go")
	if !strings.Contains(got, "var S string = Red") || !strings.Contains(got, "return \"red\"") {
		t.Errorf("b.go:\n%s", got)
	}

	got = read("a_test.go")
	if !strings.Contains(got, "C != Red") {
		t.Errorf("a_test.go:\n%s", got)
	}

	vet := exec.Command("go", "vet", "./...")
	vet.Dir = dir

	out, err := vet.CombinedOutput()
	if err != nil {
		t.Errorf("go vet after extract_constant: %v\n%s", err, out)
	}

	if !strings.Contains(text, `"skipped_sites"`) || !strings.Contains(text, `"line": 8`) {
		t.Errorf("shadowed occurrence not reported:\n%s", text)
	}
}

func TestExtractConstantMCP_RefusalWritesNothing(t *testing.T) {
	src := "package p\n\nvar A = \"red\"\n\nconst Red = 1\n"
	dir := writeModule(t, map[string]string{"a.go": src})
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcptest.InProcess(), dir)
	defer sess.Close()

	result, text := callExtractConstant(ctx, t, sess, map[string]any{
		"source_file": "a.go", "line": 3, "column": 10, "constant_name": "Red",
	})
	if !result.IsError || !strings.Contains(text, "already declared") {
		t.Fatalf("want a name-conflict error, got IsError=%v: %s", result.IsError, text)
	}

	b, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if string(b) != src {
		t.Errorf("a.go changed:\n%s", b)
	}
}
