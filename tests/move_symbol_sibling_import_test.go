package tests_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestMoveSymbolAddsDestinationImportWhenSiblingFileAlreadyHasIt is a
// regression test for a second, narrower variant of the missing-import bug
// covered by TestMoveSymbolAddsDestinationImportAtReferencingSites.
//
// Root cause: generateImportChanges (pkg/refactor/operations.go) decided
// whether to add the destination import with
// slices.Contains(refPackage.Imports, importPath). Package.Imports is the
// *union* of the imports of every file in the package, so as soon as any one
// file imported the destination — typically because an earlier move in the
// same sequence put it there — every other file in that package was treated as
// already importing it. Their references were still rewritten to skip.Foo, but
// no import followed, and the package stopped compiling. The check has to be
// per-file (hasImport), since imports are a file-level construct.
//
// The referencing file here also uses the single-line `import "testing"` form,
// so this exercises the single-line-to-block conversion path in
// GenerateAddImportChange at the same time.
func TestMoveSymbolAddsDestinationImportWhenSiblingFileAlreadyHasIt(t *testing.T) {
	tmpDir := t.TempDir()

	mustWrite(t, filepath.Join(tmpDir, "go.mod"), "module reprotest\n\ngo 1.22\n")

	// Sibling file in the source package that ALREADY imports the destination
	// package. This is what poisoned the package-level import check.
	mustWrite(t, filepath.Join(tmpDir, "test", "test.go"), `package test

import (
	"testing"

	"reprotest/test/skip"
)

func RequireSomething(t *testing.T) {
	skip.Something(t)
}
`)

	mustWrite(t, filepath.Join(tmpDir, "test", "flaky.go"), `package test

import (
	"os"
	"testing"
)

func SkipFlaky(t *testing.T) {
	if _, ok := os.LookupEnv("SKIP_FLAKY"); ok {
		t.Skip("Skipping flaky test")
	}
}
`)

	// The referencing file: same package as the moved symbol, unqualified
	// reference, and a single-line import declaration.
	mustWrite(t, filepath.Join(tmpDir, "test", "flaky_test.go"), `package test

import "testing"

func TestSkipFlaky(t *testing.T) {
	SkipFlaky(t)
}
`)

	mustWrite(t, filepath.Join(tmpDir, "test", "skip", "skip.go"), `package skip

import (
	"fmt"
	"testing"
)

func Something(t *testing.T) {
	fmt.Println("skip", t.Name())
}
`)

	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "SkipFlaky",
			"from_package": "test",
			"to_package":   "test/skip",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}

	if result.IsError {
		t.Fatalf("move_symbol returned an error result: %+v", result.Content)
	}

	refPath := filepath.Join(tmpDir, "test", "flaky_test.go")
	refContent := mustRead(t, refPath)

	if !strings.Contains(refContent, `"reprotest/test/skip"`) {
		t.Errorf("expected flaky_test.go to import the destination package, got:\n%s", refContent)
	}

	if !strings.Contains(refContent, "skip.SkipFlaky(") {
		t.Errorf("expected flaky_test.go's call to be rewritten to skip.SkipFlaky, got:\n%s", refContent)
	}

	// _test.go files are not compiled by `go build`, so the missing import only
	// surfaces under vet/test.
	cmd := exec.Command("go", "vet", "./...")

	cmd.Dir = tmpDir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture module does not vet clean after the move (this is the actual bug):\n%s", out)
	}
}
