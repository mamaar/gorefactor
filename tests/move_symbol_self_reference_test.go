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

// TestMoveSymbolDoesNotSelfQualifyAlreadyMovedReferences is a regression
// test for a bug where moving a symbol whose body/signature calls something
// that a *prior* move_symbol call already relocated to the same destination
// package produced a package that imports itself.
//
// Repro shape, taken from a real two-step move in production code:
//
//  1. move_symbol(AsyncServer, common -> pkg/asyncserver) moves the
//     AsyncServer type together with its companion constructor
//     NewAsyncServer (idiomatic constructor via a composite literal — see
//     move_companions.go). NewLoggedAsyncServer is NOT a companion (it
//     delegates to NewAsyncServer and sets a field instead of building
//     AsyncServer via a literal), so it stays behind in package common, and
//     — correctly — its call to NewAsyncServer and its *AsyncServer return
//     type get qualified to asyncserver.NewAsyncServer / *asyncserver.AsyncServer,
//     with an import of the new package added to common.
//
//  2. move_symbol(NewLoggedAsyncServer, common -> pkg/asyncserver) moves
//     that leftover function to the very package its body already
//     qualifies references with. extractSymbolSource copied the qualifier
//     verbatim, so the pasted code read `asyncserver.NewAsyncServer(...)`
//     and `*asyncserver.AsyncServer` from *inside* package asyncserver, and
//     asyncserver.go imported itself — an import cycle ("import cycle not
//     allowed").
//
// Root cause: the moved declaration's source text was pasted as-is with no
// check for whether a qualifier it already carried resolved to the
// destination package. qualifierEdits (pkg/refactor/move_imports.go) strips
// any qualifier of the destination package.
func TestMoveSymbolDoesNotSelfQualifyAlreadyMovedReferences(t *testing.T) {
	tmpDir := t.TempDir()

	mustWrite(t, filepath.Join(tmpDir, "go.mod"), "module reprotest\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(tmpDir, "common", "asyncserver.go"), `package common

import "net/http"

// AsyncServer has an unexported field so its idiomatic constructor
// (NewAsyncServer) is detected as a companion and moves alongside it.
type AsyncServer struct {
	handler http.Handler
	Label   string
}

// NewAsyncServer builds AsyncServer via a composite literal touching the
// unexported field, so it moves as AsyncServer's companion.
func NewAsyncServer(handler http.Handler) *AsyncServer {
	return &AsyncServer{handler: handler}
}

// NewLoggedAsyncServer only delegates to NewAsyncServer and sets an
// exported field, so it is NOT a companion (findCompanionFuncDecls only
// recognizes composite-literal construction) and stays behind in package
// common after AsyncServer's move.
func NewLoggedAsyncServer(handler http.Handler, label string) *AsyncServer {
	server := NewAsyncServer(handler)
	server.Label = label
	return server
}
`)

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
	defer sess.Close()

	// Step 1: move the type (bringing its companion constructor along).
	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "AsyncServer",
			"from_package": "common",
			"to_package":   "pkg/asyncserver",
		},
	})
	if err != nil {
		t.Fatalf("step 1 CallTool transport error: %v", err)
	}
	if result.IsError {
		t.Fatalf("step 1 move_symbol returned an error result: %+v", result.Content)
	}

	targetPath := filepath.Join(tmpDir, "pkg", "asyncserver", "asyncserver.go")
	if !strings.Contains(mustRead(t, targetPath), "func NewAsyncServer(") {
		t.Fatalf("expected NewAsyncServer to move as AsyncServer's companion in step 1, got:\n%s", mustRead(t, targetPath))
	}

	commonPath := filepath.Join(tmpDir, "common", "asyncserver.go")
	afterStep1 := mustRead(t, commonPath)
	if !strings.Contains(afterStep1, "func NewLoggedAsyncServer(") {
		t.Fatalf("expected NewLoggedAsyncServer to stay behind in package common after step 1, got:\n%s", afterStep1)
	}

	// Step 2: move the leftover function whose body/signature already
	// qualifies references to the package it's about to land in.
	result, err = sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "move_symbol",
		Arguments: map[string]any{
			"symbol":       "NewLoggedAsyncServer",
			"from_package": "common",
			"to_package":   "pkg/asyncserver",
		},
	})
	if err != nil {
		t.Fatalf("step 2 CallTool transport error: %v", err)
	}
	if result.IsError {
		t.Fatalf("step 2 move_symbol returned an error result: %+v", result.Content)
	}

	afterStep2 := mustRead(t, targetPath)

	if strings.Contains(afterStep2, "asyncserver.NewAsyncServer") {
		t.Errorf("expected NewLoggedAsyncServer's call to NewAsyncServer to stay unqualified inside package asyncserver, got self-qualified reference:\n%s", afterStep2)
	}
	if strings.Contains(afterStep2, "asyncserver.AsyncServer") {
		t.Errorf("expected NewLoggedAsyncServer's *AsyncServer return type to stay unqualified inside package asyncserver, got self-qualified reference:\n%s", afterStep2)
	}
	if strings.Contains(afterStep2, `"reprotest/pkg/asyncserver"`) {
		t.Errorf("expected package asyncserver to not import itself, got:\n%s", afterStep2)
	}
	if !strings.Contains(afterStep2, "func NewLoggedAsyncServer(") {
		t.Errorf("expected NewLoggedAsyncServer to have moved into %s, got:\n%s", targetPath, afterStep2)
	}

	// The acceptance criterion: the whole fixture module must actually
	// build. Before the fix this failed with "import cycle not allowed".
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture module does not compile after the two moves (this is the actual bug):\n%s", out)
	}
}
