package refactor

import (
	"strings"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

func stopSet(keys ...pkgtypes.SymbolKey) pkgtypes.SymbolKeySet { return linkedKeys(keys...) }

func stopKey(recv, name string) pkgtypes.SymbolKey {
	return pkgtypes.SymbolKey{PkgPath: linkedPkg, Recv: recv, Name: name}
}

const stopChain = `package p

func leaf() {}

func mid() { leaf() }

func top() { mid() }

func main() { top() }
`

func TestThreadContext_StopDoesNotGainAndPassesTODO(t *testing.T) {
	plan := threadPlanStopping(t, map[string]string{"p/p.go": stopChain}, "p/p.go", "leaf",
		stopSet(stopKey("", "mid")))
	assertNames(t, gainingNames(plan), []string{"leaf"}, "Funcs")
	got := argIn(t, plan, "mid")
	if got != "context.TODO()" {
		t.Errorf("stop passes %q", got)
	}
	if len(plan.Stops) != 1 {
		t.Errorf("Stops = %d, want 1", len(plan.Stops))
	}
}

func TestThreadContext_StopWithLocalCtxPassesItAndKeepsIt(t *testing.T) {
	plan := threadPlanStopping(t, map[string]string{"p/p.go": `package p

import "context"

func leaf() {}

func mid() {
	ctx := context.Background()
	_ = ctx
	leaf()
}
`}, "p/p.go", "leaf", stopSet(stopKey("", "mid")))
	assertNames(t, gainingNames(plan), []string{"leaf"}, "Funcs")
	assertNames(t, fakeNames(plan), nil, "Fake")
	got := argIn(t, plan, "mid")
	if got != "ctx" {
		t.Errorf("stop passes %q", got)
	}
	if len(plan.Stops) != 1 {
		t.Errorf("Stops = %d, want 1", len(plan.Stops))
	}
}

const stopIfaceFiles = `package p

type Store interface{ Save(x int) }

type A struct{}

func (A) Save(x int) {}

type B struct{}

func (*B) Save(x int) {}

func useA(a A) { a.Save(1) }

func useB(b *B) { b.Save(1) }

func useI(s Store) { s.Save(1) }
`

func TestThreadContext_StoppedMethodDoesNotExpandInterface(t *testing.T) {
	// Seed is a plain function that calls the stopped method.
	plan := threadPlanStopping(t, map[string]string{"p/p.go": `package p

type Store interface{ Save(x int) }

type A struct{}

func (A) Save(x int) { work() }

type B struct{}

func (B) Save(x int) {}

func work() {}

func useI(s Store) { s.Save(1) }
`}, "p/p.go", "work", stopSet(stopKey("A", "Save")))
	assertNames(t, gainingLabels(plan), []string{"work"}, "Funcs")
	got := argIn(t, plan, "Save")
	if got != "context.TODO()" {
		t.Errorf("stopped method passes %q", got)
	}
}

func TestThreadContext_ExpansionReachingStopErrors(t *testing.T) {
	err := threadErrStopping(t, map[string]string{"p/p.go": stopIfaceFiles}, "p/p.go", "B.Save", true,
		stopSet(stopKey("A", "Save")))
	for _, want := range []string{"A.Save", "Store.Save", "No changes were made"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestThreadContext_SeedInStopSetErrors(t *testing.T) {
	err := threadErrStopping(t, map[string]string{"p/p.go": stopChain}, "p/p.go", "leaf", true,
		stopSet(stopKey("", "leaf")))
	if !strings.Contains(err.Error(), "leaf") {
		t.Errorf("error %q does not name the seed", err)
	}
}

func TestThreadContext_UnreachedStopIsSilent(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{"p/p.go": stopChain + "\nfunc unrelated() {}\n"})
	op := &ThreadContextOperation{FunctionName: "leaf", SourceFile: "p/p.go",
		StopAt: stopSet(stopKey("", "unrelated"), stopKey("", "nonexistent"))}
	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SkippedSites) != 0 || len(op.plan.Stops) != 0 {
		t.Errorf("unreached stop reported: %v", plan.SkippedSites)
	}
}

func TestThreadContext_StopReportedAsSkippedSite(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{"p/p.go": stopChain})
	op := &ThreadContextOperation{FunctionName: "leaf", SourceFile: "p/p.go",
		StopAt: stopSet(stopKey("", "mid"))}
	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SkippedSites) != 1 {
		t.Fatalf("SkippedSites = %v", plan.SkippedSites)
	}
	s := plan.SkippedSites[0]
	if !strings.HasSuffix(s.File, "p.go") || s.Line != 5 || s.Snippet != "mid" || !strings.HasPrefix(s.Reason, "stop_at:") {
		t.Errorf("site = %+v", s)
	}
}

func TestThreadContext_StopWithRealCtxInScopeIsNotReported(t *testing.T) {
	ws := buildScopeWorkspace(t, map[string]string{"p/p.go": `package p

import "context"

func leaf() {}

func mid(ctx context.Context) { leaf() }
`})
	op := &ThreadContextOperation{FunctionName: "leaf", SourceFile: "p/p.go",
		StopAt: stopSet(stopKey("", "mid"))}
	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SkippedSites) != 0 {
		t.Errorf("SkippedSites = %v", plan.SkippedSites)
	}
}
