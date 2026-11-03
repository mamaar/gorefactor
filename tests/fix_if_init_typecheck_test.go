package tests_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// The goto fixture is a rewrite the analyzer has no rule against but the
// compiler rejects, so only the type-check gate can stop it.
func TestFixIfInit_DryRunReportsTypeErrorAsBlockingIssue(t *testing.T) {
	dir, result := callToolOnFixture(t, "fix_if_init_goto", "fix_if_init_assignments", map[string]any{"dry_run": true})

	issue, _ := result["blocking_issue"].(string)
	if !strings.Contains(issue, "jumps over variable declaration") {
		t.Errorf("blocking_issue = %q, want the goto type error", issue)
	}

	diffs, _ := result["diffs"].([]any)
	if len(diffs) == 0 {
		t.Error("a blocked dry run should still carry the diffs")
	}

	got := readFile(t, filepath.Join(dir, "main.go"))
	if got != readFile(t, filepath.Join("testdata", "fix_if_init_goto", "main.go")) {
		t.Errorf("dry run modified the file:\n%s", got)
	}
}

func TestFixIfInit_ApplyRefusesPlanThatWouldNotTypeCheck(t *testing.T) {
	dir := copyFixture(t, "fix_if_init_goto")
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "fix_if_init_assignments", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}

	if !result.IsError || !strings.Contains(errText(result), "jumps over variable declaration") {
		t.Fatalf("expected the apply refused with the type error, got IsError=%v: %s", result.IsError, errText(result))
	}

	got := readFile(t, filepath.Join(dir, "main.go"))
	if got != readFile(t, filepath.Join("testdata", "fix_if_init_goto", "main.go")) {
		t.Errorf("refused apply modified the file:\n%s", got)
	}
}
