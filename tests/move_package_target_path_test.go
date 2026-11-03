package tests_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestMovePackageTargetPathForms is the regression test for move_package
// writing its destination files outside the workspace.
//
// Root cause: the MCP handler resolved the destination with
// types.ResolvePackagePath, which only ever looks up packages that ALREADY
// exist — by workspace key, by root-relative key, through ImportToPath, or by
// unique package name. A move destination does not exist yet, so every
// strategy missed and the function returned the caller's string verbatim.
// MovePackageOperation.Execute then used that unresolved string as a
// filesystem path: filepath.Join(target, base) produced a relative path, and
// the serializer's MkdirAll/WriteFile resolved it against the MCP server
// process's working directory. The write succeeded, so the tool reported
// success while the destination package was never created where it was asked
// for — and the source files had already been deleted.
//
// The import-path form is what was reported in the wild, and it was the least
// broken of the three: because the user happened to type the import path,
// packagePathToImportPath's own error fallback (return the input verbatim)
// made the importer rewrites accidentally correct. The workspace-relative form
// was worse — importers were rewritten to a garbage import path with no module
// prefix at all. Both forms are asserted here.
func TestMovePackageTargetPathForms(t *testing.T) {
	cases := []struct {
		name string
		// target is built from the workspace dir, since the absolute form
		// needs it.
		target func(dir string) string
	}{
		{
			name:   "module import path",
			target: func(string) string { return "scratch/pkg/wms" },
		},
		{
			name:   "workspace relative path",
			target: func(string) string { return "pkg/wms" },
		},
		{
			name:   "absolute path",
			target: func(dir string) string { return filepath.Join(dir, "pkg", "wms") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMovePackageFixture(t)

			ctx := context.Background()

			sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
			defer sess.Close()

			result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
				Name: "move_package",
				Arguments: map[string]any{
					"source_package": "kartadm/wms",
					"target_package": tc.target(dir),
				},
			})
			if err != nil {
				t.Fatalf("CallTool transport error: %v", err)
			}

			if result.IsError {
				t.Fatalf("move_package returned an error result: %+v", result.Content)
			}

			// The destination must exist at the workspace-rooted location, for
			// every input form.
			for _, name := range []string{"wms.go", "wms_test.go"} {
				got := mustRead(t, filepath.Join(dir, "pkg", "wms", name))
				if !strings.Contains(got, "package wms") {
					t.Errorf("%s has the wrong package clause:\n%s", name, got)
				}
			}

			// The sources must be gone, not merely emptied.
			if _, err := os.Stat(filepath.Join(dir, "kartadm", "wms", "wms.go")); !os.IsNotExist(err) {
				t.Errorf("source kartadm/wms/wms.go should be deleted, stat err=%v", err)
			}

			// Both importers must reference the real module import path. This
			// is the assertion the workspace-relative form used to fail: it
			// rewrote them to a bare "pkg/wms".
			for _, importer := range []string{
				filepath.Join(dir, "app", "app.go"),
				filepath.Join(dir, "cmd", "main.go"),
			} {
				got := mustRead(t, importer)
				if !strings.Contains(got, `"scratch/pkg/wms"`) {
					t.Errorf("%s does not import the moved package by its module path:\n%s", importer, got)
				}
			}

			cmd := exec.Command("go", "build", "./...")

			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("workspace does not build after the move (this is the reported bug):\n%s", out)
			}
		})
	}
}

// TestMovePackageRefusesExistingTarget pins the chosen behaviour for a
// destination that is already a package: refuse. Merging two packages
// implicitly would let same-named files overwrite each other and leave
// duplicate declarations to be discovered at build time.
func TestMovePackageRefusesExistingTarget(t *testing.T) {
	dir := writeMovePackageFixture(t)
	mustWrite(t, filepath.Join(dir, "pkg", "wms", "existing.go"), `package wms

func Existing() {}
`)

	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_package",
		Arguments: map[string]any{
			"source_package": "kartadm/wms",
			"target_package": "scratch/pkg/wms",
		},
	})
	if err != nil {
		t.Fatalf("CallTool transport error: %v", err)
	}

	if !result.IsError {
		t.Fatalf("expected move_package to refuse an existing target, got success: %+v", result.Content)
	}

	// Refusing must mean nothing happened: the sources are still there and the
	// existing target package is untouched.
	if _, err := os.Stat(filepath.Join(dir, "kartadm", "wms", "wms.go")); err != nil {
		t.Errorf("source file should be untouched after a refusal: %v", err)
	}

	got := mustRead(t, filepath.Join(dir, "pkg", "wms", "existing.go"))
	if !strings.Contains(got, "func Existing()") {
		t.Errorf("existing target package was modified by a refused move:\n%s", got)
	}
}

// writeMovePackageFixture builds a module with a package to move and two
// importers in different packages, mirroring the reported case.
func writeMovePackageFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")

	mustWrite(t, filepath.Join(dir, "kartadm", "wms", "wms.go"), `package wms

func Capabilities() string { return "wms" }
`)
	mustWrite(t, filepath.Join(dir, "kartadm", "wms", "wms_test.go"), `package wms

import "testing"

func TestCapabilities(t *testing.T) {
	if Capabilities() != "wms" {
		t.Fatal("bad")
	}
}
`)

	mustWrite(t, filepath.Join(dir, "app", "app.go"), `package app

import "scratch/kartadm/wms"

func Serve() string { return wms.Capabilities() }
`)
	mustWrite(t, filepath.Join(dir, "cmd", "main.go"), `package main

import (
	"fmt"

	"scratch/kartadm/wms"
)

func main() { fmt.Println(wms.Capabilities()) }
`)

	return dir
}

// TestMovePackagePlanPathsAreWorkspaceRooted asserts the invariant directly on
// the generated plan, rather than inferring it from where files landed. This
// is the cheap, deterministic check that would have caught the original bug:
// every path a refactor plans to touch must be absolute and inside the
// workspace.
func TestMovePackagePlanPathsAreWorkspaceRooted(t *testing.T) {
	for _, target := range []string{"scratch/pkg/wms", "pkg/wms"} {
		t.Run(target, func(t *testing.T) {
			dir := writeMovePackageFixture(t)
			eng := createEngine(t)
			ws := loadWorkspace(t, eng, dir)

			plan, err := eng.MovePackage(ws, types.MovePackageRequest{
				SourcePackage: "kartadm/wms",
				TargetPackage: target,
			})
			if err != nil {
				t.Fatalf("MovePackage: %v", err)
			}

			if len(plan.Changes) == 0 {
				t.Fatal("plan has no changes")
			}

			for _, change := range plan.Changes {
				if !filepath.IsAbs(change.File) {
					t.Errorf("change path is not absolute: %q (%s)", change.File, change.Description)
					continue
				}

				rel, err := filepath.Rel(ws.RootPath, change.File)
				if err != nil || strings.HasPrefix(rel, "..") {
					t.Errorf("change path escapes the workspace root %s: %q (%s)",
						ws.RootPath, change.File, change.Description)
				}
			}
		})
	}
}
