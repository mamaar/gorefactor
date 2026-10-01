package refactor

import (
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// -- shared test helpers -----------------------------------------------------

// buildThreadPlanWithWorkspace runs the full ThreadContextOperation pipeline
// (buildPlan's discover+resolve fixpoint) and returns both the workspace and
// the plan, unlike thread_context_test.go's threadPlanFor which discards the
// workspace — render tests need ws to read file content and file-set offsets.
func buildThreadPlanWithWorkspace(t *testing.T, files map[string]string, sourceFile, funcName string) (*pkgtypes.Workspace, *threadPlan) {
	t.Helper()
	ws := buildScopeWorkspace(t, files)
	op := &ThreadContextOperation{FunctionName: funcName, SourceFile: sourceFile}
	if _, err := op.Execute(ws); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if op.plan == nil {
		t.Fatalf("Execute returned no plan")
	}
	return ws, op.plan
}

// findFunc locates the *gotypes.Func (and its declaration) for a top-level,
// non-method function named name, via the same buildFuncDeclIndex
// thread_context.go itself uses.
func findFunc(t *testing.T, ws *pkgtypes.Workspace, name string) (*gotypes.Func, *funcDecl) {
	t.Helper()
	idx := buildFuncDeclIndex(ws)
	for f, fd := range idx {
		if f.Name() == name && fd.Decl.Recv == nil {
			return f, fd
		}
	}
	t.Fatalf("function %s not found in workspace", name)
	return nil, nil
}

// originalContent returns the OriginalContent of the workspace file at path
// (checking both regular and test files).
func originalContent(t *testing.T, ws *pkgtypes.Workspace, path string) []byte {
	t.Helper()
	for _, pkg := range ws.Packages {
		if f, ok := pkg.Files[filepath.Base(path)]; ok && f.Path == path {
			return f.OriginalContent
		}
		if f, ok := pkg.TestFiles[filepath.Base(path)]; ok && f.Path == path {
			return f.OriginalContent
		}
		for _, f := range pkg.Files {
			if f.Path == path {
				return f.OriginalContent
			}
		}
		for _, f := range pkg.TestFiles {
			if f.Path == path {
				return f.OriginalContent
			}
		}
	}
	t.Fatalf("file %s not found in workspace", path)
	return nil
}

// changesInFile filters changes down to those targeting path.
func changesInFile(changes []pkgtypes.Change, path string) []pkgtypes.Change {
	var out []pkgtypes.Change
	for _, c := range changes {
		if c.File == path {
			out = append(out, c)
		}
	}
	return out
}

// renderedContent applies every change targeting path to that file's
// original content and returns the result, using the same descending-offset
// approach the real Serializer uses (see move_imports.go's
// applyChangesInMemory doc comment).
func renderedContent(t *testing.T, ws *pkgtypes.Workspace, path string, changes []pkgtypes.Change) string {
	t.Helper()
	content := originalContent(t, ws, path)
	return string(applyChangesInMemory(content, changesInFile(changes, path)))
}

// assertNoOverlaps groups changes by file and runs each group through the
// same Validator/Serializer machinery real plans go through
// (Serializer.validateChangePositions), per the brief's requirement that
// every emitted change set be checked this way.
func assertNoOverlaps(t *testing.T, changes []pkgtypes.Change) {
	t.Helper()
	byFile := make(map[string][]pkgtypes.Change)
	for _, c := range changes {
		byFile[c.File] = append(byFile[c.File], c)
	}
	s := NewSerializer()
	for file, cs := range byFile {
		if err := s.validateChangePositions(cs); err != nil {
			t.Errorf("overlapping changes in %s: %v", file, err)
		}
	}
}

// assertParses fails the test if src is not syntactically valid Go — a cheap
// proxy for "the applied changes did not corrupt the file".
func assertParses(t *testing.T, name, src string) {
	t.Helper()
	if _, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ParseComments); err != nil {
		t.Fatalf("rendered content does not parse: %v\n--- content ---\n%s", err, src)
	}
}

func wsPath(ws *pkgtypes.Workspace, rel string) string {
	return filepath.Join(ws.RootPath, filepath.FromSlash(rel))
}

func countImportChanges(changes []pkgtypes.Change, path string) int {
	n := 0
	for _, c := range changes {
		if c.File == path && strings.Contains(c.NewText, `import "context"`) {
			n++
		}
	}
	return n
}

// -- tests --------------------------------------------------------------

