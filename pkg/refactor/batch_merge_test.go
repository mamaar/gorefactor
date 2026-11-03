package refactor

import (
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func stepPlan(files []string, changes ...types.Change) *types.RefactoringPlan {
	return &types.RefactoringPlan{Changes: changes, AffectedFiles: files, Reversible: true}
}

func TestMergeStepPlans_ConcatenatesChangesInStepOrderAndDedupesFiles(t *testing.T) {
	a := stepPlan([]string{"a.go", "b.go"},
		types.Change{File: "a.go", Start: 0, End: 1, NewText: "x"},
		types.Change{File: "b.go", Start: 0, End: 1, NewText: "x"})
	b := stepPlan([]string{"b.go"}, types.Change{File: "b.go", Start: 5, End: 6, NewText: "y"})

	got, err := MergeStepPlans([]*types.RefactoringPlan{a, b})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Changes) != 3 || got.Changes[2].NewText != "y" {
		t.Errorf("changes = %+v", got.Changes)
	}

	if len(got.AffectedFiles) != 2 {
		t.Errorf("affected files = %v, want a.go and b.go once", got.AffectedFiles)
	}
}

func TestMergeStepPlans_NamesStepsWhoseChangesOverlap(t *testing.T) {
	a := stepPlan([]string{"a.go"}, types.Change{File: "a.go", Start: 0, End: 4, NewText: "x"})
	b := stepPlan([]string{"a.go"}, types.Change{File: "a.go", Start: 2, End: 6, NewText: "y"})

	_, err := MergeStepPlans([]*types.RefactoringPlan{a, b})
	if err == nil || !strings.Contains(err.Error(), "step 1 and step 2") {
		t.Fatalf("err = %v, want one naming step 1 and step 2", err)
	}
}

func TestMergeStepPlans_RejectsTheSameEditFromTwoSteps(t *testing.T) {
	c := types.Change{File: "a.go", Start: 3, End: 3, NewText: "x"}

	_, err := MergeStepPlans([]*types.RefactoringPlan{stepPlan([]string{"a.go"}, c), stepPlan([]string{"a.go"}, c)})
	if err == nil {
		t.Fatal("want a conflict for the same insertion from two steps")
	}
}

func TestMergeStepPlans_CarriesCoverageAndSkippedSitesWithoutDuplicates(t *testing.T) {
	site := types.SkippedSite{}
	site.File = "s.go"
	typeErr := types.PackageTypeError{Package: "p", Config: "tags=x", Error: "boom"}
	mk := func(cfgs ...string) *types.RefactoringPlan {
		p := stepPlan(nil)
		p.Coverage = types.Coverage{
			Configs:    cfgs,
			Uncovered:  []types.UncoveredFile{{Path: "u.go", Reason: "r"}},
			TypeErrors: []types.PackageTypeError{typeErr},
		}
		p.SkippedSites = []types.SkippedSite{site}

		return p
	}

	got, err := MergeStepPlans([]*types.RefactoringPlan{mk("default", "tags=x"), mk("default", "tags=x")})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Coverage.Configs) != 2 || len(got.Coverage.Uncovered) != 1 ||
		len(got.Coverage.TypeErrors) != 1 || len(got.SkippedSites) != 1 {
		t.Errorf("coverage = %+v, skipped = %v", got.Coverage, got.SkippedSites)
	}
}
