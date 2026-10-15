package refactor

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// ThreadContextOperation adds `ctx context.Context` as the first parameter of a
// seed function and then threads a real context up through every call chain in
// the workspace that reaches it, stopping only at a caller that already has a
// context or at a root that cannot take one (main, init, any function or
// method with a named *testing.T/B/F or testing.TB parameter, closures,
// package-level var initializers).
//
// This file owns the ANALYSIS half: it computes a validated plan-of-record
// (threadPlan) and emits no changes. Change emission lives in
// thread_context_render.go.
type ThreadContextOperation struct {
	FunctionName string // "Process" or "Type.Method"
	SourceFile   string

	// Scope is carried for parity with the other rename/thread requests, but the
	// traversal is deliberately workspace-wide: a signature change that is not
	// propagated to every caller leaves the workspace uncompilable, so limiting
	// the climb to one package could only ever produce a broken result.
	Scope pkgtypes.RenameScope

	CachedIndex *analysis.ReferenceIndex // may be nil; built inside Execute if so

	// Parser lets the operation type-check the workspace itself when CachedIndex
	// is nil. LoadWorkspace does not type-check and BuildReferenceIndex silently
	// skips packages with no type info, so without this the index would come back
	// empty and the whole traversal would "succeed" having found nothing. The
	// HasTypeInfo guard in buildPlan is the backstop when Parser is nil too.
	Parser *analysis.GoParser

	Logger *slog.Logger

	// AllowBreaking is populated by the engine from EngineConfig, mirroring
	// RenameFieldOperation. The value-use abort is deliberately NOT gated on it:
	// see valueUseAbort.
	AllowBreaking bool

	// Linked names functions that gain a ctx under another build configuration.
	// Each one this workspace declares is seeded alongside the seed, so platform
	// siblings and the call sites they share stay in agreement.
	Linked pkgtypes.SymbolKeySet

	// StopAt names functions the climb must not add a ctx to. Each acts as a
	// root: its calls pass an in-scope ctx or context.TODO().
	StopAt pkgtypes.SymbolKeySet

	// Reach, when set, stops the climb at every production caller the entry
	// program does not reach, like a stop.
	Reach *EntryReach

	plan *threadPlan // populated by Validate/Execute

	declIndex map[*gotypes.Func]*funcDecl

	// seed is the resolved seed object, kept so that an external-interface abort
	// can tell the user "the seed itself cannot be threaded" rather than the much
	// more confusing "something it reaches cannot be".
	seed *gotypes.Func

	// ifaceCache memoizes the workspace's interface survey. It is derived from
	// type info that does not change during a run, and expandInterfaces consults
	// it once per worklist entry.
	ifaceCache *ifaceSurvey
}

// EntryReach is the code an entry program runs.
type EntryReach struct {
	Entry    string
	Funcs    pkgtypes.SymbolKeySet // functions and methods the program reaches
	Bodiless map[string]bool       // packages with code the analysis could not see, and what they import; their callers count as reached
}

// outside reports whether f is declared in analysed code the program does not reach.
func (r *EntryReach) outside(f *gotypes.Func) bool {
	key, ok := pkgtypes.SymbolKeyOf(f)
	return ok && !r.Funcs.Has(key) && !r.Bodiless[key.PkgPath]
}

// funcDecl resolves a func object back to its declaration and its owning
// package/file. go/types offers no reverse mapping (and this workspace's
// type-checker does not populate Info.Scopes), so the mapping is built by
// walking every declaration once and reading pkg.TypesInfo.Defs.
//
// An INTERFACE method is declared as an *ast.Field inside an *ast.InterfaceType,
// not as an *ast.FuncDecl, so Decl cannot hold it. Rather than a parallel map
// keyed the same way, such a method is carried here with Decl nil and
// IfaceField/IfaceFuncType set. That keeps one lookup table for the fixpoint —
// declIndex/plan.Decls stay total over everything that can enter the worklist,
// so discover needs no interface special-case at all — and every consumer that
// needs the signature reads FuncType(), which is non-nil for both kinds.
type funcDecl struct {
	Decl *ast.FuncDecl
	Pkg  *pkgtypes.Package
	File *pkgtypes.File

	// IfaceField is the `Save(x int)` field inside an interface declaration, and
	// IfaceFuncType its signature. Both nil for an ordinary function.
	IfaceField    *ast.Field
	IfaceFuncType *ast.FuncType
}

// IsInterfaceMethod reports whether this declaration is an interface method
// field rather than an ordinary function declaration.
func (fd *funcDecl) IsInterfaceMethod() bool { return fd.Decl == nil && fd.IfaceFuncType != nil }

// FuncType returns the signature AST to rewrite, for either kind of
// declaration. This is the node whose Params.Opening receives the injected ctx.
func (fd *funcDecl) FuncType() *ast.FuncType {
	if fd.Decl != nil {
		return fd.Decl.Type
	}
	return fd.IfaceFuncType
}

// callEdit is the resolved decision for one call site: which expression must be
// passed as the ctx argument there.
type callEdit struct {
	Expr string
	Site analysis.CallSite
	// Callee is the function the plan visited that Site calls. Its object may
	// differ from the one Site's own type information resolves, which can be a
	// copy checked for an importer, so the plan is keyed by this one.
	Callee *gotypes.Func
}

