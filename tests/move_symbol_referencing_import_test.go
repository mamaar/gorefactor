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

// TestMoveSymbolAddsDestinationImportAtReferencingSites is a regression test
// for a bug where move_symbol rewrote qualified references to a moved symbol
// in *other* packages' files (e.g. "test.RequireBinary" -> "skip.RequireBinary")
// without ever adding an import for the destination package to those files.
// The rewritten reference compiled to an undefined package identifier.
//
// Root cause: findPackageForFile (pkg/refactor/operations.go) and its
// duplicate Validator.findPackageForFile (pkg/refactor/validation.go) only
// scanned pkg.Files, never pkg.TestFiles. generateImportChanges uses
// findPackageForFile to decide, per reference, whether the reference's own
// file already belongs to the target package (skip) or needs the new
// import added. For a _test.go reference site, findPackageForFile returned
// nil, and generateImportChanges' `refPackage != nil && refPackage.Path !=
// targetPackagePath` guard silently treated nil as "nothing to add an
// import for" instead of "package unknown, but definitely not the target".
// generateReferenceUpdateChange happened to still rewrite the reference
// correctly despite the same nil (its own guard only *skips* on a matching
// non-nil package), which is why references were rewritten but no import
// followed — the two functions failed differently on the same missing
// TestFiles lookup, and only the second failure was visible in the diff.
//
// This also covers the case where the referencing file uses another symbol
// from the source package too (test.Other, alongside test.RequireBinary):
// the source import must be RETAINED, and the destination import ADDED
// alongside it, not swapped for it.
func TestMoveSymbolAddsDestinationImportAtReferencingSites(t *testing.T) {
	tmpDir := t.TempDir()

	mustWrite(t, filepath.Join(tmpDir, "go.mod"), "module reprotest\n\ngo 1.22\n")

	mustWrite(t, filepath.Join(tmpDir, "test", "test.go"), `package test

import "os/exec"

func RequireBinary(name string) {
	_, _ = exec.LookPath(name)
}

func Other() string {
	return "other"
}
`)

	// Destination package pre-exists with its own unrelated content and its
	// own existing import block, mirroring go/test/skip/skip.go in the
	// reported repro - the fix must merge into this block, not append a
	// second one.
	mustWrite(t, filepath.Join(tmpDir, "test", "skip", "skip.go"), `package skip

import (
	"fmt"
	"testing"
)

func Something(t *testing.T) {
	fmt.Println("skip", t.Name())
}
`)

	// The caller package needs a non-test .go file too - a package with only
	// a _test.go file is a degenerate shape the workspace loader itself
	// refuses to parse ("no non-test Go files found in package"), which
	// would mask the bug under test rather than exercise it. Real-world
	// affected packages (e.g. go/services/dataFetcher/repo/psql) always have
	// both.
	mustWrite(t, filepath.Join(tmpDir, "caller", "caller.go"), `package caller

// Marker keeps this package non-empty outside of its _test.go file.
const Marker = true
`)

	// The referencing file is a _test.go, the exact shape that triggered the
	// bug, and it uses a second symbol from the source package so the
	// source import must survive the move.
	mustWrite(t, filepath.Join(tmpDir, "caller", "caller_test.go"), `package caller

import (
	"testing"

	"reprotest/test"
)

func TestUsesBoth(t *testing.T) {
	test.RequireBinary("go")
	if test.Other() == "" {
		t.Fatal("expected non-empty")
	}
}
`)

	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "RequireBinary",
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

	callerPath := filepath.Join(tmpDir, "caller", "caller_test.go")
	callerContent := mustRead(t, callerPath)

	if !strings.Contains(callerContent, `"reprotest/test/skip"`) {
		t.Errorf("expected caller_test.go to import the destination package, got:\n%s", callerContent)
	}

	if !strings.Contains(callerContent, `"reprotest/test"`) {
		t.Errorf("expected caller_test.go to retain the source package import (still uses test.Other), got:\n%s", callerContent)
	}

	if !strings.Contains(callerContent, "skip.RequireBinary(") {
		t.Errorf("expected caller_test.go's call to be rewritten to skip.RequireBinary, got:\n%s", callerContent)
	}

	if !strings.Contains(callerContent, "test.Other()") {
		t.Errorf("expected caller_test.go's unrelated test.Other() call to be left alone, got:\n%s", callerContent)
	}

	// Secondary defect: no "was moved from" banner comment in the moved
	// declaration's new home.
	skipContent := mustRead(t, filepath.Join(tmpDir, "test", "skip", "skip.go"))
	if strings.Contains(skipContent, "was moved from") {
		t.Errorf("expected no generated banner comment above the moved declaration, got:\n%s", skipContent)
	}

	// The acceptance criterion for the primary bug: _test.go files are not
	// compiled by `go build`, only by `go vet`/`go test`, so the missing
	// import was invisible to `go build ./...` and only surfaced here.
	cmd := exec.Command("go", "vet", "./...")

	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture module does not vet clean after the move (this is the actual bug):\n%s", out)
	}
}
