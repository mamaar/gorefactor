package tests_test

import (
	"strings"
	"testing"
)

func previewHunks(t *testing.T, result map[string]any) []map[string]any {
	t.Helper()

	diffs, ok := result["diffs"].([]any)
	if !ok || len(diffs) != 1 {
		t.Fatalf("expected one file diff, got %v", result["diffs"])
	}

	raw, ok := diffs[0].(map[string]any)["hunks"].([]any)
	if !ok {
		t.Fatalf("expected preview hunks, got %v", diffs[0])
	}

	hunks := make([]map[string]any, len(raw))
	for i, hunk := range raw {
		hunks[i] = hunk.(map[string]any)
	}

	return hunks
}

func TestFixBooleanBranching_NestedDryRunTargetsDetectedSite(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_boolean_branching_nested", "fix_boolean_branching", map[string]any{
		"min_branches": 2,
		"dry_run":      true,
	})

	count, _ := result["changes_count"].(float64)
	if count != 3 {
		t.Fatalf("changes_count = %v, want 3: %v", count, result)
	}

	removed, _ := result["booleans_removed"].([]any)
	if len(removed) != 1 || removed[0] != "isRepeated" {
		t.Errorf("booleans_removed = %v, want only isRepeated", removed)
	}

	var switches []string

	for _, hunk := range previewHunks(t, result) {
		next, _ := hunk["new"].(string)
		if strings.HasPrefix(next, "switch matches[1]") {
			switches = append(switches, next)
		}
	}

	if len(switches) != 2 {
		t.Fatalf("expected two independent switch previews, got %d: %v", len(switches), switches)
	}

	for _, next := range switches {
		if !strings.Contains(next, "\n\t\tcase ") || strings.Contains(next, "\n\t\t\t\t\t") {
			t.Errorf("switch preview is not splice-indented:\n%s", next)
		}
	}
}

func TestFixEarlyReturns_DryRunPreservesNestedIndentation(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_early_returns", "fix_early_returns", map[string]any{
		"patterns": []string{"else_after_return"},
		"dry_run":  true,
	})

	hunks := previewHunks(t, result)
	if len(hunks) != 1 {
		t.Fatalf("expected one else-after-return hunk, got %d", len(hunks))
	}

	want := "if x < 0 {\n\t\treturn \"negative\"\n\t}\n\n\tkind := \"non-negative\"\n\treturn kind"

	got, _ := hunks[0]["new"].(string)
	if got != want {
		t.Errorf("early-return preview indentation mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestFixIfInit_DryRunIndentsElseIfWrapper(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_if_init_elseif", "fix_if_init_assignments", map[string]any{
		"dry_run": true,
	})
	var sawHeader, sawClose bool

	for _, hunk := range previewHunks(t, result) {
		next, _ := hunk["new"].(string)
		if strings.HasPrefix(next, "{\n") {
			sawHeader = true

			if !strings.Contains(next, "\n\t\tb :=") && !strings.Contains(next, "\n\t\tc :=") {
				t.Errorf("else-if wrapper header is not nested:\n%s", next)
			}
		}

		if next == "\n\t}" {
			sawClose = true
		}
	}

	if !sawHeader || !sawClose {
		t.Errorf("preview did not contain indented else-if wrapper edits: header=%v close=%v", sawHeader, sawClose)
	}
}
