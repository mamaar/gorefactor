package tests_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixDeepIfElse_RejectsRewriteThatWouldNotTypeCheck covers the type-check
// gate fix_deep_if_else_chains now runs before applying anything.
//
// The fixture's typeErrorChain is fixable by every check deepifelse's own
// analyzer runs (no branch declares a name a later branch reads, and the
// final else's declarations look brand new from where the chain sits), yet
// applying the rewrite would break the build: dedenting the final else's
// `x, y := 5, "z"` into the function's top-level scope turns what was a
// legal shadowing declaration (a fresh, block-scoped `x`) into a
// reassignment of the outer `x string` with an int value, which does not
// type-check. goodChain, alongside it in the same fixture, is an ordinary
// flattenable chain with no such hazard and must still be applied normally.
func TestFixDeepIfElse_RejectsRewriteThatWouldNotTypeCheck(t *testing.T) {
	dir, result := callToolOnFixture(t, "deep_if_else_verify", "fix_deep_if_else_chains", map[string]any{})

	fixed, _ := result["functions_fixed"].([]any)
	if len(fixed) != 1 {
		t.Fatalf("expected exactly 1 applied rewrite, got %d: %v", len(fixed), fixed)
	}

	fn, _ := fixed[0].(string)
	if fn != "goodChain" {
		t.Errorf("functions_fixed = %v, want [goodChain]", fixed)
	}

	unfixable, _ := result["unfixable"].([]any)
	var typeErrorItem map[string]any

	for _, u := range unfixable {
		item := u.(map[string]any)

		fn, _ := item["function_name"].(string)
		if fn == "typeErrorChain" {
			typeErrorItem = item
		}
	}

	if typeErrorItem == nil {
		t.Fatalf("expected typeErrorChain to be reported unfixable after failing type-check verification, got unfixable=%v", unfixable)
	}

	reason, _ := typeErrorItem["reason"].(string)
	if !strings.Contains(reason, "would not type-check") {
		t.Errorf("reason = %q, want it to explain the rewrite would not type-check", reason)
	}

	for _, f := range fixed {
		fn, _ := f.(string)
		if fn == "typeErrorChain" {
			t.Error("typeErrorChain was reported as fixed despite its rewrite failing verification")
		}
	}

	// The file on disk must still compile: typeErrorChain's rewrite must have
	// been left out of what was actually applied, not just mis-reported.
	after := readFile(t, filepath.Join(dir, "main.go"))
	if !strings.Contains(after, `} else if cond2 {`) {
		t.Errorf("typeErrorChain's original else-if chain should have been left untouched on disk, got:\n%s", after)
	}

	if strings.Contains(after, "if x%2 == 0 {\n\t\t\t\treturn fmt.Sprintf(\"even") {
		t.Errorf("goodChain should have been flattened, but its original nested form is still present:\n%s", after)
	}

	// `go vet` rather than `go build`: it type-checks the fixture's test files
	// too, and it does not drop a compiled binary next to the sources.
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture module does not compile after fix_deep_if_else_chains ran:\n%s", out)
	}
}

// TestFixDeepIfElse_DryRunOnlyDiffsSurvivingChanges checks that a dry run
// applies the same verification gate: it must still preview goodChain's
// rewrite, but must not offer typeErrorChain's, since that rewrite would
// never actually be applied.
func TestFixDeepIfElse_DryRunOnlyDiffsSurvivingChanges(t *testing.T) {
	before := readFile(t, filepath.Join("testdata", "deep_if_else_verify", "main.go"))

	dir, result := callToolOnFixture(t, "deep_if_else_verify", "fix_deep_if_else_chains", map[string]any{
		"dry_run": true,
	})

	after := readFile(t, filepath.Join(dir, "main.go"))
	if after != before {
		t.Errorf("dry run modified the file on disk:\n%s", after)
	}

	dryRun, _ := result["dry_run"].(bool)
	if !dryRun {
		t.Error("result should report dry_run=true")
	}

	diffs, _ := result["diffs"].([]any)
	if len(diffs) == 0 {
		t.Fatal("expected the preview to carry a diff for goodChain")
	}

	for _, d := range diffs {
		file := d.(map[string]any)

		hunks, _ := file["hunks"].([]any)
		for _, h := range hunks {
			hunk := h.(map[string]any)

			old, _ := hunk["old"].(string)
			if strings.Contains(old, "typeErrorChain") || strings.Contains(old, "cond1") {
				t.Errorf("dry run should not preview typeErrorChain's rewrite, which fails verification, got hunk:\n%v", hunk)
			}
		}
	}

	unfixable, _ := result["unfixable"].([]any)
	found := false

	for _, u := range unfixable {
		item := u.(map[string]any)

		fn, _ := item["function_name"].(string)
		if fn == "typeErrorChain" {
			found = true
		}
	}

	if !found {
		t.Errorf("expected typeErrorChain in unfixable even on a dry run, got %v", unfixable)
	}
}
