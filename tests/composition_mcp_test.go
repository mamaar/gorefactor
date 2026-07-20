package tests_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// funcBody returns src from the declaration starting with decl up to the next
// top-level declaration, so assertions can target rewritten code rather than
// surrounding prose.
func funcBody(src, decl string) string {
	start := strings.Index(src, decl)
	if start < 0 {
		return ""
	}
	rest := src[start:]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestComposeFlattenThenSimplify pins the guarantee the operation design rests
// on: each fixer stays deliberately blunt, and a later pass cleans up after it.
// fix_early_returns emits `!(...)` wrappers on purpose rather than duplicating
// negation logic into every fixer that inverts a condition.
//
// That only works if an operation observes the previous one's output, which in
// turn requires executePlan's post-apply SyncWorkspaceChanges to have re-parsed
// the files it just wrote. If that sync ever regresses to a no-op, stage 2 here
// analyses stale ASTs and finds nothing - so this test fails loudly rather than
// letting the composition quietly stop composing.
func TestComposeFlattenThenSimplify(t *testing.T) {
	dir := copyFixture(t, "compose_flatten_then_simplify")
	path := filepath.Join(dir, "main.go")

	ctx := context.Background()
	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool(%s): %v", tool, err)
		}
		if res.IsError {
			t.Fatalf("CallTool(%s) returned error: %s", tool, errText(res))
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(errText(res)), &decoded); err != nil {
			t.Fatalf("decoding %s result: %v", tool, err)
		}
		return decoded
	}

	// Stage 1: flatten. The happy path dedents, at the cost of a blunt wrapper.
	call("fix_early_returns", map[string]any{"apply": true})

	afterFlatten := readFile(t, path)
	if !strings.Contains(afterFlatten, "!(!n.isExternal && !visited[pkg])") {
		t.Fatalf("stage 1 did not produce the expected wrapper:\n%s", afterFlatten)
	}
	if !strings.Contains(afterFlatten, "continue") {
		t.Fatalf("stage 1 did not emit an early continue:\n%s", afterFlatten)
	}

	// Stage 2: clean up what stage 1 wrote. This is the assertion that matters.
	call("fix_redundant_negations", map[string]any{"apply": true})

	afterSimplify := readFile(t, path)
	if !strings.Contains(afterSimplify, "if n.isExternal || visited[pkg] {") {
		t.Errorf("stage 2 did not collapse the wrapper stage 1 emitted:\n%s", afterSimplify)
	}
	// Scoped to the rewritten function: the fixture's package doc quotes the
	// `!(!a && !b)` shape verbatim, so a whole-file scan would match the prose.
	if body := funcBody(afterSimplify, "func detectCycles"); strings.Contains(body, "!(") {
		t.Errorf("a negation wrapper survived the cleanup pass:\n%s", body)
	}
	if !strings.Contains(afterSimplify, "continue") {
		t.Errorf("stage 1's early continue was lost by stage 2:\n%s", afterSimplify)
	}
}