// TestRenderThreadPlan_GenericsVariadicsFuncParam is the headline test: it
// proves insertion, not signature re-synthesis, is what runs. A rebuilt
// signature (as ChangeSignatureOperation.generateNewSignature would produce)
// corrupts a func-typed parameter to func(...) and drops [T any] entirely;
// a zero-width insertion at Params.Opening+1 cannot touch either.
func TestRenderThreadPlan_GenericsVariadicsFuncParam(t *testing.T) {
	path := "p/foo.go"
	ws := buildScopeWorkspace(t, map[string]string{
		path: `package p

func F[T any](fn func(int) error, xs ...string) {}
`,
	})
	f, fd := findFunc(t, ws, "F")

	plan := &threadPlan{
		Funcs:  map[*gotypes.Func]string{f: "ctx"},
		Decls:  map[*gotypes.Func]*funcDecl{f: fd},
		Fake:   map[*gotypes.Func]*ast.AssignStmt{},
		ArgFor: map[token.Pos]*callEdit{},
	}

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "func F[T any](ctx context.Context, fn func(int) error, xs ...string) {}") {
		t.Fatalf("generics/variadic/func-param signature corrupted, got:\n%s", got)
	}
	if !strings.Contains(got, `import "context"`) {
		t.Errorf("expected context import to be added, got:\n%s", got)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes (signature insert + import), got %d: %+v", len(changes), changes)
	}
}

// TestRenderThreadPlan_EmptyParamList checks the no-stray-comma case: an
// empty existing parameter list must not gain a leading/trailing comma.
func TestRenderThreadPlan_EmptyParamList(t *testing.T) {
	path := "p/foo.go"
	ws := buildScopeWorkspace(t, map[string]string{
		path: `package p

func f() {}
`,
	})
	f, fd := findFunc(t, ws, "f")

	plan := &threadPlan{
		Funcs:  map[*gotypes.Func]string{f: "ctx"},
		Decls:  map[*gotypes.Func]*funcDecl{f: fd},
		Fake:   map[*gotypes.Func]*ast.AssignStmt{},
		ArgFor: map[token.Pos]*callEdit{},
	}

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "func f(ctx context.Context) {}") {
		t.Fatalf("expected no stray comma on an empty param list, got:\n%s", got)
	}
}

// TestRenderThreadPlan_NestedBothThreaded builds the plan by hand (bypassing
// discover/resolve, which is not what this file tests) to prove the
// zero-width no-overlap design: Save(Load(id)) with BOTH Save and Load
// threaded renders as two independent insertions and applies cleanly to
// Save(ctx, Load(ctx, id)) — no outermost-call detection, no splicing.
func TestRenderThreadPlan_NestedBothThreaded(t *testing.T) {
	path := "p/foo.go"
	ws := buildScopeWorkspace(t, map[string]string{
		path: `package p

func caller() {
	Save(Load(1))
}

func Save(id int) {}

func Load(id int) int { return id }
`,
	})

	saveF, saveFd := findFunc(t, ws, "Save")
	loadF, loadFd := findFunc(t, ws, "Load")
	_, callerFd := findFunc(t, ws, "caller")

	saveCall := findCallTo(callerFd.Decl, "Save")
	loadCall := findCallTo(callerFd.Decl, "Load")
	if saveCall == nil || loadCall == nil {
		t.Fatalf("expected calls to Save and Load inside caller, got Save=%v Load=%v", saveCall, loadCall)
	}

	plan := &threadPlan{
		Funcs: map[*gotypes.Func]string{saveF: "ctx", loadF: "ctx"},
		Decls: map[*gotypes.Func]*funcDecl{saveF: saveFd, loadF: loadFd},
		Fake:  map[*gotypes.Func]*ast.AssignStmt{},
		ArgFor: map[token.Pos]*callEdit{
			saveCall.Lparen: {Expr: "ctx", Callee: saveF, Site: analysis.CallSite{
				File: callerFd.File, Pkg: callerFd.Pkg, Call: saveCall, Fn: callerFd.Decl,
			}},
			loadCall.Lparen: {Expr: "ctx", Callee: loadF, Site: analysis.CallSite{
				File: callerFd.File, Pkg: callerFd.Pkg, Call: loadCall, Fn: callerFd.Decl,
			}},
		},
	}

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)
	if len(changes) != 5 { // 2 signatures + 2 call inserts + 1 import
		t.Fatalf("expected 5 changes, got %d: %+v", len(changes), changes)
	}

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "Save(ctx, Load(ctx, 1))") {
		t.Fatalf("expected Save(ctx, Load(ctx, 1)), got:\n%s", got)
	}
	if !strings.Contains(got, "func Save(ctx context.Context, id int) {}") {
		t.Fatalf("Save signature not threaded correctly, got:\n%s", got)
	}
	if !strings.Contains(got, "func Load(ctx context.Context, id int) int { return id }") {
		t.Fatalf("Load signature not threaded correctly, got:\n%s", got)
	}
}

