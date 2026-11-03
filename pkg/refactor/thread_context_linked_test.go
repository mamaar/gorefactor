package refactor

import (
	"errors"
	"slices"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

const linkedPkg = "example.com/m/p"

func linkedKeys(keys ...pkgtypes.SymbolKey) pkgtypes.SymbolKeySet {
	set := make(pkgtypes.SymbolKeySet)
	for _, k := range keys {
		set[k] = struct{}{}
	}

	return set
}

func sortedKeys(set pkgtypes.SymbolKeySet) []string {
	var out []string
	for k := range set {
		out = append(out, k.String())
	}

	slices.Sort(out)

	return out
}

const linkedFiles = `package p

func leaf() {}

func open() {}

func use() { open() }

func other() {}
`

func TestThreadContext_LinkedKeySeedsItsFunctionAndClimbsItsCallers(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{"p/p.go": linkedFiles})
	op := &ThreadContextOperation{
		FunctionName: "leaf",
		SourceFile:   "p/p.go",
		Linked:       linkedKeys(pkgtypes.SymbolKey{PkgPath: linkedPkg, Name: "open"}),
	}

	_, err := op.Execute(ws)
	if err != nil {
		t.Fatal(err)
	}

	assertNames(t, gainingNames(op.plan), []string{"leaf", "open", "use"}, "Funcs")

	got := argIn(t, op.plan, "use")
	if got != "ctx" {
		t.Errorf("use passes %q", got)
	}

	want := []string{"example.com/m/p.leaf", "example.com/m/p.open", "example.com/m/p.use"}
	if got := sortedKeys(op.GainedKeys()); !slices.Equal(got, want) {
		t.Errorf("GainedKeys = %v, want %v", got, want)
	}
}

func TestThreadContext_AbsentSeedRunsOnLinkedKeysAlone(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{"p/p.go": linkedFiles})
	op := &ThreadContextOperation{
		FunctionName: "leaf",
		SourceFile:   "p/elsewhere.go",
		Linked:       linkedKeys(pkgtypes.SymbolKey{PkgPath: linkedPkg, Name: "open"}),
	}

	_, err := op.Execute(ws)
	if err != nil {
		t.Fatal(err)
	}

	assertNames(t, gainingNames(op.plan), []string{"open", "use"}, "Funcs")
}

func TestThreadContext_AbsentSeedWithoutResolvingLinkedKeyIsNotFound(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{"p/p.go": linkedFiles})
	plain := &ThreadContextOperation{FunctionName: "leaf", SourceFile: "p/elsewhere.go"}

	_, want := plain.Execute(ws)
	if want == nil {
		t.Fatal("expected the seed to be absent")
	}

	op := &ThreadContextOperation{
		FunctionName: "leaf",
		SourceFile:   "p/elsewhere.go",
		Linked:       linkedKeys(pkgtypes.SymbolKey{PkgPath: "example.com/m/q", Name: "open"}),
	}
	_, err := op.Execute(ws)

	var re *pkgtypes.RefactorError
	if !errors.As(err, &re) || re.Type != pkgtypes.SymbolNotFound {
		t.Fatalf("err = %v, want a SymbolNotFound RefactorError", err)
	}

	if err.Error() != want.Error() {
		t.Errorf("err = %q, want %q", err, want)
	}

	if !isSymbolNotFound(err) {
		t.Error("isSymbolNotFound = false")
	}
}

func TestThreadContext_LinkedInterfaceMethodPullsInImplementations(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{
		"store/store.go": storeFiles,
		"store/leaf.go":  "package store\n\nfunc leaf() {}\n",
	})
	op := &ThreadContextOperation{
		FunctionName: "leaf",
		SourceFile:   "store/leaf.go",
		Linked:       linkedKeys(pkgtypes.SymbolKey{PkgPath: "example.com/m/store", Recv: "Store", Name: "Save"}),
	}

	_, err := op.Execute(ws)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"example.com/m/store.FileStore.Save", "example.com/m/store.MemStore.Save", "example.com/m/store.Store.Save",
		"example.com/m/store.leaf", "example.com/m/store.useIface", "example.com/m/store.useMem",
	}

	got := sortedKeys(op.GainedKeys())
	if !slices.Equal(got, want) {
		t.Errorf("GainedKeys = %v, want %v", got, want)
	}
}