// threadPlan is the plan-of-record: everything decided before any change is
// emitted. Rendering reads it and never re-derives any of these decisions.
type threadPlan struct {
	// Funcs maps each function that must GAIN a ctx param to the param name it
	// receives (usually "ctx"; uniqueCtxName yields ctx0/ctx1 on collision).
	Funcs map[*gotypes.Func]string
	// Decls resolves every function the traversal touched (Funcs plus a seed that
	// already had a context) back to its declaration.
	Decls map[*gotypes.Func]*funcDecl
	// Fake records top-level `ctx := context.Background()` statements to delete.
	// A statement is recorded exactly when its function joins Funcs: once ctx is
	// a parameter the `:=` is a redeclaration and will not compile. Roots never
	// join Funcs, so a root's local ctx is never deleted.
	Fake map[*gotypes.Func]*ast.AssignStmt
	// ArgFor maps a call site (keyed by its CallExpr's Lparen, which is unique
	// per call and is exactly where an argument is inserted) to the expression to
	// pass there.
	ArgFor map[token.Pos]*callEdit
	// Stops holds the declared stops whose presence changed the outcome: the
	// climb reached them and would otherwise have added a ctx.
	Stops map[*gotypes.Func]bool
	// Outside holds the callers the climb did not climb into because the entry
	// program does not reach them.
	Outside map[*gotypes.Func]bool
	// Renamed holds callers whose blank or unnamed context parameter is named
	// ctx instead of gaining a second one.
	Renamed map[*gotypes.Func]bool
}

// dropAnalysis forgets the index, declarations and plan-of-record the
// operation was planned with.
func (op *ThreadContextOperation) dropAnalysis() {
	op.CachedIndex = nil
	op.plan = nil
	op.declIndex = nil
	op.seed = nil
	op.ifaceCache = nil
}

func (op *ThreadContextOperation) Type() pkgtypes.OperationType {
	return pkgtypes.ThreadContextOperationType
}

func (op *ThreadContextOperation) Description() string {
	return fmt.Sprintf("Thread context.Context through every call chain reaching %s", op.FunctionName)
}

func (op *ThreadContextOperation) logger() *slog.Logger {
	if op.Logger != nil {
		return op.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (op *ThreadContextOperation) Validate(ws *pkgtypes.Workspace) error {
	plan, err := op.buildPlan(ws)
	if err != nil {
		return err
	}
	op.plan = plan
	return nil
}

func (op *ThreadContextOperation) Execute(ws *pkgtypes.Workspace) (*pkgtypes.RefactoringPlan, error) {
	if op.plan == nil {
		plan, err := op.buildPlan(ws)
		if err != nil {
			return nil, err
		}
		op.plan = plan
	}

	// Change emission is owned by thread_context_render.go; this half computes
	// the plan-of-record only.
	changes, err := op.RenderChanges(ws)
	if err != nil {
		return nil, err
	}

	// Defensive check at the plan boundary. thread_context_render.go's package
	// doc argues, and its own tests assert, that every change it emits is
	// either zero-width or a tightly bounded replacement that can never
	// contain another edit's offset — so two changes should never overlap by
	// construction. Re-checking that guarantee here means a regression in it
	// surfaces immediately as a clear error naming the two conflicting
	// changes, rather than as a much later and more opaque rejection from
	// Validator/Serializer.validateChangePositions deep inside ExecutePlan.
	if err := assertNoOverlappingChanges(changes); err != nil {
		return nil, err
	}

	affectedFiles := make([]string, 0, len(changes))
	for _, c := range changes {
		affectedFiles = append(affectedFiles, c.File)
	}

	return &pkgtypes.RefactoringPlan{
		Operations:    []pkgtypes.Operation{op},
		Changes:       changes,
		AffectedFiles: removeDuplicateStrings(affectedFiles),
		SkippedSites:  op.stopSites(),
		Reversible:    true,
	}, nil
}

// stopSites reports each effective stop as a skipped declaration, in file and
// line order.
func (op *ThreadContextOperation) stopSites() []pkgtypes.SkippedSite {
	var sites []pkgtypes.SkippedSite
	add := func(f *gotypes.Func, reason string) {
		fd := op.declIndex[f]
		if fd == nil || fd.File == nil {
			return
		}
		sites = append(sites, pkgtypes.SkippedSite{
			File:    fd.File.Path,
			Line:    offsetLine(fd.File, fd.Decl.Pos()),
			Reason:  reason + ": the context climb stopped here; calls inside pass the in-scope ctx or context.TODO()",
			Snippet: funcLabel(f),
		})
	}
	for f := range op.plan.Stops {
		add(f, "stop_at")
	}
	for f := range op.plan.Outside {
		if _, gains := op.plan.Funcs[f]; !gains {
			add(f, "outside entry")
		}
	}
	slices.SortFunc(sites, func(a, b pkgtypes.SkippedSite) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), strings.Compare(a.Snippet, b.Snippet))
	})
	return sites
}

