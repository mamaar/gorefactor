package refactor

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestMoveSharedSymbols_BisectsAGroupPlanErrorThatBlamesNoMember(t *testing.T) {
	const use = "package %s\n\nimport \"example.com/threadtest/src\"\n\nfunc F() int { return src.S1() + src.S2() + src.S3() + src.S4() }\n"
	eng, dir := loadConfigsEngine(t, map[string]string{
		"src/src.go": "package src\n\nfunc S1() int { return 1 }\n\nfunc S2() int { return 2 }\n\nfunc S3() int { return 3 }\n\nfunc S4() int { return 4 }\n",
		"a/a.go":     fmt.Sprintf(use, "a"),
		"b/b.go":     fmt.Sprintf(use, "b"),
	})
	ws := eng.WorkspaceSet().Primary().Workspace
	op := &MoveByDependenciesOperation{Request: types.MoveByDependenciesRequest{Workspace: dir, MoveSharedTo: "shared"}}
	groupPlans := 0
	planMove := func(req types.MoveSymbolRequest) (*types.RefactoringPlan, error) {
		if len(req.Symbols) > 1 {
			groupPlans++
			for _, s := range req.Symbols {
				if s.Name == "S3" {
					return nil, errors.New("refused under tagx")
				}
			}
		}
		return eng.MoveSymbolAcrossConfigs(req)
	}

	plan, err := eng.moveSharedSymbols(ws, op, planMove)

	if err != nil {
		t.Fatalf("moveSharedSymbols: %v", err)
	}
	added := ""
	for _, c := range plan.Changes {
		if c.File == filepath.Join(dir, "shared", "shared.go") {
			added += c.NewText
		}
	}
	for _, name := range []string{"S1", "S2", "S4"} {
		if !strings.Contains(added, "func "+name+"()") {
			t.Errorf("plan does not move %s:\n%s", name, added)
		}
	}
	if strings.Contains(added, "func S3()") {
		t.Errorf("plan moves S3:\n%s", added)
	}
	if len(plan.SkippedSites) != 1 || !strings.Contains(plan.SkippedSites[0].Reason, "S3 not moved: refused under tagx") {
		t.Errorf("SkippedSites = %+v, want S3 skipped with the error text", plan.SkippedSites)
	}
	if groupPlans > 5 {
		t.Errorf("planned %d groups, want at most 5 for 4 candidates", groupPlans)
	}
}
