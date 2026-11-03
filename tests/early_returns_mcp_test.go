package tests_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// callToolOnFixture dials a session against a copy of the named fixture,
// invokes tool with args, and decodes the JSON result.
func callToolOnFixture(t *testing.T, fixture, tool string, args map[string]any) (string, map[string]any) {
	t.Helper()

	dir := copyFixture(t, fixture)

	ctx := t.Context()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}

	if result.IsError {
		t.Fatalf("CallTool(%s) returned error: %s", tool, errText(result))
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(errText(result)), &decoded); err != nil {
		t.Fatalf("decoding %s result: %v\n%s", tool, err, errText(result))
	}

	return dir, decoded
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

// TestFixEarlyReturns_PreviewsOnDryRun is the guarantee behind dry-run-first:
// a call with dry_run set must leave every byte on disk untouched while still
// returning the diff the caller is meant to review.
func TestFixEarlyReturns_PreviewsOnDryRun(t *testing.T) {
	before := readFile(t, filepath.Join("testdata", "fix_early_returns", "main.go"))

	dir, result := callToolOnFixture(t, "fix_early_returns", "fix_early_returns", map[string]any{"dry_run": true})

	after := readFile(t, filepath.Join(dir, "main.go"))
	if after != before {
		t.Errorf("preview mode modified the file on disk:\n%s", after)
	}

	applied, _ := result["applied"].(bool)
	if applied {
		t.Error("applied should be false when dry_run is set")
	}

	blocking, ok := result["blocking_issue"]
	if ok {
		t.Errorf("preview reported a blocking issue: %v", blocking)
	}

	diffs, ok := result["diffs"].([]any)
	if !ok || len(diffs) == 0 {
		t.Fatalf("expected the preview to carry diffs, got %v", result["diffs"])
	}

	// The diff must show both sides, so the reviewer can see what is being
	// replaced rather than only what it becomes.
	file := diffs[0].(map[string]any)

	hunks, ok := file["hunks"].([]any)
	if !ok || len(hunks) == 0 {
		t.Fatalf("expected hunks in the diff, got %v", file["hunks"])
	}

	for i, h := range hunks {
		hunk := h.(map[string]any)

		old, _ := hunk["old"].(string)
		if old == "" {
			t.Errorf("hunk %d has no before-text; the preview cannot be reviewed", i)
		}

		next, _ := hunk["new"].(string)
		if next == "" {
			t.Errorf("hunk %d has no after-text", i)
		}
	}
}

// TestFixEarlyReturns_ReportsUnfixableSeparately checks that the tool does not
// claim credit for findings it could not rewrite. See
// TestFixDeepIfElse_CountsOnlyWhatItRewrote for the same guarantee on the
// deep-chain fixer.
func TestFixEarlyReturns_ReportsUnfixableSeparately(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_early_returns", "fix_early_returns", map[string]any{"dry_run": true})

	fixed, _ := result["fixed"].([]any)
	if len(fixed) != 3 {
		t.Errorf("expected 3 rewritten findings, got %d: %v", len(fixed), fixed)
	}

	unfixable, _ := result["unfixable"].([]any)
	if len(unfixable) != 1 {
		t.Fatalf("expected the redeclaration case to be reported unfixable, got %v", unfixable)
	}

	item := unfixable[0].(map[string]any)

	fn, _ := item["function_name"].(string)
	if fn != "clamp" {
		t.Errorf("unfixable finding = %q, want clamp", fn)
	}

	reason, _ := item["reason"].(string)
	if reason == "" {
		t.Error("an unfixable finding must explain why")
	}

	for _, f := range fixed {
		fn, _ := f.(map[string]any)["function_name"].(string)
		if fn == "clamp" {
			t.Error("clamp was reported as fixed despite being unfixable")
		}
	}
}

func TestDetectEarlyReturns_ReportsEveryPattern(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_early_returns", "detect_early_returns", map[string]any{})

	violations, _ := result["violations"].([]any)
	if len(violations) != 4 {
		t.Fatalf("expected 4 findings, got %d: %v", len(violations), violations)
	}

	// Keyed by pattern+function: guard_clause legitimately matches twice, once
	// rewritably (report) and once not (clamp).
	seen := make(map[string]bool)

	for _, v := range violations {
		item := v.(map[string]any)
		pattern, _ := item["pattern"].(string)
		fn, _ := item["function_name"].(string)
		seen[pattern+"/"+fn] = true
	}

	for _, want := range []string{
		"guard_clause/report",
		"guard_clause/clamp",
		"loop_continue/summarize",
		"else_after_return/classify",
	} {
		if !seen[want] {
			t.Errorf("missing finding %s; got %v", want, seen)
		}
	}

	fixable, _ := result["fixable_count"].(float64)
	if fixable != 3 {
		t.Errorf("fixable_count = %v, want 3", fixable)
	}
}

func TestDetectEarlyReturns_PatternFilter(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_early_returns", "detect_early_returns", map[string]any{
		"patterns": []string{"loop_continue"},
	})

	violations, _ := result["violations"].([]any)
	if len(violations) != 1 {
		t.Fatalf("expected only the loop_continue finding, got %v", violations)
	}

	pattern, _ := violations[0].(map[string]any)["pattern"].(string)
	if pattern != "loop_continue" {
		t.Errorf("pattern = %q, want loop_continue", pattern)
	}
}

func TestDetectEarlyReturns_RejectsUnknownPattern(t *testing.T) {
	dir := copyFixture(t, "fix_early_returns")

	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "detect_early_returns",
		Arguments: map[string]any{"patterns": []string{"nonsense"}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if !result.IsError {
		t.Error("expected an unknown pattern name to be rejected rather than ignored")
	}
}
