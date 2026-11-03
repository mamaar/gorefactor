package refactor

import (
	"slices"
	"strings"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// threadPlanFor builds a type-checked workspace, runs the analysis, and returns
// the plan-of-record. Type-checking is what makes these tests meaningful: on an
// unchecked workspace every go/types predicate degrades and the operation aborts
// (see TestThreadContext_AbortsWithoutTypeInfo), so a helper that skipped it
// would make every other test here vacuous.
func threadPlanFor(t *testing.T, files map[string]string, sourceFile, funcName string) *threadPlan {
	t.Helper()
	return threadPlanStopping(t, files, sourceFile, funcName, nil)
}

// threadPlanStopping is threadPlanFor with a user-declared stop set.
func threadPlanStopping(t *testing.T, files map[string]string, sourceFile, funcName string, stops pkgtypes.SymbolKeySet) *threadPlan {
	t.Helper()
	ws := buildScopeWorkspace(t, files)
	op := &ThreadContextOperation{FunctionName: funcName, SourceFile: sourceFile, StopAt: stops}
	_, err := op.Execute(ws)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if op.plan == nil {
		t.Fatalf("Execute returned no plan")
	}
	return op.plan
}

// threadErrFor runs the analysis expecting it to abort, and returns the error.
func threadErrFor(t *testing.T, files map[string]string, sourceFile, funcName string, typeCheck bool) error {
	t.Helper()
	return threadErrStopping(t, files, sourceFile, funcName, typeCheck, nil)
}

// threadErrStopping is threadErrFor with a user-declared stop set.
func threadErrStopping(t *testing.T, files map[string]string, sourceFile, funcName string, typeCheck bool, stops pkgtypes.SymbolKeySet) error {
	t.Helper()
	ws := buildScopeWorkspace(t, files)
	if !typeCheck {
		// LoadWorkspace on its own does not type-check, which is exactly the
		// silent-no-op condition the HasTypeInfo guard exists to catch.
		_, ws = writeRenameFieldWorkspace(t, files)
	}
	op := &ThreadContextOperation{FunctionName: funcName, SourceFile: sourceFile, StopAt: stops}
	_, err := op.Execute(ws)
	if err == nil {
		t.Fatalf("expected Execute to abort, got nil error")
	}
	return err
}

// gainingNames returns the sorted names of the functions that gain a ctx param.
func gainingNames(plan *threadPlan) []string {
	var names []string
	for f := range plan.Funcs {
		names = append(names, f.Name())
	}
	slices.Sort(names)
	return names
}

// argIn returns the single ctx expression injected at a call site located
// directly inside the function declaration named caller.
func argIn(t *testing.T, plan *threadPlan, caller string) string {
	t.Helper()
	var found []string
	for _, edit := range plan.ArgFor {
		if edit.Site.Fn != nil && edit.Site.Fn.Name.Name == caller {
			found = append(found, edit.Expr)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("no ArgFor entry for a call site inside %s", caller)
	default:
		t.Fatalf("expected exactly one ArgFor entry inside %s, got %d: %v", caller, len(found), found)
	}
	return ""
}

func fakeNames(plan *threadPlan) []string {
	var names []string
	for f := range plan.Fake {
		names = append(names, f.Name())
	}
	slices.Sort(names)
	return names
}

func assertNames(t *testing.T, got, want []string, what string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func TestThreadContext_LinearChain(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"app/main.go": `package main

func main() { a() }

func a() { b() }

func b() { c() }

func c() {}
`,
	}, "app/main.go", "c")

	// main is a root: it cannot gain a parameter, so the climb stops there.
	assertNames(t, gainingNames(plan), []string{"a", "b", "c"}, "Funcs")

	got := argIn(t, plan, "main")
	if got != "context.TODO()" {
		t.Errorf("main must fabricate a root context, got %q", got)
	}
	got = argIn(t, plan, "a")
	if got != "ctx" {
		t.Errorf("a must pass its injected ctx, got %q", got)
	}
	got = argIn(t, plan, "b")
	if got != "ctx" {
		t.Errorf("b must pass its injected ctx, got %q", got)
	}
}

func TestThreadContext_CallerWithCtxStopsClimb(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

import "context"

func caller(ctx context.Context) { target() }

func target() {}
`,
	}, "p/foo.go", "target")

	assertNames(t, gainingNames(plan), []string{"target"}, "Funcs")
	got := argIn(t, plan, "caller")
	if got != "ctx" {
		t.Errorf("caller already has a ctx and must pass it, got %q", got)
	}
}

func TestThreadContext_CallerWithDifferentlyNamedCtx(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

import "context"

func caller(c context.Context) { target() }

func target() {}
`,
	}, "p/foo.go", "target")

	assertNames(t, gainingNames(plan), []string{"target"}, "Funcs")
	got := argIn(t, plan, "caller")
	if got != "c" {
		t.Errorf("the ctx expression must be the real parameter name, not a hardcoded ctx; got %q", got)
	}
}

