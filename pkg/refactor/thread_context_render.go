package refactor

import (
	"fmt"
	"go/ast"
	"go/token"
	gotypes "go/types"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/analysis"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// This file owns the RENDER half of context threading: it turns the
// plan-of-record built by thread_context.go's buildPlan into concrete
// pkgtypes.Change values. It never re-derives any decision the plan already
// made (which functions gain a parameter, what expression each call site
// must pass, which fake roots must go) — it only converts those decisions
// into byte-offset edits.
//
// THE CORE DESIGN DECISION: every edit this file emits is either a
// zero-width point insertion, or (in exactly one case, described below) a
// tightly-bounded replacement of a single argument expression. There is no
// "rebuild the signature" and no "splice around the nested call" logic
// anywhere in this file, and there must never be:
//
//   - A signature change is inserted at fn.Type.Params.Opening + 1 (the byte
//     immediately after "("), never synthesized from stringified types. See
//     insertCtxParam for why: ChangeSignatureOperation.generateNewSignature
//     round-trips types through strings and drops Type.TypeParams entirely —
//     it erases generics. A zero-width insertion cannot do that: every original byte, including
//     comments, generics, and variadics, survives untouched.
//   - A call-site edit that ADDS an argument (the callee is gaining its own
//     ctx parameter) is inserted at call.Lparen + 1, for the same reason.
//   - Two zero-width changes at two different offsets never overlap, by
//     Validator.changesOverlap's own definition (a.Start < b.End && b.Start
//     < a.End is false whenever a.Start == a.End and a.Start != b.Start).
//     That is what makes a nested call like Save(Load(id)), where both Save
//     and Load are threaded, safe to render as two completely independent
//     insertions with no notion of "outermost call" or substring splicing:
//     applying both yields Save(ctx, Load(ctx, id)) for free.
//
// The non-zero-width edits this file emits are the call-site REPLACEMENT case
// (renderCallEdit's case b), renderFakeDeletion, and renderInternalCtxUses.
// The first two are bounded tightly enough that they can never contain another
// edit's offset: see their doc comments. renderInternalCtxUses is the one that
// cannot argue that from its own shape — it replaces a context.TODO() call
// expression that a case (b) replacement or a fake deletion may have claimed
// already — so it is rendered last and drops any candidate overlapping a
// change already decided.

// RenderChanges converts the plan built by Validate/Execute into the
// concrete Changes that apply context threading. It is the seam
// thread_context.go's Execute will call once wired in (this file
// deliberately does not modify thread_context.go — see its package doc).
func (op *ThreadContextOperation) RenderChanges(ws *pkgtypes.Workspace) ([]pkgtypes.Change, error) {
	if op.plan == nil {
		return nil, fmt.Errorf("thread context plan not built: call Validate or Execute first")
	}
	return renderThreadPlan(ws, op.plan)
}

// renderThreadPlan is the actual renderer; split out from the method above
// so tests can drive it directly against a hand-built or op.Execute-derived
// plan without needing a live *ThreadContextOperation.
func renderThreadPlan(ws *pkgtypes.Workspace, plan *threadPlan) ([]pkgtypes.Change, error) {
	var changes []pkgtypes.Change

	// needsContext tracks, per file path, whether some emitted change in that
	// file mentions "context." (a signature's "ctx context.Context", or a
	// context.TODO()/context.Background() call-site expression) and so needs
	// the import. Tracked explicitly here — rather than inferred later from
	// Change.Description, which is what change_signature.go's
	// addDefaultValueImports does via a Description string-sniff — because
	// that sniff never fires for a file that received only a signature
	// change (its Description never contains "call to"), which is exactly
	// the common case here: the declaring file and the calling files usually
	// differ.
	needsContext := make(map[string]bool)

	// 1. Signatures: every function in plan.Funcs gains its injected name as
	// a new first parameter, in its own declaring file. fd.FuncType() covers
	// both an ordinary function/method (fd.Decl.Type) and an interface
	// method (fd.IfaceFuncType) uniformly — see funcDecl's doc comment in
	// thread_context.go.
	for f, name := range plan.Funcs {
		fd, ok := plan.Decls[f]
		if !ok {
			return nil, fmt.Errorf(
				"internal error: %s is in threadPlan.Funcs but has no resolvable declaration", funcLabel(f))
		}
		ft := fd.FuncType()
		if ft == nil || ft.Params == nil {
			return nil, fmt.Errorf(
				"internal error: %s is in threadPlan.Funcs but has no resolvable signature", funcLabel(f))
		}
		changes = append(changes, insertCtxParam(ws, fd.File, ft, name, bodyUsesCtx(plan, f, fd))...)
		needsContext[fd.File.Path] = true
	}

	for f := range plan.Renamed {
		changes = append(changes, renderBlankCtx(ws, plan.Decls[f])...)
	}

	// 2. Call sites: for every recorded call-site decision, either insert a
	// new argument or replace an existing fabricated-root one.
	for _, edit := range plan.ArgFor {
		if plan.WithoutCancel && strings.Contains(edit.Expr, "context.") && contextShadowed(edit.Site.Pkg, edit.Site.Call.Pos()) {
			plan.Notes = append(plan.Notes, pkgtypes.SkippedSite{
				File:    edit.Site.File.Path,
				Line:    offsetLine(edit.Site.File, edit.Site.Call.Pos()),
				Reason:  fmt.Sprintf("shadowed context: %s cannot be written here, so the call to %s needs a ctx argument by hand", edit.Expr, funcLabel(edit.Callee)),
				Snippet: funcLabel(edit.Callee),
			})
			continue
		}
		change, importFile, err := renderCallEdit(ws, plan, edit)
		if err != nil {
			return nil, fmt.Errorf("render thread plan: %w", err)
		}
		if change != nil {
			changes = append(changes, *change)
		}
		if importFile != "" {
			needsContext[importFile] = true
		}
	}

	// 3. Fake-root deletions: `ctx := context.Background()`/`context.TODO()`
	// statements that would become illegal redeclarations now that ctx is a
	// parameter.
	var err error
	changes, err = renderFakeRootDeletions(plan, ws, changes)
	if err != nil {
		return nil, err
	}

	// 4. Internal fabricated contexts: a function that GAINS a ctx parameter
	// has no reason left to fabricate one in its own body. Step 3 above
	// handles the `ctx := context.TODO()` statement shape; this handles the
	// other one — an inline context.TODO()/context.Background() passed
	// straight to a downstream call, which is precisely what
	// detect_missing_context_params reports as the reason the function needed
	// a parameter in the first place. Without this the threading is inert
	// exactly where it was aimed: the seed would gain an unused ctx parameter
	// while the caller's real context still never reached the I/O below it.
	changes = append(changes, renderInternalCtxUses(ws, plan, changes)...)

	// 5. Imports: add "context" to every file that needs it and doesn't
	// already have it. Two changes at the same insertion offset would
	// overlap, so this is done once per file, after every other change has
	// been decided.
	//
	// Step 4 never removes the need for the import in a file it edits: it only
	// rewrites bodies of functions that are gaining `ctx context.Context` in
	// step 1, so the declaring file still mentions "context." afterwards.
	for path, needed := range needsContext {
		if !needed || hasImport(ws, path, "context") {
			continue
		}
		imp := GenerateAddImportChange(ws, path, "", "context")
		if imp != nil {
			changes = append(changes, *imp)
		}
	}

	return changes, nil
}

func renderFakeRootDeletions(plan *threadPlan, ws *pkgtypes.Workspace, changes []pkgtypes.Change) ([]pkgtypes.Change, error) {
	for f, stmt := range plan.Fake {
		fd, ok := plan.Decls[f]
		if !ok {
			return changes, fmt.Errorf(
				"internal error: %s has a recorded fake-root deletion but no resolvable declaration", funcLabel(f))
		}
		del := renderFakeDeletion(ws, fd.File, stmt, funcLabel(f))
		if plan.WithoutCancel {
			del = renderFakeWithoutCancel(ws, fd.File, stmt, plan.Funcs[f], funcLabel(f))
		}
		// Assert, per the plan, that this deletion cannot swallow an
		// insertion already emitted. It never can in practice: the deleted
		// range is exactly `ctx := context.Background()` (or ...TODO()),
		// and that call takes no arguments, so no target call site — which
		// is what every insertion offset points at — can lexically sit
		// inside it. Assert rather than assume.
		for _, existing := range changes {
			if existing.File != del.File || existing.Start != existing.End || existing.Start < del.Start || existing.Start >= del.End {
				continue
			}
			return changes, fmt.Errorf(
				"internal error: fake ctx deletion in %s [%d,%d) unexpectedly contains an insertion at %d",
				del.File, del.Start, del.End, existing.Start)
		}
		changes = append(changes, del)
	}
	return changes, nil
}

// renderInternalCtxUses rewrites the fabricated root contexts left inside the
// bodies of the functions that are gaining a ctx parameter, replacing each
// with that parameter's name. This is the one place the renderer looks
// DOWNWARD out of a threaded function rather than upward at its callers.
//
// The scope is deliberately narrow:
//
//   - Only functions in plan.Funcs, i.e. only those actually gaining a
//     parameter. A root — main, a test, any caller that keeps its own
//     context — is left alone: its context.Background() is the real base of
//     the tree, not a stand-in for one.
//   - Only ordinary declarations with a body. An interface method has none,
//     and fd.Decl is nil for it.
//   - Never inside a nested *ast.FuncLit. A closure may outlive the call that
//     built it (`go func() { work(context.Background()) }()` is plausibly
//     detached on purpose), and it may bind its own ctx that shadows the new
//     parameter. This is the same boundary RemoveInternalCtxCreation draws.
//
// emitted is every change decided so far. A candidate overlapping one of them
// is dropped, and that is not hypothetical: renderCallEdit's case (b) already
// replaces a fabricated argument at a call site that can sit inside one of
// these very bodies, and step 3 deletes a whole `ctx := context.TODO()`
// statement whose right-hand side is itself a root context call. In both cases
// the already-emitted change is the more specific one, so it wins.
func renderInternalCtxUses(ws *pkgtypes.Workspace, plan *threadPlan, emitted []pkgtypes.Change) []pkgtypes.Change {
	var changes []pkgtypes.Change

	for f, name := range plan.Funcs {
		fd, ok := plan.Decls[f]
		if !ok || fd.Decl == nil || fd.Decl.Body == nil || fd.File == nil {
			continue
		}

		for _, call := range internalRootContextCalls(fd.Decl.Body, plan.WithoutCancel) {
			start := ws.FileSet.Position(call.Pos()).Offset
			end := ws.FileSet.Position(call.End()).Offset
			old := string(fd.File.OriginalContent[start:end])
			text := name
			if plan.WithoutCancel {
				text = "context.WithoutCancel(" + name + ")"
			}
			changes = append(changes, pkgtypes.Change{
				File:    fd.File.Path,
				Start:   start,
				End:     end,
				OldText: old,
				NewText: text,
				Description: fmt.Sprintf(
					"Replace fabricated %s with the new %s parameter in %s", old, name, funcLabel(f)),
			})
		}
	}

	kept := changes[:0]
	for _, c := range changes {
		if !overlapsAny(c, emitted) {
			kept = append(kept, c)
		}
	}

	// plan.Funcs is a map, so the order candidates were discovered in is not
	// stable across runs. Sort so the emitted change list — and the preview
	// diff built from it — is deterministic.
	slices.SortFunc(kept, func(a, b pkgtypes.Change) int {
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		return a.Start - b.Start
	})

	return kept
}

// internalRootContextCalls returns the root context calls of body, outside
// nested function literals unless intoClosures.
func internalRootContextCalls(body *ast.BlockStmt, intoClosures bool) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		_, isLit := n.(*ast.FuncLit)
		if isLit && !intoClosures {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !isRootContextCall(call) {
			return true
		}
		calls = append(calls, call)
		// A root context call takes no arguments, so nothing nested inside.
		return false
	})
	return calls
}

