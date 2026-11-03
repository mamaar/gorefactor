package refactor

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// renamedByFile returns how many changes plan makes per file, keyed by path
// relative to dir.
func renamedByFile(t *testing.T, dir string, plan *types.RefactoringPlan) map[string]int {
	t.Helper()
	out := make(map[string]int)
	for _, c := range plan.Changes {
		rel, err := filepath.Rel(dir, c.File)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.ToSlash(rel)]++
	}
	return out
}

func assertRenamed(t *testing.T, dir string, plan *types.RefactoringPlan, want map[string]int) {
	t.Helper()
	got := renamedByFile(t, dir, plan)
	if !maps.Equal(got, want) {
		t.Errorf("changes per file = %v, want %v", got, want)
	}
}

func renameMethodPlan(t *testing.T, files map[string]string, req types.RenameMethodRequest) (*types.RefactoringPlan, string, error) {
	t.Helper()
	eng, dir := loadConfigsEngine(t, files)
	req.PackagePath = types.ResolvePackagePath(eng.WorkspaceSet().Primary().Workspace, req.PackagePath)
	plan, err := eng.RenameMethodAcrossConfigs(req)
	return plan, dir, err
}

const mod = "example.com/threadtest/"

func TestRenameMethod_LeavesPackageFunctionOfSameNameAlone(t *testing.T) {
	plan, dir, err := renameMethodPlan(t, map[string]string{
		"p/t.go": "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"q/q.go": "package q\n\nfunc M() {}\n",
		"r/r.go": "package r\n\nimport (\n\t\"" + mod + "p\"\n\t\"" + mod + "q\"\n)\n\nfunc Use() { q.M(); p.T{}.M() }\n",
	}, types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err != nil {
		t.Fatal(err)
	}
	assertRenamed(t, dir, plan, map[string]int{"p/t.go": 1, "r/r.go": 1})
}

func TestRenameMethod_LeavesSameNamedMethodOfOtherTypeAlone(t *testing.T) {
	plan, dir, err := renameMethodPlan(t, map[string]string{
		"p/t.go": "package p\n\ntype T struct{}\n\ntype U struct{}\n\nfunc (T) M() {}\n\nfunc (U) M() {}\n\nfunc Use(t T, u U) { t.M(); u.M() }\n",
		"q/q.go": "package q\n\ntype V struct{}\n\nfunc (V) M() {}\n\nfunc Use(v V, f func()) { v.M() }\n",
	}, types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err != nil {
		t.Fatal(err)
	}
	assertRenamed(t, dir, plan, map[string]int{"p/t.go": 2})
}

func TestRenameMethod_FollowsEmbeddingExplicitAndPromoted(t *testing.T) {
	plan, dir, err := renameMethodPlan(t, map[string]string{
		"p/t.go": "package p\n\ntype T struct{}\n\nfunc (*T) M() {}\n",
		"w/w.go": "package w\n\nimport \"" + mod + "p\"\n\ntype W struct{ *p.T }\n\ntype X struct{ T p.T }\n\n" +
			"func Use(w W, x X) { w.M(); w.T.M(); x.T.M() }\n",
	}, types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err != nil {
		t.Fatal(err)
	}
	assertRenamed(t, dir, plan, map[string]int{"p/t.go": 1, "w/w.go": 3})
}

func TestRenameMethod_RenamesMethodValuesAndExpressions(t *testing.T) {
	plan, dir, err := renameMethodPlan(t, map[string]string{
		"p/t.go": "package p\n\ntype T struct{}\n\nfunc (*T) M() {}\n\n" +
			"func Use(t *T) { f := t.M; f(); h := (*T).M; _ = h }\n",
	}, types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err != nil {
		t.Fatal(err)
	}
	assertRenamed(t, dir, plan, map[string]int{"p/t.go": 3})
}

func TestRenameMethod_RenamesMethodExpressionOfValueReceiver(t *testing.T) {
	plan, dir, err := renameMethodPlan(t, map[string]string{
		"p/t.go": "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n\n" +
			"func Use(t T) { f := t.M; f(); g := T.M; _ = g; h := (*T).M; _ = h }\n",
	}, types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err != nil {
		t.Fatal(err)
	}
	assertRenamed(t, dir, plan, map[string]int{"p/t.go": 4})
}

func TestRenameMethod_InterfaceTargetRenamesOnlyImplementations(t *testing.T) {
	plan, dir, err := renameMethodPlan(t, map[string]string{
		"p/i.go": "package p\n\ntype I interface{ M() }\n\ntype A struct{}\n\nfunc (A) M() {}\n\ntype B struct{}\n\nfunc (B) M(x int) {}\n\n" +
			"func Use(i I, b B) { i.M(); b.M(1) }\n",
	}, types.RenameMethodRequest{TypeName: "I", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err != nil {
		t.Fatal(err)
	}
	// I.M, A.M and the call through I; B.M and its call are unrelated.
	assertRenamed(t, dir, plan, map[string]int{"p/i.go": 3})
}

func TestRenameMethod_ConcreteTargetRenamesWorkspaceInterfaceAndSiblings(t *testing.T) {
	plan, dir, err := renameMethodPlan(t, map[string]string{
		"p/i.go": "package p\n\ntype I interface{ M() }\n\ntype A struct{}\n\nfunc (A) M() {}\n\ntype B struct{}\n\nfunc (B) M() {}\n\n" +
			"func Use(i I, a A) { i.M(); a.M() }\n",
	}, types.RenameMethodRequest{TypeName: "A", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err != nil {
		t.Fatal(err)
	}
	// I.M, A.M, B.M and both calls.
	assertRenamed(t, dir, plan, map[string]int{"p/i.go": 5})
}

func TestRenameMethod_RefusesWhenMethodSatisfiesExternalInterface(t *testing.T) {
	_, _, err := renameMethodPlan(t, map[string]string{
		"p/t.go": "package p\n\nimport \"fmt\"\n\ntype T struct{}\n\nfunc (T) String() string { return \"\" }\n\nvar _ fmt.Stringer = T{}\n",
	}, types.RenameMethodRequest{TypeName: "T", MethodName: "String", NewMethodName: "Str", PackagePath: mod + "p"})
	if err == nil || !strings.Contains(err.Error(), "fmt.Stringer") {
		t.Errorf("error = %v, want a refusal naming fmt.Stringer", err)
	}
}

func TestRenameMethod_RefusesWhenFileWithTypeErrorMentionsName(t *testing.T) {
	_, _, err := renameMethodPlan(t, map[string]string{
		"p/t.go":   "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n",
		"p/bad.go": "package p\n\nfunc Bad() { undefinedValue.M() }\n",
	}, types.RenameMethodRequest{TypeName: "T", MethodName: "M", NewMethodName: "N", PackagePath: mod + "p"})
	if err == nil || !strings.Contains(err.Error(), "type error") {
		t.Errorf("error = %v, want a refusal because of the type error", err)
	}
}