// TestThreadContext_Diamond proves the visited set collapses the two paths that
// reach top: without it, top would be enqueued (and re-expanded) once per path.
func TestThreadContext_Diamond(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

func top() {
	left()
	right()
}

func left() { target() }

func right() { target() }

func target() {}
`,
	}, "p/foo.go", "target")

	assertNames(t, gainingNames(plan), []string{"left", "right", "target", "top"}, "Funcs")

	// Both arms thread, and top's two call sites both pass its injected ctx.
	var topArgs []string
	for _, edit := range plan.ArgFor {
		if edit.Site.Fn != nil && edit.Site.Fn.Name.Name == "top" {
			topArgs = append(topArgs, edit.Expr)
		}
	}
	if len(topArgs) != 2 {
		t.Fatalf("expected 2 call sites inside top, got %d", len(topArgs))
	}
	for _, a := range topArgs {
		if a != "ctx" {
			t.Errorf("top must pass its injected ctx to both arms, got %q", a)
		}
	}
	got := argIn(t, plan, "left")
	if got != "ctx" {
		t.Errorf("left must thread ctx, got %q", got)
	}
	got = argIn(t, plan, "right")
	if got != "ctx" {
		t.Errorf("right must thread ctx, got %q", got)
	}
}

func TestThreadContext_SelfRecursion(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

func f() { f() }
`,
	}, "p/foo.go", "f")

	assertNames(t, gainingNames(plan), []string{"f"}, "Funcs")
	got := argIn(t, plan, "f")
	if got != "ctx" {
		t.Errorf("the recursive call must pass f's own injected ctx, got %q", got)
	}
}

func TestThreadContext_MutualRecursion(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

func f() { g() }

func g() { f() }
`,
	}, "p/foo.go", "f")

	assertNames(t, gainingNames(plan), []string{"f", "g"}, "Funcs")
	got := argIn(t, plan, "f")
	if got != "ctx" {
		t.Errorf("f must pass its injected ctx, got %q", got)
	}
	got = argIn(t, plan, "g")
	if got != "ctx" {
		t.Errorf("g must pass its injected ctx, got %q", got)
	}
}

// TestThreadContext_HoistPastFakeRoot and TestThreadContext_RootWithLocalCtx are
// the pair that is easy to conflate: the same `ctx := context.Background()`
// statement is hoisted past in one and preserved in the other, decided purely by
// whether its function can legally gain a parameter.
func TestThreadContext_HoistPastFakeRoot(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

import "context"

func caller() {
	ctx := context.Background()
	_ = ctx
	target()
}

func target() {}
`,
	}, "p/foo.go", "target")

	assertNames(t, gainingNames(plan), []string{"caller", "target"}, "Funcs")
	assertNames(t, fakeNames(plan), []string{"caller"}, "Fake")
	got := argIn(t, plan, "caller")
	if got != "ctx" {
		t.Errorf("caller must pass its injected ctx, got %q", got)
	}
}

func TestThreadContext_RootWithLocalCtx(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"app/main.go": `package main

import "context"

func main() {
	ctx := context.Background()
	_ = ctx
	target()
}

func target() {}
`,
	}, "app/main.go", "target")

	// main cannot gain a parameter, so its local ctx is passed and kept.
	assertNames(t, gainingNames(plan), []string{"target"}, "Funcs")
	assertNames(t, fakeNames(plan), nil, "Fake")
	got := argIn(t, plan, "main")
	if got != "ctx" {
		t.Errorf("a root with a local ctx must pass it rather than fabricate a new one, got %q", got)
	}
}

// TestThreadContext_SeedAlreadyHasCtxAtNonZeroPosition guards against the
// first-parameter-only check that would add a second context.Context here.
func TestThreadContext_SeedAlreadyHasCtxAtNonZeroPosition(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

import "context"

func seed(a int, ctx context.Context) {
	_ = a
	_ = ctx
}

func caller() { seed(1, context.Background()) }
`,
	}, "p/foo.go", "seed")

	assertNames(t, gainingNames(plan), []string{"caller"}, "Funcs")
	got := argIn(t, plan, "caller")
	if got != "ctx" {
		t.Errorf("the caller must still be threaded and pass its injected ctx, got %q", got)
	}
}

func TestThreadContext_AbortsOnValueUse(t *testing.T) {
	err := threadErrFor(t, map[string]string{
		"p/foo.go": `package p

func Seed() {}

var h = Seed
`,
	}, "p/foo.go", "Seed", true)

	if !strings.Contains(err.Error(), "Seed") || !strings.Contains(err.Error(), "used as a value") {
		t.Fatalf("error must name the offending value use, got: %v", err)
	}
	if !strings.Contains(err.Error(), "foo.go") {
		t.Fatalf("error must name the file of the value use, got: %v", err)
	}
}

func TestThreadContext_AbortsWithoutTypeInfo(t *testing.T) {
	err := threadErrFor(t, map[string]string{
		"p/foo.go": `package p

func caller() { target() }

func target() {}
`,
	}, "p/foo.go", "target", false)

	if !strings.Contains(err.Error(), "type information") {
		t.Fatalf("an unchecked workspace must abort rather than silently no-op, got: %v", err)
	}
}

func TestThreadContext_ClosureIsARoot(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

func F() {
	go func() { target() }()
}

func target() {}
`,
	}, "p/foo.go", "target")

	// F is not climbed: the goroutine may outlive it, so F's (hypothetical) ctx
	// is not a sound source for the closure.
	assertNames(t, gainingNames(plan), []string{"target"}, "Funcs")
	got := argIn(t, plan, "F")
	if got != "context.TODO()" {
		t.Errorf("a closure with no ctx in scope is a root, got %q", got)
	}
}

func TestThreadContext_ClosureCapturingOuterCtx(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/foo.go": `package p

import "context"

func F(ctx context.Context) {
	go func() { target() }()
}

func target() {}
`,
	}, "p/foo.go", "target")

	assertNames(t, gainingNames(plan), []string{"target"}, "Funcs")
	got := argIn(t, plan, "F")
	if got != "ctx" {
		t.Errorf("a closure capturing a real outer ctx must use it, not TODO(); got %q", got)
	}
}

func TestThreadContext_SeedNotFound(t *testing.T) {
	err := threadErrFor(t, map[string]string{
		"p/foo.go": "package p\n",
	}, "p/foo.go", "nope", true)

	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error must name the unresolved seed, got: %v", err)
	}
}