func hasInternalRootContext(body *ast.BlockStmt) bool {
	return len(internalRootContextCalls(body, false)) > 0
}

// overlapsAny reports whether c intersects any change in others, using the
// same definition of overlap as assertNoOverlappingChanges and
// Serializer.changesOverlap.
func overlapsAny(c pkgtypes.Change, others []pkgtypes.Change) bool {
	for _, o := range others {
		if o.File == c.File && c.Start < o.End && o.Start < c.End {
			return true
		}
	}
	return false
}

// insertCtxParam builds the zero-width Changes that add `<name> context.Context`
// as the first parameter of ft, a function or interface method type.
//
// Go forbids mixing named and unnamed parameters. Over an unnamed list the
// context stays unnamed unless the body uses it, in which case it is named and
// every other parameter becomes `_`.
func insertCtxParam(ws *pkgtypes.Workspace, file *pkgtypes.File, ft *ast.FuncType, name string, used bool) []pkgtypes.Change {
	list := ft.Params.List
	unnamed := len(list) > 0 && len(list[0].Names) == 0
	text := name + " context.Context"
	if unnamed && !used {
		text = "context.Context"
	}
	if len(list) > 0 {
		text += ", "
	}
	head := ft.Params.Opening + 1
	var rest []pkgtypes.Change
	if unnamed && used {
		for i, field := range list {
			if i == 0 && field.Type.Pos() == head {
				text += "_ " // one insertion: two at an offset would not keep their order
				continue
			}
			rest = append(rest, ctxInsertion(ws, file, field.Type.Pos(), "_ ", "Name unused parameter"))
		}
	}
	changes := []pkgtypes.Change{ctxInsertion(ws, file, head, text, fmt.Sprintf("Add %s context.Context parameter", name))}
	return append(changes, rest...)
}

