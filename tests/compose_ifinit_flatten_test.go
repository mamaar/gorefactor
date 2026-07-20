package tests_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// TestComposeIfInitThenFlatten pins the supported route from a deeply nested
// chain to flat guard clauses: fix_if_init_assignments first, then
// fix_deep_if_else_chains.
//
// The order is not incidental. fix_deep_if_else_chains deliberately refuses to
// hoist an `if init; cond` header - dropping the declaration would break the
// code, and hoisting it safely (including downgrading a colliding `:=` to `=`)
// is work package ifinit already does properly. So a header that declares
// variables can be elided but never inverted, and inversion is the move that
// removes a level of nesting. Hoist first and the same chain goes all the way
// down; hoist never and it stops one level short.
//
// Both halves are asserted here, because the interesting claim is the
// difference between them.
func TestComposeIfInitThenFlatten(t *testing.T) {
	ctx := context.Background()

	flatten := func(t *testing.T, hoistFirst bool) (string, map[string]any) {
		t.Helper()

		dir := copyFixture(t, "compose_ifinit_then_flatten")
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

		if hoistFirst {
			call("fix_if_init_assignments", map[string]any{})
		}
		result := call("fix_deep_if_else_chains", map[string]any{})

		// The MCP server runs with SkipCompilation, and the serializer writes
		// its output even when gofmt rejects it, so a green assertion on the
		// text proves nothing about the code being valid Go. Build it.
		mustBuild(t, dir)

		return readFile(t, filepath.Join(dir, "main.go")), result
	}

	t.Run("hoist first reaches depth 1", func(t *testing.T) {
		src, _ := flatten(t, true)
		body := funcBody(src, "func findTypeString")

		// Every branch is now a sibling guard clause: seven statements in a
		// row, none of them nested inside another.
		want := []string{
			"if len(c.Type) > 0 {",
			"if len(c.TypeField) <= 0 {",
			"typeKey, ok := attributes[c.TypeField]",
			"if !ok {",
			"typeValue, ok := c.TypeMapping[typeKey]",
			"if ok {",
			"if slices.Contains(c.IgnoreTypes, typeKey) {",
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("flattened function is missing %q:\n%s", w, body)
			}
		}

		if got := maxIndent(body); got > 2 {
			t.Errorf("expected every statement at one level of indentation, got %d:\n%s", got-1, body)
		}
		if strings.Contains(body, "else") {
			t.Errorf("no else should survive the flattening:\n%s", body)
		}
	})

	t.Run("without hoisting it stops one level short", func(t *testing.T) {
		src, result := flatten(t, false)
		body := funcBody(src, "func findTypeString")

		// The headers keep their declarations, so those two links could only be
		// elided, not inverted - one level of nesting stays.
		if !strings.Contains(body, "if typeKey, ok := attributes[c.TypeField]; ok {") {
			t.Errorf("the if-init header should have been kept intact:\n%s", body)
		}
		if strings.Contains(body, "else") {
			t.Errorf("the elses should still have been dropped:\n%s", body)
		}

		// And the tool must say why it stopped, naming the tool that gets the
		// caller the rest of the way. This is the whole point of reporting a
		// reason on a finding that *was* fixed.
		if !strings.Contains(reasonsIn(result), "fix_if_init_assignments") {
			t.Errorf("expected a reason pointing at fix_if_init_assignments, got: %s", reasonsIn(result))
		}
	})
}

// mustBuild compiles the rewritten fixture, which is the only check in this
// test that a rewrite producing plausible-looking text actually produces a
// program. `go vet` rather than `go build` so the fixture's own test files, if
// it ever grows any, are type-checked too.
func mustBuild(t *testing.T, dir string) {
	t.Helper()

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the rewritten fixture does not build:\n%s", out)
	}
}

// reasonsIn collects every reason string the fix tool reported, across whichever
// lists it used to report them.
func reasonsIn(result map[string]any) string {
	var reasons []string
	for _, key := range []string{"partial", "unfixable"} {
		items, _ := result[key].([]any)
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if reason, _ := m["reason"].(string); reason != "" {
				reasons = append(reasons, reason)
			}
		}
	}
	return strings.Join(reasons, " | ")
}

// maxIndent returns the deepest leading-tab count in src, which is how this
// test measures nesting without re-parsing.
func maxIndent(src string) int {
	deepest := 0
	for _, line := range strings.Split(src, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		tabs := len(line) - len(strings.TrimLeft(line, "\t"))
		if tabs > deepest {
			deepest = tabs
		}
	}
	return deepest
}