// assertNoOverlappingChanges groups changes by file and reports an error
// naming the two conflicting changes (file, description, and byte range) the
// moment an overlap is found, rather than letting an opaque rejection surface
// later from Validator/Serializer. Mirrors Serializer.changesOverlap's own
// definition of overlap.
func assertNoOverlappingChanges(changes []pkgtypes.Change) error {
	byFile := make(map[string][]pkgtypes.Change)
	for _, c := range changes {
		byFile[c.File] = append(byFile[c.File], c)
	}
	for file, cs := range byFile {
		for i := range cs {
			for j := i + 1; j < len(cs); j++ {
				a, b := cs[i], cs[j]
				if a.Start < b.End && b.Start < a.End {
					return &pkgtypes.RefactorError{
						Type: pkgtypes.InvalidOperation,
						Message: fmt.Sprintf(
							"internal error: context threading produced overlapping changes in %s: "+
								"%q [%d,%d) conflicts with %q [%d,%d)",
							file, a.Description, a.Start, a.End, b.Description, b.Start, b.End),
						File: file,
					}
				}
			}
		}
	}
	return nil
}

// buildPlan runs both analysis phases and returns the plan-of-record, or an
// error if any abort condition fires. It emits nothing and mutates nothing.
func (op *ThreadContextOperation) buildPlan(ws *pkgtypes.Workspace) (*threadPlan, error) {
	idx := op.referenceIndex(ws)

	// Mandatory guard. An index built over a workspace that was never
	// type-checked has no object-identity data at all, so CallSitesOf returns
	// nothing for every function — the traversal would find no callers and report
	// a successful no-op instead of an error.
	if !idx.HasTypeInfo() {
		return nil, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: "workspace has no type information: context threading resolves callers by go/types " +
				"object identity and would silently find nothing; type-check the workspace first",
		}
	}

	op.declIndex = buildFuncDeclIndex(ws)

	seeds, err := op.seeds(ws)
	if err != nil {
		return nil, err
	}

	plan := &threadPlan{
		Funcs:  make(map[*gotypes.Func]string),
		Decls:  make(map[*gotypes.Func]*funcDecl),
		Fake:   make(map[*gotypes.Func]*ast.AssignStmt),
		ArgFor: make(map[token.Pos]*callEdit),
		Stops:  make(map[*gotypes.Func]bool),

		Outside: make(map[*gotypes.Func]bool),
		Renamed: make(map[*gotypes.Func]bool),
	}

	visited, err := op.discover(ws, idx, plan, seeds)
	if err != nil {
		return nil, err
	}
	if err := op.resolve(idx, plan, visited); err != nil {
		return nil, err
	}
	if err := op.unattributedTestsAbort(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// seeds returns the requested seed followed by every function Linked names in
// this workspace. A seed the workspace does not declare is an error only when
// no linked function stands in for it.
func (op *ThreadContextOperation) seeds(ws *pkgtypes.Workspace) ([]*gotypes.Func, error) {
	linked := op.linkedFuncs()
	seed, err := op.resolveSeed(ws)
	if err != nil {
		if len(linked) == 0 || !isSymbolNotFound(err) {
			return nil, err
		}
		return op.rejectStopSeeds(linked)
	}
	op.seed = seed
	return op.rejectStopSeeds(append([]*gotypes.Func{seed}, linked...))
}

// rejectStopSeeds fails when a seed is a declared stop: it is the function that
// gains the parameter, so it cannot also be where the climb stops.
func (op *ThreadContextOperation) rejectStopSeeds(seeds []*gotypes.Func) ([]*gotypes.Func, error) {
	for _, f := range seeds {
		if op.isStop(f) {
			return nil, &pkgtypes.RefactorError{
				Type: pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("cannot thread context through %s: it is the function that gains the "+
					"parameter and is also declared as a stop. No changes were made.", funcLabel(f)),
			}
		}
	}
	return seeds, nil
}

// isStop reports whether f is a declared stop.
func (op *ThreadContextOperation) isStop(f *gotypes.Func) bool {
	key, ok := pkgtypes.SymbolKeyOf(f)
	return ok && op.StopAt.Has(key)
}

// linkedFuncs returns the declared functions whose key is in Linked.
func (op *ThreadContextOperation) linkedFuncs() []*gotypes.Func {
	if len(op.Linked) == 0 {
		return nil
	}
	var out []*gotypes.Func
	for f := range op.declIndex {
		if key, ok := pkgtypes.SymbolKeyOf(f); ok && op.Linked.Has(key) {
			out = append(out, f)
		}
	}
	return out
}

// GainedKeys returns the keys of the functions that gain a ctx parameter in the
// built plan. Functions without a stable key are left out.
func (op *ThreadContextOperation) GainedKeys() pkgtypes.SymbolKeySet {
	keys := make(pkgtypes.SymbolKeySet)
	if op.plan == nil {
		return keys
	}
	for f := range op.plan.Funcs {
		if key, ok := pkgtypes.SymbolKeyOf(f); ok {
			keys[key] = struct{}{}
		}
	}
	return keys
}

// isSymbolNotFound reports whether err is, or wraps, a RefactorError of type
// SymbolNotFound: the seed's file or declaration is not in the workspace.
func isSymbolNotFound(err error) bool {
	var re *pkgtypes.RefactorError
	return errors.As(err, &re) && re.Type == pkgtypes.SymbolNotFound
}

// unattributedTestsAbort fails when a reached package had all its test files
// dropped from type-checking: call sites in them are invisible and cannot be
// listed for the user to fix by hand.
func (op *ThreadContextOperation) unattributedTestsAbort(plan *threadPlan) error {
	var blind []*pkgtypes.Package
	for _, pkg := range plan.reachedPackages() {
		if pkg.UnattributedTestErr != nil {
			blind = append(blind, pkg)
		}
	}
	if len(blind) == 0 {
		return nil
	}
	return unattributedTestsError(blind)
}

// reachedPackages returns, by directory, the packages that hold a function the
// traversal touched or a call site it rewrites.
func (plan *threadPlan) reachedPackages() []*pkgtypes.Package {
	byDir := make(map[string]*pkgtypes.Package)
	for _, fd := range plan.Decls {
		if fd.Pkg != nil {
			byDir[fd.Pkg.Path] = fd.Pkg
		}
	}
	for _, edit := range plan.ArgFor {
		if pkg := edit.Site.Pkg; pkg != nil {
			byDir[pkg.Path] = pkg
		}
	}
	out := make([]*pkgtypes.Package, 0, len(byDir))
	for _, dir := range slices.Sorted(maps.Keys(byDir)) {
		out = append(out, byDir[dir])
	}
	return out
}

// reach returns the package directories and changed function names of the
// built plan.
func (op *ThreadContextOperation) reach() reach {
	var r reach
	if op.plan == nil {
		return r
	}
	for _, pkg := range op.plan.reachedPackages() {
		r.dirs = append(r.dirs, pkg.Path)
	}
	for f := range op.plan.Funcs {
		r.names = append(r.names, f.Name())
	}
	return r
}

// discover is phase 1: the worklist fixpoint that decides WHICH functions gain a
// ctx parameter. It deliberately does not resolve any argument expression —
// discovery is monotone (a function only ever gains a ctx, never loses one), so
// an argument resolved during the walk could be invalidated later when a
// different branch pulls its enclosing function into Funcs. Resolving only after
// Funcs is final is what makes phase 2 correct.
//
// The returned set is every function the traversal touched, which is Funcs plus
// any seed that already carried a context.
func (op *ThreadContextOperation) discover(
	ws *pkgtypes.Workspace,
	idx *analysis.ReferenceIndex,
	plan *threadPlan,
	seeds []*gotypes.Func,
) (map[*gotypes.Func]bool, error) {
	visited := make(map[*gotypes.Func]bool)
	queue := slices.Clone(seeds)
	gen := newGeneratedReach()

	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		if visited[f] {
			continue // the visited set is what terminates cycles and diamonds
		}
		visited[f] = true

		fd := op.declIndex[f]
		if fd == nil {
			return nil, missingDeclError(ws, f)
		}
		// Before anything is planned or expanded: nothing inside generated code
		// may raise an abort that hides this one.
		if gen.isGaining(f, fd) {
			continue
		}
		plan.Decls[f] = fd

		if !funcHasContextParam(f) {
			plan.Funcs[f] = uniqueCtxName(fd.Pkg, fd.Decl)
			if stmt, ok := isFakeRootCtxCreation(fd.Decl); ok {
				plan.Fake[f] = stmt
			}
			uses := gen.handWrittenUses(ws, f, idx.NonCallUsesOf(f))
			if err := op.valueUseAbort(ws, f, uses); err != nil {
				return nil, err
			}
		}

		extra, err := op.expandInterfaces(f, ws, idx)
		if err != nil {
			return nil, err
		}
		for _, e := range extra {
			if op.isStop(e) {
				return nil, &pkgtypes.RefactorError{
					Type: pkgtypes.InvalidOperation,
					Message: fmt.Sprintf("cannot thread context through %s: expanding it reaches %s, which is "+
						"declared as a stop; the interface method would gain ctx while %s does not, so the "+
						"result would not compile. No changes were made.", funcLabel(f), funcLabel(e), funcLabel(e)),
				}
			}
		}
		gen.expanded(f, extra)
		queue = append(queue, extra...)

		for _, site := range idx.CallSitesOf(f) {
			if gen.isCallSite(f, site, op.siteLine(site)) {
				continue
			}
			caller, climb, err := op.climbTarget(idx, plan, site)
			if err != nil {
				return nil, err
			}
			if climb {
				gen.climbed(f, caller)
				queue = append(queue, caller)
			}
		}
	}
	if err := op.generatedAbort(ws, gen, seeds); err != nil {
		return nil, err
	}
	return visited, nil
}

