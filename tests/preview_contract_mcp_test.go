package tests_test

import (
	"path/filepath"
	"testing"
)

// TestDryRunPreviewsCarryBothSides pins the dry-run contract that every
// mutating tool is documented to honour: a preview returns the full,
// untruncated before AND after text of every planned change.
//
// The tools below build their changes from analyzer suggested fixes, which
// only ever carry the replacement text. Each handler was individually
// responsible for backfilling the "before" side, and several silently did not
// — so their previews rendered a one-sided diff that could not actually be
// reviewed, while the tools that did remember looked fine. The backfill now
// happens once, centrally, in previewPlan; this test is what keeps a new tool
// from reintroducing the gap.
//
// fix_early_returns is covered separately by
// TestFixEarlyReturns_PreviewsOnDryRun. It was already correct and is
// included here only so the contract is asserted uniformly across the family.
func TestDryRunPreviewsCarryBothSides(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		tool    string
		args    map[string]any
	}{
		{
			name: "fix_if_init_assignments", fixture: "fix_if_init",
			tool: "fix_if_init_assignments", args: map[string]any{"dry_run": true},
		},
		{
			name: "fix_boolean_branching", fixture: "fix_boolean_branching",
			tool: "fix_boolean_branching", args: map[string]any{"dry_run": true},
		},
		{
			name: "fix_error_wrapping", fixture: "fix_error_wrapping",
			tool: "fix_error_wrapping", args: map[string]any{"dry_run": true},
		},
		{
			name: "fix_early_returns", fixture: "fix_early_returns",
			tool: "fix_early_returns", args: map[string]any{"dry_run": true},
		},
		{
			name: "fix_deep_if_else_chains", fixture: "fix_deep_if_else",
			tool: "fix_deep_if_else_chains", args: map[string]any{"dry_run": true},
		},
		{
			name: "fix_switchable_if_chains", fixture: "switch_chains",
			tool: "fix_switchable_if_chains", args: map[string]any{"dry_run": true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := readFile(t, filepath.Join("testdata", tc.fixture, "main.go"))

			dir, result := callToolOnFixture(t, tc.fixture, tc.tool, tc.args)

			// A preview that wrote to disk is not a preview.
			after := readFile(t, filepath.Join(dir, "main.go"))
			if after != before {
				t.Errorf("%s: preview modified the file on disk", tc.tool)
			}

			diffs, ok := result["diffs"].([]any)
			if !ok || len(diffs) == 0 {
				t.Fatalf("%s: expected the preview to carry diffs, got %v", tc.tool, result["diffs"])
			}

			for _, d := range diffs {
				file := d.(map[string]any)
				// A delete carries no hunks by design.
				action, _ := file["action"].(string)
				if action == "delete" {
					continue
				}

				hunks, ok := file["hunks"].([]any)
				if !ok || len(hunks) == 0 {
					t.Fatalf("%s: expected hunks in the diff, got %v", tc.tool, file["hunks"])
				}

				for i, h := range hunks {
					hunk := h.(map[string]any)
					start, _ := hunk["start"].(float64)
					end, _ := hunk["end"].(float64)

					next, _ := hunk["new"].(string)
					old, _ := hunk["old"].(string)

					// Every hunk must show at least one side. A hunk with
					// neither is pure noise — it tells the reviewer that
					// something changed somewhere, and nothing more.
					if old == "" && next == "" {
						t.Errorf("%s: hunk %d carries neither before- nor after-text", tc.tool, i)
						continue
					}

					// A pure insertion legitimately has no before-text: there
					// is no prior content at a zero-width position. Every
					// other hunk replaces or deletes bytes and must show what
					// they were — an empty after-text is fine on its own
					// (that is a deletion), an empty before-text is not.
					if start == end {
						continue
					}

					if old == "" {
						t.Errorf("%s: hunk %d replaces bytes [%d,%d) but carries no before-text; "+
							"the preview cannot be reviewed", tc.tool, i, int(start), int(end))
					}
				}
			}
		})
	}
}
