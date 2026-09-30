package tests_test

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// writeInlineWorkspace writes files (relative path -> content) under a fresh
// temp directory. Unlike copyFixture, this does not read from testdata/ and
// so never needs a golden file: the tests in this file assert on tool
// results and rewritten file content directly, per this task's brief ("do
// not create golden fixtures under tests/testdata/thread_context_*" — a
// separate task owns those).
func writeInlineWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/threadmcp\n\ngo 1.21\n"
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return dir
}

func errText(result *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestAddContextParameter_ThreadWithDefaultValueRejected covers the brief's
// requirement that thread: true and default_value are mutually exclusive:
// with thread, every call site resolves its own context expression, so a
// single fixed default cannot apply to all of them.
func TestAddContextParameter_ThreadWithDefaultValueRejected(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"main.go": `package main

func Process(name string) string { return name }

func main() { Process("task") }
`,
	})

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "add_context_parameter",
		Arguments: map[string]any{
			"function_name": "Process",
			"source_file":   "main.go",
			"thread":        true,
			"default_value": "context.Background()",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an error result when thread and default_value are both set, got success: %s", errText(result))
	}
	msg := errText(result)
	if !strings.Contains(msg, "default_value") {
		t.Errorf("expected error message to mention default_value, got: %s", msg)
	}

	// Nothing should have been changed on disk.
	content, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	if strings.Contains(string(content), "context.Context") {
		t.Errorf("expected no changes to main.go after a rejected request, got:\n%s", content)
	}
}

// TestAddContextParameter_ThreadAvoidsStaleIndex proves the stale-index trap
// (BuildReferenceIndex silently skips packages with no type info, so an
// index cached from a freshly loaded — not yet type-checked — workspace has
// an empty object-identity map and CallSitesOf would find nothing) is
// avoided by construction: mcptest.Dial loads the workspace exactly the way
// load_workspace does in production, without any explicit type-checking
// step, and the very next call is add_context_parameter with thread: true.
// If the handler mistakenly reused MCPServer's cached reference index, this
// would silently succeed having changed nothing; here it must actually
// rewrite both the seed function and its caller.
func TestAddContextParameter_ThreadAvoidsStaleIndex(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"main.go": `package main

func Process(name string) string { return name }

func Run() string { return Process("task") }

func main() { Run() }
`,
	})

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	// Deliberately no analyze_symbol / workspace_status / other call that
	// might incidentally force type-checking in between load and thread:
	// the whole point is to exercise the handler right after the plain,
	// untype-checked load that load_workspace performs.
	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "add_context_parameter",
		Arguments: map[string]any{
			"function_name": "Process",
			"source_file":   "main.go",
			"thread":        true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned error: %s", errText(result))
	}

	content, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	got := string(content)

	if !strings.Contains(got, "func Process(ctx context.Context, name string) string") {
		t.Errorf("expected Process to gain a ctx context.Context parameter, got:\n%s", got)
	}
	if !strings.Contains(got, "func Run(ctx context.Context) string") {
		t.Errorf("expected Run to gain a ctx context.Context parameter (it calls Process), got:\n%s", got)
	}
	if !strings.Contains(got, "Process(ctx, \"task\")") {
		t.Errorf("expected Run to pass its own ctx to Process, got:\n%s", got)
	}
	if !strings.Contains(got, "Run(context.TODO())") {
		t.Errorf("expected main (a root) to pass context.TODO() to Run, got:\n%s", got)
	}
}

func TestAddContextParameter_Thread_RewritesCallInCgoFile(t *testing.T) {
	if !build.Default.CgoEnabled {
		t.Skip("cgo disabled on host: cgo files are not part of the package")
	}
	dir := writeInlineWorkspace(t, map[string]string{
		"leaf.go": `package main

func leaf() {}
`,
		"cgo.go": `package main

/*
#include <stdio.h>
*/
import "C"

func caller() {
	C.puts(C.CString("x"))
	leaf()
}

func main() { caller() }
`,
	})

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "add_context_parameter",
		Arguments: map[string]any{
			"function_name": "leaf",
			"source_file":   "leaf.go",
			"thread":        true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned error: %s", errText(result))
	}

	content, err := os.ReadFile(filepath.Join(dir, "cgo.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "leaf(ctx)") {
		t.Errorf("expected leaf call in cgo file to be rewritten, got:\n%s", content)
	}
	if !strings.Contains(string(content), "C.puts(C.CString(\"x\"))") {
		t.Errorf("C expressions must be untouched, got:\n%s", content)
	}
}