// missingDeclError reports a worklist entry with no indexed declaration: what
// was looked up, where it is declared and why the index does not hold it.
func missingDeclError(ws *pkgtypes.Workspace, f *gotypes.Func) error {
	pos := ws.FileSet.Position(f.Pos())
	pkgPath := "(none)"
	if f.Pkg() != nil {
		pkgPath = f.Pkg().Path()
	}
	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf(
			"cannot thread context: no declaration is indexed for %s, declared at %s:%d in package %s: %s",
			funcLabel(f), pos.Filename, pos.Line, pkgPath, missingDeclReason(ws, f)),
		File: pos.Filename,
		Line: pos.Line,
	}
}

// missingDeclReason says why buildFuncDeclIndex holds no declaration for f.
func missingDeclReason(ws *pkgtypes.Workspace, f *gotypes.Func) string {
	if f.Pkg() == nil {
		return "it belongs to no package"
	}
	pkg := ws.Packages[ws.ImportToPath[strings.TrimSuffix(f.Pkg().Path(), "_test")]]
	switch {
	case pkg == nil:
		return "the workspace has no package with that import path"
	case pkg.TypesInfo == nil:
		return "the package has no type information"
	case f.Pkg() != pkg.TypesPkg && f.Pkg() != pkg.ExternalTestTypesPkg:
		return "the object belongs to a copy of the package checked for its importers, not to the package's own check"
	default:
		return "the package is type-checked, but defines no package-level function, method or interface method there"
	}
}

