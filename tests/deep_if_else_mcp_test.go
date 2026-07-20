package tests_test

import (
	"path/filepath"
	"testing"
)

// TestFixDeepIfElse_CountsOnlyWhatItRewrote covers the counting bug: the tool
// used to list every detected function in functions_fixed and derive
// early_returns from nesting depth, so it claimed credit for chains it never
// touched. The fixture's chains (process, an else-if chain in categorize, and
// one below the depth threshold in describe) still exercise that distinction
// even though the rewriter now flattens else-if chains too: process and
// categorize both get applied, describe is never detected at all, and
// functions_fixed/early_returns must reflect exactly the two that were
// actually rewritten - not an estimate derived from nesting depth.
func TestFixDeepIfElse_CountsOnlyWhatItRewrote(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_deep_if_else", "fix_deep_if_else_chains", map[string]any{})

	fixed, _ := result["functions_fixed"].([]any)
	if len(fixed) != 2 {
		t.Fatalf("expected exactly 2 rewritten functions, got %d: %v", len(fixed), fixed)
	}
	fixedSet := make(map[string]bool, len(fixed))
	for _, f := range fixed {
		fn, _ := f.(string)
		fixedSet[fn] = true
	}
	if !fixedSet["process"] || !fixedSet["categorize"] {
		t.Errorf("functions_fixed = %v, want [process categorize]", fixed)
	}
	if fixedSet["describe"] {
		t.Error("describe is below the depth threshold and must not be reported as fixed")
	}

	// process yields three guards, one per else branch; categorize yields four
	// (its else-if chain elides three links and terminates in a return, no
	// leftover reason). The old code summed NestingDepth over every finding
	// including the one it declined to touch, so on this fixture it reported
	// 6 early returns across 2 functions by coincidence rather than by
	// counting what the rewrite actually emitted.
	if got, _ := result["early_returns"].(float64); got != 7 {
		t.Errorf("early_returns = %v, want 7 (3 from process + 4 from categorize)", got)
	}

	if unfixable, _ := result["unfixable"].([]any); len(unfixable) != 0 {
		t.Errorf("expected no unfixable findings on this fixture, got %v", unfixable)
	}
	if partial, _ := result["partial"].([]any); len(partial) != 0 {
		t.Errorf("expected no partial findings on this fixture (both chains flatten fully), got %v", partial)
	}
}

// The detector reports every chain past the depth threshold, fixable or not,
// so callers need the fixable flag to tell which findings need flattening by
// hand. Both chains in this fixture are fixable now that else-if chains
// flatten too; describe stays the control that keeps the depth threshold
// honest.
func TestDetectDeepIfElse_MarksUnfixableChains(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_deep_if_else", "detect_deep_if_else_chains", map[string]any{})

	violations, _ := result["violations"].([]any)
	if len(violations) != 2 {
		t.Fatalf("expected 2 detected chains (process, categorize), got %d: %v", len(violations), violations)
	}

	fixable := make(map[string]bool)
	for _, v := range violations {
		item := v.(map[string]any)
		fn, _ := item["function_name"].(string)
		f, _ := item["fixable"].(bool)
		fixable[fn] = f
	}

	if !fixable["process"] {
		t.Error("process is a flattenable chain and should be marked fixable")
	}
	if !fixable["categorize"] {
		t.Error("categorize's else-if chain flattens fully too and should be marked fixable")
	}
	// describe nests only two deep, below the default threshold.
	if _, found := fixable["describe"]; found {
		t.Error("describe is below the depth threshold and should not be reported at all")
	}
}

// dry_run must say so rather than returning an empty files_modified that reads
// like an apply that changed nothing.
func TestFixDeepIfElse_DryRunLeavesFilesAlone(t *testing.T) {
	before := readFile(t, filepath.Join("testdata", "fix_deep_if_else", "main.go"))

	dir, result := callToolOnFixture(t, "fix_deep_if_else", "fix_deep_if_else_chains", map[string]any{
		"dry_run": true,
	})

	if after := readFile(t, filepath.Join(dir, "main.go")); after != before {
		t.Errorf("dry run modified the file on disk:\n%s", after)
	}
	if dryRun, _ := result["dry_run"].(bool); !dryRun {
		t.Error("result should report dry_run=true so the caller knows nothing was written")
	}
	if diffs, _ := result["diffs"].([]any); len(diffs) == 0 {
		t.Error("a dry run should return the diffs it would apply")
	}
}
