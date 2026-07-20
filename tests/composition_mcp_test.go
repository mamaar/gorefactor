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
// on: an operation can observe what a previous one wrote, which in turn
// requires executePlan's post-apply SyncWorkspaceChanges to have re-parsed the
// files it just wrote. If that sync ever regresses to a no-op, stage 2 here
// analyses stale ASTs and finds nothing - so this test fails loudly rather than
// letting the composition quietly stop composing.
//
// fix_early_returns inverts the guard's condition with srctext.PushNeg, which
// distributes De Morgan on the spot rather than emitting a blunt `!(...)`
// wrapper - so most of this fixture's condition is already simplified by
// stage 1 alone. The one thing PushNeg cannot see through is a double
// negation nested inside an opaque call argument: it treats the call as a
// single unit and puts one `!` in front of it, leaving the `!!` inside
// untouched. That residual `!!` is what stage 2 (fix_redundant_negations)
// still has to clean up, so the composition still does real work even though
// neither fixer is "blunt" anymore.
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

	// Stage 1: flatten. De Morgan already simplifies the negated selector away;
	// only the `!!` buried inside the opaque call argument survives.
	call("fix_early_returns", map[string]any{"apply": true})

	afterFlatten := readFile(t, path)
	if !strings.Contains(afterFlatten, "n.isExternal || !hasCycle(!!visited[pkg])") {
		t.Fatalf("stage 1 did not produce the expected condition:\n%s", afterFlatten)
	}
	if !strings.Contains(afterFlatten, "continue") {
		t.Fatalf("stage 1 did not emit an early continue:\n%s", afterFlatten)
	}

	// Stage 2: clean up the double negation stage 1 left behind. This is the
	// assertion that matters.
	call("fix_redundant_negations", map[string]any{"apply": true})

	afterSimplify := readFile(t, path)
	if !strings.Contains(afterSimplify, "if n.isExternal || !hasCycle(visited[pkg]) {") {
		t.Errorf("stage 2 did not collapse the double negation stage 1 left behind:\n%s", afterSimplify)
	}
	// Scoped to the rewritten function: the fixture's package doc quotes `!!`
	// in prose, so a whole-file scan would match that instead.
	if body := funcBody(afterSimplify, "func detectCycles"); strings.Contains(body, "!!") {
		t.Errorf("the double negation survived the cleanup pass:\n%s", body)
	}
	if !strings.Contains(afterSimplify, "continue") {
		t.Errorf("stage 1's early continue was lost by stage 2:\n%s", afterSimplify)
	}
}