func ctxInsertion(ws *pkgtypes.Workspace, file *pkgtypes.File, at token.Pos, text, description string) pkgtypes.Change {
	offset := ws.FileSet.Position(at).Offset
	return pkgtypes.Change{File: file.Path, Start: offset, End: offset, NewText: text, Description: description}
}

// bodyUsesCtx reports whether the rendering refers to f's injected parameter
// name in its body: as an argument at a call site, or in place of a fabricated
// context.
func bodyUsesCtx(plan *threadPlan, f *gotypes.Func, fd *funcDecl) bool {
	if fd.Decl == nil || fd.Decl.Body == nil {
		return false
	}
	if plan.Fake[f] != nil || hasInternalRootContext(fd.Decl.Body) {
		return true
	}
	name := plan.Funcs[f]
	for _, edit := range plan.ArgFor {
		if edit.Site.Fn == fd.Decl && edit.Expr == name {
			return true
		}
	}
	return false
}

// renderCallEdit decides the Change (if any) for one recorded call-site
// decision, and reports the file path that needs a "context" import if the
// emitted text requires one (empty string if not).
//
// The callee is the plan's own object for the called function.
func renderCallEdit(ws *pkgtypes.Workspace, plan *threadPlan, edit *callEdit) (*pkgtypes.Change, string, error) {
	site := edit.Site
	call := site.Call
	callee := edit.Callee

	_, gaining := plan.Funcs[callee]
	if gaining {
		// Case (a): the callee is itself gaining a ctx parameter. Insert the
		// new argument — zero-width, so it can never overlap anything else.
		text := edit.Expr
		if len(call.Args) > 0 {
			text += ", "
		}
		offset := ws.FileSet.Position(call.Lparen).Offset + 1
		change := &pkgtypes.Change{
			File:    site.File.Path,
			Start:   offset,
			End:     offset,
			OldText: "",
			NewText: text,
			// Description must contain a phrase Validator.validateCompilation
			// recognizes ("argument"/"call to") so it skips trying to parse a
			// bare expression like "ctx" as a standalone Go declaration — see
			// that function's isFragment check.
			Description: fmt.Sprintf("Pass %s as argument to call to %s", edit.Expr, funcLabel(callee)),
		}
		return change, importFileFor(site.File.Path, edit.Expr), nil
	}

	_, touched := plan.Decls[callee]
	if touched {
		// Case (b): the callee already had a context.Context parameter (that
		// is why it never joined Funcs), so every call to it already passes
		// something at that position. Bounded tightly on purpose: replace
		// ONLY when that existing argument is a fabricated root
		// (context.Background()/context.TODO()) — isRootContextCall is the
		// same predicate ctx_creation.go uses to find fake roots worth
		// deleting. Such a call takes no arguments, so it can never contain
		// another target call site, which is what keeps this replacement
		// from ever overlapping another emitted change.
		//
		// Anything else already in scope there — a real ctx variable, a
		// function call, a field access — must be left exactly as written:
		// a caller already passing a real context must not be rewritten.
		idx, ok := ctxParamIndex(callee)
		if !ok || idx >= len(call.Args) {
			// Out of range covers a short/variadic argument list at the
			// call site; emit nothing rather than guess or panic.
			return nil, "", nil
		}
		arg := call.Args[idx]
		if !isRootContextCall(arg) {
			return nil, "", nil
		}
		start := ws.FileSet.Position(arg.Pos()).Offset
		end := ws.FileSet.Position(arg.End()).Offset
		text := edit.Expr
		if plan.WithoutCancel && !strings.HasPrefix(text, "context.") {
			text = "context.WithoutCancel(" + text + ")"
		}
		change := &pkgtypes.Change{
			File:        site.File.Path,
			Start:       start,
			End:         end,
			OldText:     string(site.File.OriginalContent[start:end]),
			NewText:     text,
			Description: fmt.Sprintf("Replace fabricated root context with %s at call to %s", edit.Expr, funcLabel(callee)),
		}
		return change, importFileFor(site.File.Path, edit.Expr), nil
	}

	// threadPlan.ArgFor is only ever populated (by resolve, in
	// thread_context.go) for call sites whose callee is a function the
	// traversal actually visited, so callee must be a key of either Funcs or
	// Decls. Getting here means the independent resolution above disagrees
	// with that invariant — report it rather than silently drop the call.
	return nil, "", &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf(
			"internal error: call at %s targets %s, which is outside the threading plan",
			ws.FileSet.Position(call.Pos()), funcLabel(callee)),
		File: site.File.Path,
	}
}