// climbTarget decides, for a single call site of a function that is gaining a
// ctx, whether the enclosing function must itself be climbed (and thus gain a
// ctx parameter of its own).
func (op *ThreadContextOperation) climbTarget(idx *analysis.ReferenceIndex, plan *threadPlan, site analysis.CallSite) (*gotypes.Func, bool, error) {
	// A call inside a closure is a threading root by product decision: a closure
	// may outlive or run outside its lexical caller (go/defer/callback), so its
	// enclosing FuncDecl is not a reliable place to source a context from.
	if site.Lit != nil {
		// Still consult findCtxSource, purely so that missing type info is caught
		// here in phase 1 rather than surfacing in phase 2 after Funcs is final.
		if src, _ := findCtxSource(site.Pkg, site.Fn, site.Call); src.Kind == ctxUnknown {
			return nil, false, op.unknownCtxAbort(site)
		}
		return nil, false, nil
	}

	// No enclosing function: a package-level var initializer. Nothing to climb.
	if site.Fn == nil {
		return nil, false, nil
	}

	src, found := findCtxSource(site.Pkg, site.Fn, site.Call)
	if src.Kind == ctxUnknown {
		return nil, false, op.unknownCtxAbort(site)
	}

	// Root-ness is checked BEFORE the fake-root hoist, and this ordering is the
	// whole point. A root such as main() or TestXxx() cannot be given a
	// parameter, so even when it holds a local `ctx := context.Background()` we
	// must pass that local and stop — not hoist past it and delete it. Reversing
	// these two checks silently strips main's context creation and produces code
	// that does not compile.
	cleanupHelper, err := op.calledFromCleanup(idx, site)
	if err != nil {
		return nil, false, err
	}
	if _, isRoot := classifyRoot(site.Pkg, site.File, site.Fn, nil, site.Call, cleanupHelper); isRoot {
		return nil, false, nil
	}

	// A local `ctx := context.Background()` IS in scope, so findCtxSource happily
	// reports it — but it is a fabricated root, not a threaded context. The
	// product decision is to hoist past it: the enclosing function gains a real
	// ctx parameter and the fake creation is deleted (recorded when the function
	// joins Funcs).
	hoist := found && src.Kind == ctxInScope && sourceIsFakeRoot(site, src)

	if found && !hoist {
		return nil, false, nil // a real context is already in scope here
	}

	caller, err := op.callerObject(site)
	if err != nil {
		return nil, false, err
	}

	if op.isStop(caller) {
		plan.Stops[caller] = true
		return nil, false, nil
	}
	if op.Reach != nil && !strings.HasSuffix(site.File.Path, "_test.go") && op.Reach.outside(caller) {
		plan.Outside[caller] = true
		return nil, false, nil
	}

	// The caller is about to gain a ctx parameter, but it already declares one
	// that the call site cannot reach. A blank or unnamed one is named ctx. A
	// named one shadowed by a local cannot be fixed by guessing: abort and let
	// the user un-shadow it.
	if funcHasContextParam(caller) {
		if renamed, err := op.renameBlankCtx(plan, caller, site); err != nil || renamed {
			return nil, false, err
		}
		return nil, false, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: fmt.Sprintf(
				"cannot thread context through %s at %s: it already has a context.Context parameter that "+
					"the call site cannot reference (unnamed, blank, or shadowed by a local); name or "+
					"un-shadow that parameter and re-run", funcLabel(caller), op.sitePos(site)),
			File: site.File.Path,
			Line: op.siteLine(site),
		}
	}

	return caller, true, nil
}

// resolve is phase 2: with Funcs final, walk every touched function's call sites
// again and pin down the exact expression each one must pass.
func (op *ThreadContextOperation) resolve(
	idx *analysis.ReferenceIndex,
	plan *threadPlan,
	visited map[*gotypes.Func]bool,
) error {
	for f := range visited {
		for _, site := range idx.CallSitesOf(f) {
			expr, err := op.argFor(idx, plan, site)
			if err != nil {
				return err
			}
			plan.ArgFor[site.Call.Lparen] = &callEdit{Expr: expr, Site: site, Callee: f}
		}
	}
	return nil
}

// argFor returns the expression to pass as ctx at site.
func (op *ThreadContextOperation) argFor(idx *analysis.ReferenceIndex, plan *threadPlan, site analysis.CallSite) (string, error) {
	// 1. The enclosing function is itself gaining a ctx parameter — pass it.
	//    Only when the call is directly in the FuncDecl body: a closure does not
	//    automatically get to use its enclosing declaration's new parameter,
	//    since phase 1 refused to climb through it.
	if site.Fn != nil && site.Lit == nil {
		caller, err := op.callerObject(site)
		if err != nil {
			return "", err
		}
		if name, ok := plan.Funcs[caller]; ok {
			return name, nil
		}
		if plan.Renamed[caller] {
			return ctxParamName, nil
		}
	}

	// 2/3. A context already reachable from the call site wins over any root
	//      literal. This is also what makes a root with a local ctx (main with
	//      `ctx := context.Background()`) pass its own ctx rather than a fresh
	//      context.TODO(), and what lets a closure use a captured outer ctx.
	if src, ok := findCtxSource(site.Pkg, site.Fn, site.Call); ok {
		return src.Expr, nil
	} else if src.Kind == ctxUnknown {
		return "", op.unknownCtxAbort(site)
	}

	// 4. A root: <param>.Context() for a testing parameter the call can still
	//    reference, context.TODO() for any other root.
	cleanupHelper, err := op.calledFromCleanup(idx, site)
	if err != nil {
		return "", err
	}
	if src, ok := classifyRoot(site.Pkg, site.File, site.Fn, site.Lit, site.Call, cleanupHelper); ok {
		return src.Expr, nil
	}

	// 5. A declared stop with no context in scope.
	if site.Fn != nil && site.Lit == nil {
		if caller, err := op.callerObject(site); err != nil {
			return "", err
		} else if op.isStop(caller) || plan.Outside[caller] {
			return "context.TODO()", nil
		}
	}

	// 6. Phase 1 climbed through this site, so its enclosing function must be in
	//    Funcs and case 1 must have matched. Report the inconsistency rather than
	//    quietly emitting context.TODO(), which would drop a real chain on the
	//    floor while reporting success.
	return "", &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf(
			"internal error: call site at %s has no context source and no enclosing function in the "+
				"threading plan", op.sitePos(site)),
		File: site.File.Path,
		Line: op.siteLine(site),
	}
}