// TestRenderThreadPlan_NoArgCall checks f() -> f(ctx).
func TestRenderThreadPlan_NoArgCall(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

func f() { f() }
`,
	}, path, "f")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "func f(ctx context.Context) { f(ctx) }") {
		t.Fatalf("expected f(ctx) no-arg call insertion, got:\n%s", got)
	}
}

// TestRenderThreadPlan_VariadicCall checks f(a, b...) -> f(ctx, a, b...).
func TestRenderThreadPlan_VariadicCall(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

func caller(a string, b []string) { f(a, b...) }

func f(id string, xs ...string) {}
`,
	}, path, "f")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "f(ctx, a, b...)") {
		t.Fatalf("expected variadic call site f(ctx, a, b...), got:\n%s", got)
	}
	if !strings.Contains(got, "func caller(ctx context.Context, a string, b []string)") {
		t.Fatalf("caller must also be threaded, got:\n%s", got)
	}
}

// TestRenderThreadPlan_ReplaceRootContext covers the replacement case: the
// callee (seed) already has a context.Context parameter (at a non-zero
// index), and the caller passes a fabricated root — context.Background()
// must be replaced with the caller's own injected ctx.
func TestRenderThreadPlan_ReplaceRootContext(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

import "context"

func seed(a int, ctx context.Context) {
	_ = a
	_ = ctx
}

func caller() { seed(1, context.Background()) }
`,
	}, path, "seed")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "func caller(ctx context.Context) { seed(1, ctx) }") {
		t.Fatalf("expected context.Background() replaced by ctx, got:\n%s", got)
	}
	if strings.Contains(got, "context.Background()") {
		t.Fatalf("fabricated root context.Background() must be gone, got:\n%s", got)
	}
	// The file already imports "context" (for seed's own pre-existing
	// signature) — no duplicate import may be added.
	if n := strings.Count(got, `import "context"`); n != 1 {
		t.Fatalf("expected exactly 1 \"context\" import, got %d:\n%s", n, got)
	}
}

// TestRenderThreadPlan_InternalFabricatedCtx covers the shape
// detect_missing_context_params actually reports: the seed takes no context
// and fabricates one inline as an argument to the I/O call below it. Adding
// the parameter without rewriting that argument would leave the seed with an
// unused ctx and the caller's real context still not reaching the I/O — the
// threading would compile and do nothing.
func TestRenderThreadPlan_InternalFabricatedCtx(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

import "context"

func fetch(ctx context.Context, key string) string { return key }

func seed(key string) string {
	return fetch(context.TODO(), key)
}

func caller(c context.Context) string { return seed("k") }
`,
	}, path, "seed")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "func seed(ctx context.Context, key string) string {") {
		t.Fatalf("expected seed to gain a ctx parameter, got:\n%s", got)
	}
	if !strings.Contains(got, "return fetch(ctx, key)") {
		t.Fatalf("expected the fabricated context.TODO() to become ctx, got:\n%s", got)
	}
	if strings.Contains(got, "context.TODO()") {
		t.Fatalf("fabricated context.TODO() must be gone, got:\n%s", got)
	}
	// The caller already holds a real context, so threading stops there and
	// passes it straight through.
	if !strings.Contains(got, `return seed(c, "k")`) {
		t.Fatalf("expected the caller's real ctx to be passed to seed, got:\n%s", got)
	}
}

// TestRenderThreadPlan_InternalFabricatedCtxNotInClosure pins the boundary
// renderInternalCtxUses draws: a context fabricated inside a nested closure is
// left alone. The closure may outlive the call that built it, so its
// context.Background() can be a deliberate detachment rather than a stand-in
// for a parameter that did not exist.
func TestRenderThreadPlan_InternalFabricatedCtxNotInClosure(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

import "context"

func fetch(ctx context.Context, key string) string { return key }

func seed(key string) string {
	go func() {
		_ = fetch(context.Background(), key)
	}()
	return fetch(context.TODO(), key)
}

func caller(c context.Context) string { return seed("k") }
`,
	}, path, "seed")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "_ = fetch(context.Background(), key)") {
		t.Fatalf("closure's own context.Background() must survive, got:\n%s", got)
	}
	if !strings.Contains(got, "return fetch(ctx, key)") {
		t.Fatalf("expected the non-closure context.TODO() to become ctx, got:\n%s", got)
	}
}

// TestRenderThreadPlan_InternalCtxDoesNotDoubleEdit guards the overlap the new
// step could otherwise cause: renderCallEdit's case (b) already replaces the
// fabricated argument at a call to a function that kept its own ctx parameter,
// and that call site can sit inside a body this step also scans. Only one
// change may be emitted for those bytes.
func TestRenderThreadPlan_InternalCtxDoesNotDoubleEdit(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

import "context"

func seed(a int, ctx context.Context) {
	_ = a
	_ = ctx
}

func caller() { seed(1, context.Background()) }
`,
	}, path, "seed")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)

	if !strings.Contains(got, "func caller(ctx context.Context) { seed(1, ctx) }") {
		t.Fatalf("expected exactly one rewrite of the fabricated root, got:\n%s", got)
	}
}