// importFileFor returns filePath when text mentions a context.X expression
// (context.TODO(), context.Background(), or the "context.Context" of a
// signature) and so requires the import, or "" otherwise. A plain identifier
// ("ctx", "c") or a method expression that merely happens to contain
// "Context" as a suffix (t.Context(), r.Context()) needs no import: the
// lowercase "context." check is deliberately exact-cased so it does not
// false-positive on those.
func importFileFor(filePath, text string) string {
	if strings.Contains(text, "context.") {
		return filePath
	}
	return ""
}

// renderFakeDeletion deletes stmt — the top-level `ctx :=
// context.Background()`/`context.TODO()` recorded in threadPlan.Fake for f's
// function — which becomes an illegal redeclaration once ctx is a real
// parameter. This is the one other non-zero-width change this file emits (a
// full statement span), sharing its byte-range/newline-swallowing logic with
// RemoveInternalCtxCreation via ctxAssignDeletionChange in ctx_creation.go.
func renderFakeDeletion(ws *pkgtypes.Workspace, file *pkgtypes.File, stmt *ast.AssignStmt, label string) pkgtypes.Change {
	start := ws.FileSet.Position(stmt.Pos()).Offset
	end := ws.FileSet.Position(stmt.End()).Offset
	return ctxAssignDeletionChange(file.Path, file.OriginalContent, start, end, label)
}