// calledFromCleanup reports whether the function enclosing site, when the call
// is directly in its body, is called from inside a testing Cleanup closure
// anywhere in the workspace.
func (op *ThreadContextOperation) calledFromCleanup(idx *analysis.ReferenceIndex, site analysis.CallSite) (bool, error) {
	if site.Fn == nil || site.Lit != nil || !hasTestingTBParam(site.Pkg, site.Fn.Type) {
		return false, nil
	}
	caller, err := op.callerObject(site)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(idx.CallSitesOf(caller), func(s analysis.CallSite) bool {
		return inCleanupClosure(s.Pkg, s.Fn, s.Call)
	}), nil
}

// expandInterfaces returns any additional functions that must gain a ctx
// parameter alongside f: for a concrete method, the workspace interface methods
// it satisfies; for an interface method, every workspace implementation of it.
// Seeding one implementation therefore reaches all of its siblings in two hops
// (impl -> interface method -> every impl), which is why neither branch needs to
// enumerate both.
//
// Returning the INTERFACE method object (not just the implementations) is what
// makes calls made through the interface thread at all: the caller enqueues it,
// so CallSitesOf finds `var s Store; s.Save(x)` — whose Uses entry resolves to
// Store.Save, never to MemStore.Save — and climbs those callers in turn.
//
// The implementation machinery lives in thread_context_iface.go.
func (op *ThreadContextOperation) expandInterfaces(
	f *gotypes.Func,
	ws *pkgtypes.Workspace,
	idx *analysis.ReferenceIndex,
) ([]*gotypes.Func, error) {
	// Checked for every worklist entry, before anything is expanded. Satisfaction
	// of an interface declared outside the workspace is broken by the signature
	// change itself and cannot be repaired by rewriting anything we own: adding a
	// ctx to ServeHTTP silently stops the type satisfying http.Handler. This is an
	// abort rather than a SkippedSite because the product decision is that the
	// user must never be handed a half-threaded, uncompilable workspace.
	if err := op.externalInterfaceAbort(ws, f); err != nil {
		return nil, err
	}

	return op.interfaceSurvey(ws).expansionOf(f), nil
}

// valueUseAbort fails when f is referenced as a value rather than called.
//
// `var h = Seed`, `Register(Seed)` and method values all bind the function's
// current signature. CallSitesOf cannot see them and a signature change breaks
// every one. These are reported as a hard abort rather than a SkippedSite: a
// half-threaded workspace does not compile, so leaving the user to fix the
// remainder by hand is strictly worse than not starting.
func (op *ThreadContextOperation) valueUseAbort(
	ws *pkgtypes.Workspace,
	f *gotypes.Func,
	uses []analysis.ValueUse,
) error {
	if len(uses) == 0 {
		return nil
	}
	use := uses[0]
	pos := ws.FileSet.Position(use.Pos)
	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf(
			"cannot thread context through %s: it is used as a value (not called) at %s:%d:%d as %q"+
				"%s; adding a parameter would break that use, so no changes were made",
			funcLabel(f), pos.Filename, pos.Line, pos.Column, use.Ident.Name,
			moreUses(len(uses)-1)),
		File: use.File.Path,
		Line: pos.Line,
	}
}

func moreUses(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" (and %d more such use(s))", n)
}

func (op *ThreadContextOperation) unknownCtxAbort(site analysis.CallSite) error {
	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf(
			"cannot thread context: package %s has no type information, so the context in scope at %s "+
				"cannot be resolved; refusing to guess", site.Pkg.ImportPath, op.sitePos(site)),
		File: site.File.Path,
		Line: op.siteLine(site),
	}
}

func (op *ThreadContextOperation) sitePos(site analysis.CallSite) string {
	line := op.siteLine(site)
	if line == 0 {
		return site.File.Path
	}
	return fmt.Sprintf("%s:%d", site.File.Path, line)
}

func (op *ThreadContextOperation) siteLine(site analysis.CallSite) int {
	if site.File == nil || site.File.Package == nil {
		return 0
	}
	return offsetLine(site.File, site.Call.Pos())
}