// TestRenderThreadPlan_NoChangeForRealCtx is the negative twin of the above:
// when the caller already passes a real ctx (not a fabricated root), nothing
// must be emitted for that call site — and since that also means the caller
// never needs to climb, nothing must be emitted at all.
func TestRenderThreadPlan_NoChangeForRealCtx(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

import "context"

func seed(a int, ctx context.Context) {
	_ = a
	_ = ctx
}

func caller(c context.Context) { seed(1, c) }
`,
	}, path, "seed")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	if len(changes) != 0 {
		t.Fatalf("expected no changes when the caller already passes a real ctx, got %+v", changes)
	}
}

// TestRenderThreadPlan_ImportOnlySignature is the addDefaultValueImports
// regression this brief calls out: a file that receives ONLY a signature
// change (no call-site change at all, because the seed has no callers) must
// still get the "context" import. change_signature.go's
// addDefaultValueImports would miss this (it only fires on a Description
// string containing "call to").
func TestRenderThreadPlan_ImportOnlySignature(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

func Leaf() {}
`,
	}, path, "Leaf")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	if countImportChanges(changes, full) != 1 {
		t.Fatalf("expected exactly 1 import-adding change for %s, changes: %+v", full, changes)
	}
	if len(changes) != 2 { // signature insert + import
		t.Fatalf("expected 2 changes (signature + import), got %d: %+v", len(changes), changes)
	}

	got := renderedContent(t, ws, full, changes)
	assertParses(t, path, got)
	if !strings.Contains(got, "func Leaf(ctx context.Context) {}") {
		t.Fatalf("expected Leaf's signature to gain ctx, got:\n%s", got)
	}
}

// TestRenderThreadPlan_ImportNotDuplicated checks that a file which already
// imports "context" does not get a second import change.
func TestRenderThreadPlan_ImportNotDuplicated(t *testing.T) {
	path := "p/foo.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		path: `package p

import "context"

// other keeps the "context" import used, so the file is exactly what a real
// pre-existing import looks like (an unused import would fail to compile
// and never reach the type-checker in the first place).
func other(ctx context.Context) { _ = ctx }

func Leaf() {}
`,
	}, path, "Leaf")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	full := wsPath(ws, path)
	if countImportChanges(changes, full) != 0 {
		t.Fatalf("import must not be duplicated, changes: %+v", changes)
	}
	if len(changes) != 1 {
		t.Fatalf("expected exactly 1 change (signature only), got %d: %+v", len(changes), changes)
	}
}

// TestRenderThreadPlan_NoImportForPlainIdentifier proves a file that
// receives only a call-site edit whose expression is not a context.X
// expression (here, t.Context() — capital C, no "context." substring, and
// "testing" is already imported for the TestX declaration itself) gets no
// "context" import, even though the sibling file declaring the threaded
// function does.
func TestRenderThreadPlan_NoImportForPlainIdentifier(t *testing.T) {
	targetPath := "p/target.go"
	testPath := "p/main_test.go"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		targetPath: `package p

func target() {}
`,
		testPath: `package p

import "testing"

func TestX(t *testing.T) { target() }
`,
	}, targetPath, "target")

	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	assertNoOverlaps(t, changes)

	fullTarget := wsPath(ws, targetPath)
	fullTest := wsPath(ws, testPath)

	if countImportChanges(changes, fullTarget) != 1 {
		t.Fatalf("expected target.go to get the context import, changes: %+v", changes)
	}
	if countImportChanges(changes, fullTest) != 0 {
		t.Fatalf("main_test.go must not get a context import (only a plain t.Context() call edit), changes: %+v", changes)
	}

	gotTest := renderedContent(t, ws, fullTest, changes)
	assertParses(t, testPath, gotTest)
	if !strings.Contains(gotTest, "target(t.Context())") {
		t.Fatalf("expected target(t.Context()) in the test file, got:\n%s", gotTest)
	}

	gotTarget := renderedContent(t, ws, fullTarget, changes)
	assertParses(t, targetPath, gotTarget)
	if !strings.Contains(gotTarget, "func target(ctx context.Context) {}") {
		t.Fatalf("expected target's signature to gain ctx, got:\n%s", gotTarget)
	}
}
