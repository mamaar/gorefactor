package tests_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixSwitchableIfChains_CountsOnlyWhatItRewrote mirrors
// TestFixDeepIfElse_CountsOnlyWhatItRewrote: functions_fixed and
// chains_converted must describe exactly the chains that were actually
// rewritten (httpStatusMessage, classifyMessage, describeValue), not every
// chain the detector saw. resolveKey is detected but refused (a later link
// declares variables in its if header), and gradeScore is never reported at
// all — it is the shape fix_deep_if_else_chains owns.
func TestFixSwitchableIfChains_CountsOnlyWhatItRewrote(t *testing.T) {
	_, result := callToolOnFixture(t, "switch_chains", "fix_switchable_if_chains", map[string]any{})

	fixed, _ := result["functions_fixed"].([]any)
	if len(fixed) != 3 {
		t.Fatalf("expected exactly 3 rewritten functions, got %d: %v", len(fixed), fixed)
	}
	fixedSet := make(map[string]bool, len(fixed))
	for _, f := range fixed {
		fn, _ := f.(string)
		fixedSet[fn] = true
	}
	for _, want := range []string{"httpStatusMessage", "classifyMessage", "describeValue"} {
		if !fixedSet[want] {
			t.Errorf("functions_fixed = %v, want it to include %s", fixed, want)
		}
	}
	if fixedSet["gradeScore"] {
		t.Error("gradeScore is the flattener's chain and must not be reported as fixed")
	}
	if fixedSet["resolveKey"] {
		t.Error("resolveKey's rewrite is refused and must not be reported as fixed")
	}

	converted, _ := result["chains_converted"].(float64)
	if converted != 3 {
		t.Errorf("chains_converted = %v, want 3", converted)
	}
	changes, _ := result["changes_count"].(float64)
	if changes != 3 {
		t.Errorf("changes_count = %v, want 3 (one change per converted chain)", changes)
	}

	unfixable, _ := result["unfixable"].([]any)
	if len(unfixable) != 1 {
		t.Fatalf("expected exactly 1 unfixable finding (resolveKey), got %d: %v", len(unfixable), unfixable)
	}
	item := unfixable[0].(map[string]any)
	fn, _ := item["function_name"].(string)
	if fn != "resolveKey" {
		t.Errorf("unfixable finding = %q, want resolveKey", fn)
	}
}

// TestDetectSwitchableIfChains_ReportsKinds checks that the detector classifies
// each chain by the switch form it maps onto, and that the chain
// fix_deep_if_else_chains already owns (gradeScore) is not reported at all —
// not even as unfixable, since flattenerHandlesIt keeps the two fixers from
// ever reporting the same chain.
func TestDetectSwitchableIfChains_ReportsKinds(t *testing.T) {
	_, result := callToolOnFixture(t, "switch_chains", "detect_switchable_if_chains", map[string]any{})

	violations, _ := result["violations"].([]any)
	if len(violations) != 4 {
		t.Fatalf("expected 4 detected chains, got %d: %v", len(violations), violations)
	}

	kinds := make(map[string]string, len(violations))
	fixable := make(map[string]bool, len(violations))
	for _, v := range violations {
		item := v.(map[string]any)
		fn, _ := item["function_name"].(string)
		kind, _ := item["kind"].(string)
		f, _ := item["fixable"].(bool)
		kinds[fn] = kind
		fixable[fn] = f
	}

	wantKinds := map[string]string{
		"httpStatusMessage": "tagged",
		"classifyMessage":   "tagless",
		"describeValue":     "type_switch",
	}
	for fn, wantKind := range wantKinds {
		if kinds[fn] != wantKind {
			t.Errorf("kind[%s] = %q, want %q", fn, kinds[fn], wantKind)
		}
		if !fixable[fn] {
			t.Errorf("fixable[%s] = false, want true", fn)
		}
	}

	_, found := kinds["resolveKey"]
	if !found {
		t.Error("resolveKey should still be detected (and reported unfixable), just not applied")
	}
	if fixable["resolveKey"] {
		t.Error("resolveKey should be reported as not fixable")
	}

	_, found = kinds["gradeScore"]
	if found {
		t.Error("gradeScore is the flattener's chain and must not appear in violations at all")
	}
}