// offsetLine derives a 1-based line number without a FileSet by counting
// newlines in the file's original content up to pos' offset. CallSite carries no
// FileSet, and the operation's error paths only need a human-readable location.
func offsetLine(file *pkgtypes.File, pos token.Pos) int {
	if file.AST == nil || len(file.OriginalContent) == 0 {
		return 0
	}
	offset := int(pos - file.AST.FileStart)
	if offset < 0 || offset > len(file.OriginalContent) {
		return 0
	}
	return 1 + strings.Count(string(file.OriginalContent[:offset]), "\n")
}

// callerObject resolves site.Fn to its canonical *gotypes.Func.
//
// Info.Scopes is not populated by this workspace's type-checker, so the only
// reliable FuncDecl -> object direction is TypesInfo.Defs on the declaration's
// name identifier.
func (op *ThreadContextOperation) callerObject(site analysis.CallSite) (*gotypes.Func, error) {
	if site.Pkg == nil || site.Pkg.TypesInfo == nil {
		return nil, op.unknownCtxAbort(site)
	}
	obj, _ := site.Pkg.TypesInfo.Defs[site.Fn.Name].(*gotypes.Func)
	if obj == nil {
		return nil, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: fmt.Sprintf(
				"cannot thread context: the function enclosing the call at %s could not be resolved to a "+
					"go/types object; its package may have failed to type-check", op.sitePos(site)),
			File: site.File.Path,
			Line: op.siteLine(site),
		}
	}
	return canonicalFunc(obj), nil
}

// sourceIsFakeRoot reports whether the context src names at site is exactly the
// variable declared by a top-level `ctx := context.Background()` in the
// enclosing function. Comparing declaration positions rather than the name
// alone matters: an inner block may shadow the fake ctx with a real one that is
// also called "ctx", and hoisting past that would discard a genuine context.
func sourceIsFakeRoot(site analysis.CallSite, src ctxSource) bool {
	fake, ok := isFakeRootCtxCreation(site.Fn)
	if !ok {
		return false
	}
	lhs, ok := fake.Lhs[0].(*ast.Ident)
	if !ok {
		return false
	}
	v := lookupVarAt(site.Pkg, src.Expr, site.Call.Pos())
	return v != nil && v.Pos() == lhs.Pos()
}

// lookupVarAt resolves the variable that name refers to at pos, using go/types'
// scope tree on the Package (which is populated even though Info.Scopes is not).
func lookupVarAt(pkg *pkgtypes.Package, name string, pos token.Pos) *gotypes.Var {
	if pkg == nil || pkg.TypesPkg == nil {
		return nil
	}
	_, inner := scopeAt(pkg, pos)
	if inner == nil {
		return nil
	}
	_, obj := inner.LookupParent(name, pos)
	v, _ := obj.(*gotypes.Var)
	return v
}

// funcHasContextParam reports whether f already accepts a context.Context in ANY
// position, not just the first. A first-position-only check silently adds a
// second context parameter to `func f(a int, ctx context.Context)`.
func funcHasContextParam(f *gotypes.Func) bool {
	sig, ok := f.Type().(*gotypes.Signature)
	if !ok {
		return false
	}
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if analysis.IsContextType(params.At(i).Type()) {
			return true
		}
	}
	return false
}

// canonicalFunc collapses a generic instantiation to its origin declaration.
// ReferenceIndex canonicalizes the objects it stores and the arguments it is
// given, so keying every map here on the origin keeps both sides consistent.
func canonicalFunc(f *gotypes.Func) *gotypes.Func {
	if orig := f.Origin(); orig != nil {
		return orig
	}
	return f
}

func funcLabel(f *gotypes.Func) string {
	sig, ok := f.Type().(*gotypes.Signature)
	if ok && sig.Recv() != nil {
		return fmt.Sprintf("%s.%s", gotypes.TypeString(sig.Recv().Type(), nil), f.Name())
	}
	return f.Name()
}

// buildFuncDeclIndex maps every type-checked function declaration in the
// workspace to its AST and owning package/file.
func buildFuncDeclIndex(ws *pkgtypes.Workspace) map[*gotypes.Func]*funcDecl {
	index := make(map[*gotypes.Func]*funcDecl)
	for _, pkg := range ws.Packages {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range allFilesOf(pkg) {
			if file.AST == nil {
				continue
			}
			for _, decl := range file.AST.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					if fd.Name == nil {
						continue
					}
					obj, _ := pkg.TypesInfo.Defs[fd.Name].(*gotypes.Func)
					if obj == nil {
						continue
					}
					index[canonicalFunc(obj)] = &funcDecl{Decl: fd, Pkg: pkg, File: file}
					continue
				}
				indexInterfaceMethods(index, decl, pkg, file)
			}
		}
	}
	return index
}

// indexInterfaceMethods adds every interface method declared by decl to the
// index. Interface methods must be resolvable exactly like ordinary functions:
// expandInterfaces enqueues them so that calls made THROUGH the interface get
// climbed, and discover aborts on any worklist entry it cannot resolve to a
// declaration.
func indexInterfaceMethods(
	index map[*gotypes.Func]*funcDecl,
	decl ast.Decl,
	pkg *pkgtypes.Package,
	file *pkgtypes.File,
) {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.TYPE {
		return
	}
	for _, spec := range gen.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok || iface.Methods == nil {
			continue
		}
		for _, field := range iface.Methods.List {
			// No name means an embedded interface (or a type-set element), which
			// declares no method of its own here — the embedded interface's own
			// declaration is indexed where it is written.
			if len(field.Names) == 0 {
				continue
			}
			ft, ok := field.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			obj, _ := pkg.TypesInfo.Defs[field.Names[0]].(*gotypes.Func)
			if obj == nil {
				continue
			}
			index[canonicalFunc(obj)] = &funcDecl{
				Pkg:           pkg,
				File:          file,
				IfaceField:    field,
				IfaceFuncType: ft,
			}
		}
	}
}

