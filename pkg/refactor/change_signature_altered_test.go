package refactor

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func alteredFixture(t *testing.T) (*DefaultEngine, string) {
	t.Helper()
	return loadConfigsEngine(t, map[string]string{
		"p/iface.go": "package p\n\ntype Runner interface{ Run(n int) int }\n",
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n int) int { return n }\n",
		"p/b.go":     "package p\n\ntype B struct{}\n\nfunc (*B) Run(n int) int { return n }\n",
		"p/fn.go":    "package p\n\nfunc fn(n int) int { return n }\n",
	})
}

func executeAltered(t *testing.T, eng *DefaultEngine, dir, fn, file string, propagate bool) (*ChangeSignatureOperation, *types.RefactoringPlan) {
	t.Helper()
	ws := eng.WorkspaceSet().Primary().Workspace
	op := &ChangeSignatureOperation{
		FunctionName:         fn,
		SourceFile:           filepath.Join(dir, file),
		NewParams:            []Parameter{{Name: "ctx", Type: "context.Context"}, {Name: "n", Type: "int"}},
		Scope:                types.WorkspaceScope,
		PropagateToInterface: propagate,
		DefaultValue:         "context.TODO()",
		NewReturnPosition:    -1,
		RemovedReturnIndex:   -1,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ensureTypeChecked(eng.WorkspaceSet().Primary().Parser, ws)
	err := op.Validate(ws)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatal(err)
	}
	return op, plan
}

func TestChangeSignatureOperation_AlteredDeclsListsFunction(t *testing.T) {
	eng, dir := alteredFixture(t)
	op, _ := executeAltered(t, eng, dir, "fn", "p/fn.go", false)

	want := []AlteredDecl{{Key: types.SymbolKey{PkgPath: "example.com/threadtest/p", Name: "fn"}, File: filepath.Join(dir, "p/fn.go")}}
	got := op.AlteredDecls()
	if !slices.Equal(got, want) {
		t.Errorf("AlteredDecls = %+v, want %+v", got, want)
	}
}

func TestChangeSignatureOperation_AlteredDeclsListsInterfaceAndImplementationsWithPropagate(t *testing.T) {
	eng, dir := alteredFixture(t)
	op, plan := executeAltered(t, eng, dir, "A.Run", "p/a.go", true)
	assertNoDuplicateChanges(t, plan)

	pkg := "example.com/threadtest/p"
	want := []AlteredDecl{
		{Key: types.SymbolKey{PkgPath: pkg, Recv: "A", Name: "Run"}, File: filepath.Join(dir, "p/a.go")},
		{Key: types.SymbolKey{PkgPath: pkg, Recv: "Runner", Name: "Run"}, File: filepath.Join(dir, "p/iface.go")},
		{Key: types.SymbolKey{PkgPath: pkg, Recv: "B", Name: "Run"}, File: filepath.Join(dir, "p/b.go")},
	}
	got := op.AlteredDecls()
	if len(got) != len(want) {
		t.Errorf("AlteredDecls = %+v, want %d entries", got, len(want))
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("AlteredDecls = %+v, missing %+v", got, w)
		}
	}
}

func assertNoDuplicateChanges(t *testing.T, plan *types.RefactoringPlan) {
	t.Helper()
	seen := make(map[changeIdentity]bool)
	for _, c := range plan.Changes {
		id := identityOf(c)
		if seen[id] {
			t.Errorf("duplicate change %q at %s [%d,%d)", c.Description, c.File, c.Start, c.End)
		} else {
			seen[id] = true
		}
	}
}
