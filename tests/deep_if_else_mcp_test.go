package tests_test

import (
	"path/filepath"
	"testing"
)

// TestFixDeepIfElse_CountsOnlyWhatItRewrote covers the counting bug: the tool
// used to list every detected function in functions_fixed and derive
// early_returns from nesting depth, so it claimed credit for chains it never
// touched. The fixture holds one rewritable chain, one detected-but-unfixable
// chain, and one below the depth threshold.
func TestFixDeepIfElse_CountsOnlyWhatItRewrote(t *testing.T) {
	_, result := callToolOnFixture(t, "fix_deep_if_else", "fix_deep_if_else_chains", map[string]any{})

	fixed, _ := result["functions_fixed"].([]any)
	if len(fixed) != 1 {
		t.Fatalf("expected exactly 1 rewritten function, got %d: %v", len(fixed), fixed)
	}
	if fn, _ := fixed[0].(string); fn != "process" {
		t.Errorf("functions_fixed = %v, want [process]", fixed)
	}

	// process yields three guards, one per else branch. The old code summed
	// NestingDepth over every finding including the unfixable one, so on this
	// fixture it reported 6 early returns across 2 functions.
	if got, _ := result["early_returns"].(float64); got != 3 {
		t.Errorf("early_returns = %v, want 3", got)
	}

	unfixable, _ := result["unfixable"].([]any)
	if len(unfixable) != 1 {
		t.Fatalf("expected categorize to be reported unfixable, got %v", unfixable)
	}
	item := unfixable[0].(map[string]any)
	if fn, _ := item["function_name"].(string); fn != "categorize" {
		t.Errorf("unfixable = %q, want categorize", fn)
	}
	if reason, _ := item["reason"].(string); reason == "" {
		t.Error("an unfixable finding must explain why")
	}

	for _, f := range fixed {
		if fn, _ := f.(string); fn == "categorize" {
			t.Error("categorize was reported as fixed despite being left untouched")
		}
	}
}

// The detector reports chains the fixer cannot rewrite, so callers need the
// fixable flag to tell which findings need flattening by hand.
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
	if fixable["categorize"] {
		t.Error("categorize is an else-if chain and should be marked unfixable")
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