func allFilesOf(pkg *pkgtypes.Package) []*pkgtypes.File {
	files := make([]*pkgtypes.File, 0, len(pkg.Files)+len(pkg.TestFiles))
	for _, f := range pkg.Files {
		files = append(files, f)
	}
	for _, f := range pkg.TestFiles {
		files = append(files, f)
	}
	return files
}

// referenceIndex reuses the cached index or builds one, type-checking first when
// a parser is available. The HasTypeInfo assertion in buildPlan catches the case
// where neither happened.
func (op *ThreadContextOperation) referenceIndex(ws *pkgtypes.Workspace) *analysis.ReferenceIndex {
	if op.CachedIndex != nil {
		return op.CachedIndex
	}
	if op.Parser != nil {
		for _, pkg := range ws.Packages {
			op.Parser.EnsureTypeChecked(ws, pkg)
		}
	}
	return analysis.NewSymbolResolver(ws, op.logger()).BuildReferenceIndex()
}

// resolveSeed resolves FunctionName ("Process" or "Type.Method") in SourceFile
// to its canonical func object.
func (op *ThreadContextOperation) resolveSeed(ws *pkgtypes.Workspace) (*gotypes.Func, error) {
	file, pkg, err := op.findSourceFile(ws)
	if err != nil {
		return nil, err
	}
	// The name identifier to resolve: an ordinary declaration's, or — when the
	// seed names an interface method such as "Store.Save", which is an *ast.Field
	// and so invisible to FindFuncDeclByName — that field's.
	var seedIdent *ast.Ident
	if fd := analysis.FindFuncDeclByName(file.AST, op.FunctionName); fd != nil {
		seedIdent = fd.Name
	} else {
		seedIdent = findInterfaceMethodIdent(file.AST, op.FunctionName)
	}
	if seedIdent == nil {
		return nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("function %s not found in %s", op.FunctionName, file.Path),
			File:    file.Path,
		}
	}
	if pkg.TypesInfo == nil {
		return nil, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: fmt.Sprintf(
				"package %s has no type information; cannot resolve %s", pkg.ImportPath, op.FunctionName),
			File: file.Path,
		}
	}
	obj, _ := pkg.TypesInfo.Defs[seedIdent].(*gotypes.Func)
	if obj == nil {
		return nil, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: fmt.Sprintf(
				"could not resolve %s in %s to a go/types object; the package may have failed to type-check",
				op.FunctionName, file.Path),
			File: file.Path,
		}
	}
	return canonicalFunc(obj), nil
}

// fileHit is a source file matched by matchSourceFiles, paired with its package.
type fileHit struct {
	file *pkgtypes.File
	pkg  *pkgtypes.Package
}

// findSourceFile locates SourceFile, accepting an absolute path, the map key the
// loader used, or a workspace-relative path. Exact matches win outright; a
// suffix match is only consulted when nothing matched exactly, and is rejected
// when it is ambiguous rather than picking one arbitrarily.
func (op *ThreadContextOperation) findSourceFile(ws *pkgtypes.Workspace) (*pkgtypes.File, *pkgtypes.Package, error) {
	var hits []fileHit
	if op.SourceFile != "" {
		hits = matchSourceFiles(ws, op.SourceFile)
	}
	switch len(hits) {
	case 1:
		return hits[0].file, hits[0].pkg, nil
	case 0:
		return nil, nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("source file not found in workspace: %s", op.SourceFile),
			File:    op.SourceFile,
		}
	default:
		return nil, nil, &pkgtypes.RefactorError{
			Type: pkgtypes.InvalidOperation,
			Message: fmt.Sprintf(
				"ambiguous source file %q: matches %d files; pass a full path", op.SourceFile, len(hits)),
			File: op.SourceFile,
		}
	}
}

// matchSourceFiles returns the parsed files of ws matching path: an exact path
// or loader key, else those whose path ends in it. An empty path matches every
// parsed file.
func matchSourceFiles(ws *pkgtypes.Workspace, path string) []fileHit {
	var all, exact, loose []fileHit
	want := filepath.ToSlash(path)
	for _, pkg := range ws.Packages {
		for key, f := range allNamedFilesOf(pkg) {
			if f.AST == nil {
				continue
			}
			h := fileHit{file: f, pkg: pkg}
			all = append(all, h)
			switch {
			case f.Path == path || key == path:
				exact = append(exact, h)
			case strings.HasSuffix(filepath.ToSlash(f.Path), "/"+want):
				loose = append(loose, h)
			}
		}
	}
	switch {
	case path == "":
		return all
	case len(exact) > 0:
		return exact
	default:
		return loose
	}
}

// allNamedFilesOf returns a package's source and test files keyed as the loader
// keyed them.
func allNamedFilesOf(pkg *pkgtypes.Package) map[string]*pkgtypes.File {
	files := make(map[string]*pkgtypes.File, len(pkg.Files)+len(pkg.TestFiles))
	for k, f := range pkg.Files {
		files[k] = f
	}
	for k, f := range pkg.TestFiles {
		files[k] = f
	}
	return files
}
