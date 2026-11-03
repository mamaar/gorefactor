package refactor

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

func changedFiles(plan *types.RefactoringPlan) []string {
	var names []string
	for _, c := range plan.Changes {
		name := filepath.Base(c.File)
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func TestEngine_ThreadContextRunsAgainstASecondaryView(t *testing.T) {
	dir := writeThreadContextWorkspace(t, map[string]string{
		"a/seed.go":       "package a\n\nfunc Seed() {}\n",
		"b/b.go":          "package b\n",
		"b/x_tag_test.go": "//go:build fts5x\n\npackage b\n\nimport \"example.com/threadtest/a\"\n\nfunc helper() { a.Seed() }\n",
	})
	cfg := analysis.NewBuildConfig("linux", "amd64", true)
	eng := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true, AllowBreaking: true, BuildConfig: &cfg},
		slog.New(slog.NewTextHandler(io.Discard, nil))).(*DefaultEngine)
	primary, err := eng.LoadWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	views, err := eng.WorkspaceSet().Views()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Workspace != primary {
		t.Fatalf("views = %d, want the primary and one tagged view", len(views))
	}
	req := types.ThreadContextRequest{FunctionName: "Seed", SourceFile: "a/seed.go"}

	plan, err := eng.ThreadContext(views[1].Workspace, req)
	if err != nil {
		t.Fatal(err)
	}
	got := changedFiles(plan)
	if !slices.Equal(got, []string{"seed.go", "x_tag_test.go"}) {
		t.Errorf("tagged view changed %v", got)
	}

	plan, err = eng.ThreadContext(primary, req)
	if err != nil {
		t.Fatal(err)
	}
	got = changedFiles(plan)
	if !slices.Equal(got, []string{"seed.go"}) {
		t.Errorf("primary view changed %v", got)
	}
}