// TestFixSwitchableIfChains_DryRunLeavesFilesAlone mirrors
// TestFixDeepIfElse_DryRunLeavesFilesAlone.
func TestFixSwitchableIfChains_DryRunLeavesFilesAlone(t *testing.T) {
	before := readFile(t, filepath.Join("testdata", "switch_chains", "main.go"))

	dir, result := callToolOnFixture(t, "switch_chains", "fix_switchable_if_chains", map[string]any{
		"dry_run": true,
	})

	after := readFile(t, filepath.Join(dir, "main.go"))
	if after != before {
		t.Errorf("dry run modified the file on disk:\n%s", after)
	}
	dryRun, _ := result["dry_run"].(bool)
	if !dryRun {
		t.Error("result should report dry_run=true so the caller knows nothing was written")
	}
	diffs, _ := result["diffs"].([]any)
	if len(diffs) == 0 {
		t.Error("a dry run should return the diffs it would apply")
	}
}

// TestFixSwitchableIfChains_KindsFilter checks that restricting kinds to
// "tagged" converts only httpStatusMessage, leaving the tagless and
// type-switch chains untouched even though they would otherwise convert.
func TestFixSwitchableIfChains_KindsFilter(t *testing.T) {
	dir, result := callToolOnFixture(t, "switch_chains", "fix_switchable_if_chains", map[string]any{
		"kinds": []string{"tagged"},
	})

	fixed, _ := result["functions_fixed"].([]any)
	if len(fixed) != 1 {
		t.Fatalf("expected exactly 1 rewritten function, got %d: %v", len(fixed), fixed)
	}
	fn, _ := fixed[0].(string)
	if fn != "httpStatusMessage" {
		t.Errorf("functions_fixed = %v, want [httpStatusMessage]", fixed)
	}
	converted, _ := result["chains_converted"].(float64)
	if converted != 1 {
		t.Errorf("chains_converted = %v, want 1", converted)
	}

	after := readFile(t, filepath.Join(dir, "main.go"))
	if !strings.Contains(after, `switch code {`) {
		t.Errorf("httpStatusMessage should have been converted to a switch, got:\n%s", after)
	}
	if !strings.Contains(after, `if strings.HasPrefix(s, "ERROR") {`) {
		t.Errorf("classifyMessage is tagless and should have been left as an if-chain with kinds=[tagged], got:\n%s", after)
	}
	if !strings.Contains(after, `if v, ok := x.(int); ok {`) {
		t.Errorf("describeValue is a type switch and should have been left as an if-chain with kinds=[tagged], got:\n%s", after)
	}
}

// TestFixSwitchableIfChains_RefusalsAreReported checks that resolveKey's
// later-link init is reported in unfixable with a reason naming
// fix_if_init_assignments, rather than being silently dropped.
func TestFixSwitchableIfChains_RefusalsAreReported(t *testing.T) {
	_, result := callToolOnFixture(t, "switch_chains", "fix_switchable_if_chains", map[string]any{})

	unfixable, _ := result["unfixable"].([]any)
	var item map[string]any
	for _, u := range unfixable {
		candidate := u.(map[string]any)
		fn, _ := candidate["function_name"].(string)
		if fn == "resolveKey" {
			item = candidate
		}
	}
	if item == nil {
		t.Fatalf("expected resolveKey to be reported unfixable, got unfixable=%v", unfixable)
	}
	reason, _ := item["reason"].(string)
	if !strings.Contains(reason, "fix_if_init_assignments") {
		t.Errorf("reason = %q, want it to name fix_if_init_assignments", reason)
	}
}

// TestFixSwitchableIfChains_ModuleStillCompiles is the single most valuable
// test in this file: the golden suite runs with SkipCompilation, so the
// serializer will happily write a switch statement that does not compile. Only
// an actual build catches that.
func TestFixSwitchableIfChains_ModuleStillCompiles(t *testing.T) {
	dir, _ := callToolOnFixture(t, "switch_chains", "fix_switchable_if_chains", map[string]any{})

	buildCmd := exec.Command("go", "build", "./...")
	buildCmd.Dir = dir
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./... failed after fix_switchable_if_chains:\n%s", out)
	}

	vetCmd := exec.Command("go", "vet", "./...")
	vetCmd.Dir = dir
	out, err = vetCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go vet ./... failed after fix_switchable_if_chains:\n%s", out)
	}
}
