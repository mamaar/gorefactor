package refactor

import (
	"fmt"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// planDump renders everything of a cross-configuration outcome that must not
// depend on how the views were prepared.
func planDump(plan *types.RefactoringPlan, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	files := slices.Clone(plan.AffectedFiles)
	slices.Sort(files)
	return fmt.Sprintf("changes %+v\nfiles %v\ncoverage %+v\nskipped %+v",
		sortedChanges(plan), files, plan.Coverage, plan.SkippedSites)
}

// A run through the engine prepares views ahead and drops each secondary view
// once planned; planAcrossViews alone keeps every view loaded.
func TestPlanAcrossConfigs_PlansLikeKeepingEveryViewLoaded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		seed  string // function, in p/seed.go
	}{
		{"platform siblings", map[string]string{
			"p/seed.go":         "package p\n\nfunc leaf() {}\n",
			"p/open_linux.go":   "package p\n\nfunc open() { leaf() }\n",
			"p/open_windows.go": "package p\n\nfunc open() { leaf() }\n",
			"p/use.go":          "package p\n\nfunc Use() { open() }\n",
			"q/q_tagx.go":       "//go:build tagx\n\npackage q\n\nimport \"example.com/threadtest/p\"\n\nfunc Q() { p.Use() }\n",
			"q/q.go":            "package q\n",
		}, "leaf"},
		{"alternating siblings over several rounds", map[string]string{
			"p/seed.go":      "package p\n\nfunc leaf() {}\n",
			"p/f_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc f() { leaf() }\n",
			"p/f_default.go": "//go:build !tagx\n\npackage p\n\nfunc f() {}\n",
			"p/g_default.go": "//go:build !tagx\n\npackage p\n\nfunc g() { f() }\n",
			"p/g_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc g() {}\n",
			"p/h_tagx.go":    "//go:build tagx\n\npackage p\n\nfunc h() { g() }\n",
			"p/h_default.go": "//go:build !tagx\n\npackage p\n\nfunc h() {}\n",
			"p/h_windows.go": "package p\n\nfunc onWindows() { h() }\n",
			"p/top.go":       "package p\n\nfunc Top() { h() }\n",
		}, "leaf"},
		{"test file no view type-checks", map[string]string{
			"p/seed.go":        seedSrc,
			"p/seed_test.go":   seedTestSrc,
			"p/bad_test.go":    "package p\n\n" + missingImport + "\nfunc useSeed() { Seed() }\n",
			"p/tagged_test.go": "//go:build tagx\n\npackage p\n\nfunc useSeedTagged() { Seed() }\n",
			"p/win_windows.go": "package p\n\nfunc win() { Seed() }\n",
		}, "Seed"},
		{"seed only in a secondary view", map[string]string{
			"p/p.go":       "package p\n\nfunc Use() {}\n",
			"p/seed.go":    "//go:build tagx\n\npackage p\n\nfunc leaf() {}\n\nfunc caller() { leaf() }\n",
			"p/p_win.go":   "//go:build windows\n\npackage p\n\nfunc win() { Use() }\n",
			"p/p_tag2.go":  "//go:build tagx\n\npackage p\n\nfunc second() { caller() }\n",
			"p/p_other.go": "//go:build !tagx\n\npackage p\n\nfunc caller() {}\n",
		}, "leaf"},
		{"value use aborts in a secondary view", map[string]string{
			"p/seed.go":   "package p\n\nfunc leaf() {}\n",
			"p/p_win.go":  "//go:build windows\n\npackage p\n\nvar hook = leaf\n",
			"p/p_tagx.go": "//go:build tagx\n\npackage p\n\nfunc tagged() { leaf() }\n",
		}, "leaf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			threadReq := types.ThreadContextRequest{FunctionName: tc.seed, SourceFile: "p/seed.go"}

			eng, dir := loadConfigsEngine(t, tc.files)
			got := planDump(eng.ThreadContextAcrossConfigs(threadReq))
			addReq := types.AddContextParameterRequest{FunctionName: tc.seed, SourceFile: dir + "/p/seed.go"}
			gotAdd := planDump(eng.AddContextParameterAcrossConfigs(addReq))
			again := planDump(eng.ThreadContextAcrossConfigs(threadReq))

			views, err := eng.WorkspaceSet().Views()
			if err != nil {
				t.Fatal(err)
			}
			plan, _, err := planAcrossViews(views, &threadContextPlanner{engine: eng, req: threadReq}, maxConfigRounds)
			if want := planDump(plan, err); got != want || again != want {
				t.Errorf("thread plans differ:\n got %s\nagain %s\n want %s", got, again, want)
			}
			plan, _, err = planAcrossViews(views, &addContextParameterPlanner{engine: eng, req: addReq}, maxConfigRounds)
			if want := planDump(plan, err); gotAdd != want {
				t.Errorf("add plans differ:\n got %s\nwant %s", gotAdd, want)
			}
		})
	}
}
