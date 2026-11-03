package refactor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestMoveSymbolOperation_RefusesSourcePackageThatIsNotTypeChecked(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"src/src.go": "package src\n\nfunc One() int { return 1 }\n",
	})
	ws := eng.WorkspaceSet().Primary().Workspace
	op := &MoveSymbolOperation{Request: types.MoveSymbolRequest{
		SymbolName:   "One",
		FromPackage:  filepath.Join(dir, "src"),
		ToPackage:    filepath.Join(dir, "dst"),
		CreateTarget: true,
	}}

	err := op.Validate(ws)

	if err == nil || !strings.Contains(err.Error(), "not type-checked") {
		t.Errorf("Validate = %v, want a refusal naming the missing type-check", err)
	}
}

func TestMoveSymbolAcrossConfigs_PlanningCreatesNothingAndLeavesTheWorkspaceAsItWas(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"src/src.go": "package src\n\nfunc One() int { return 1 }\n",
	})
	ws := eng.WorkspaceSet().Primary().Workspace
	packages := len(ws.Packages)

	plan, err := eng.MoveSymbolAcrossConfigs(types.MoveSymbolRequest{
		SymbolName:  "One",
		FromPackage: filepath.Join(dir, "src"),
		ToPackage:   filepath.Join(dir, "dst"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "dst")); !os.IsNotExist(err) {
		t.Errorf("planning created the target directory, stat err = %v", err)
	}

	if len(ws.Packages) != packages {
		t.Errorf("planning changed the workspace's packages: %d, want %d", len(ws.Packages), packages)
	}

	created := false

	for _, c := range plan.Changes {
		if c.File == filepath.Join(dir, "dst", "dst.go") && strings.Contains(c.NewText, "package dst") && strings.Contains(c.NewText, "func One() int") {
			created = true
		}
	}

	if !created {
		t.Errorf("plan has no change creating dst/dst.go with the moved function: %+v", plan.Changes)
	}
}

func TestMoveSymbolOperation_RefusesACompanionCollidingWithASymbolFromAnotherPackage(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"a/a.go": "package a\n\ntype Foo struct{ n int }\n\nfunc NewFoo() *Foo { return &Foo{n: 1} }\n",
		"b/b.go": "package b\n\nfunc NewFoo() int { return 1 }\n",
	})

	_, err := eng.MoveSymbolAcrossConfigs(types.MoveSymbolRequest{
		ToPackage: filepath.Join(dir, "dst"),
		Symbols: []types.QualifiedSymbol{
			{Package: filepath.Join(dir, "a"), Name: "Foo"},
			{Package: filepath.Join(dir, "b"), Name: "NewFoo"},
		},
	})

	if err == nil || !strings.Contains(err.Error(), "NewFoo would collide with another symbol of the same name") {
		t.Errorf("err = %v, want a refusal naming the colliding NewFoo", err)
	}
}
