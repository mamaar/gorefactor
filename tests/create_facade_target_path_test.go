package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestCreateFacadeWritesInsideWorkspace covers the same defect as
// TestMovePackageTargetPathForms, in the tool where it was guaranteed to fire
// rather than merely likely to.
//
// create_facade's target is a package that by definition does not exist —
// creating it is the whole operation — so types.ResolvePackagePath could never
// resolve it and always handed the raw string through. Every non-absolute
// invocation of this tool wrote facade.go relative to the MCP server's working
// directory. Unlike move_package nothing was deleted, so the only symptom was
// a facade that silently never appeared.
func TestCreateFacadeWritesInsideWorkspace(t *testing.T) {
	for _, target := range []string{"scratch/api", "api"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "go.mod"), "module scratch\n\ngo 1.22\n")
			mustWrite(t, filepath.Join(dir, "internal", "store", "store.go"), `package store

type Store struct{}

func New() *Store { return &Store{} }
`)

			ctx := context.Background()
			sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
			defer sess.Close()

			result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
				Name: "create_facade",
				Arguments: map[string]any{
					"target_package": target,
					"exports": []map[string]any{
						{"source_package": "scratch/internal/store", "symbol_name": "Store"},
					},
				},
			})
			if err != nil {
				t.Fatalf("CallTool transport error: %v", err)
			}
			if result.IsError {
				t.Fatalf("create_facade returned an error result: %+v", result.Content)
			}

			facade := filepath.Join(dir, "api", "facade.go")
			got, readErr := os.ReadFile(facade)
			if readErr != nil {
				t.Fatalf("facade was not created inside the workspace at %s: %v", facade, readErr)
			}
			if !strings.Contains(string(got), "package api") {
				t.Errorf("facade has the wrong package clause:\n%s", got)
			}
		})
	}
}