// contextShadowed reports whether `context` at pos names something other than
// the context package, so a context.X expression written there would not compile.
func contextShadowed(pkg *pkgtypes.Package, pos token.Pos) bool {
	_, inner := scopeAt(pkg, pos)
	if inner == nil {
		return false
	}
	_, obj := inner.LookupParent("context", pos)
	if obj == nil {
		return false
	}
	pn, ok := obj.(*gotypes.PkgName)
	return !ok || pn.Imported().Path() != "context"
}

// renderFakeWithoutCancel replaces stmt, the top-level `ctx :=
// context.Background()` of f's function, with a ctx that keeps the parameter's
// values and drops its cancellation, as the fabricated one did not cancel.
func renderFakeWithoutCancel(ws *pkgtypes.Workspace, file *pkgtypes.File, stmt *ast.AssignStmt, param, label string) pkgtypes.Change {
	start := ws.FileSet.Position(stmt.Pos()).Offset
	end := ws.FileSet.Position(stmt.End()).Offset
	text := "ctx := context.WithoutCancel(" + param + ")"
	if param == "ctx" {
		text = "ctx = context.WithoutCancel(ctx)"
	}
	return pkgtypes.Change{
		File:        file.Path,
		Start:       start,
		End:         end,
		OldText:     string(file.OriginalContent[start:end]),
		NewText:     text,
		Description: fmt.Sprintf("Replace fabricated root context with context.WithoutCancel in %s", label),
	}
}

// ctxParamIndex returns the index of f's context.Context parameter. Mirrors
// funcHasContextParam's scan (thread_context.go) but returns the position too,
// since a replacement Change must know exactly which call.Args element to
// replace — the ctx parameter is not necessarily first (e.g. `func
// seed(a int, ctx context.Context)`).
func ctxParamIndex(f *gotypes.Func) (int, bool) {
	sig, ok := f.Type().(*gotypes.Signature)
	if !ok {
		return 0, false
	}
	params := sig.Params()
	for i := 0; i < params.Len(); i++ {
		if analysis.IsContextType(params.At(i).Type()) {
			return i, true
		}
	}
	return 0, false
}
