package refactor

import (
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	gotypes "go/types"
	"slices"
	"sort"
	"strings"

	"github.com/mamaar/gorefactor/pkg/refactor/extractsig"
	"github.com/mamaar/gorefactor/pkg/types"
)

// This file renders an *extractsig.Analysis into the two pieces every
// extract-function/extract-method refactoring is made of — the new
// declaration and the text that replaces the selected block — and turns them
// into a verified plan.
//
// It exists because the previous implementation computed both from regexps
// over the block's raw text. That approach could not distinguish a
// package-level function from a captured variable, had no way to learn a
// variable's real type (so everything became `interface{}`) and never
// noticed that a struct mutated by value has to travel back out, yet it
// reported success regardless. Everything here derives from go/types via
// extractsig, and anything that cannot be derived is refused rather than
// guessed.

// errDeferInSelection is the refusal for a selection containing a defer. The
// analyzer deliberately leaves this to the caller, and it is not something a
// type-checker can catch, so it is checked here explicitly.
var errDeferInSelection = errors.New(
	"the selection contains a `defer`; moving it into an extracted function would change when it runs " +
		"(at the extracted function's return instead of the original function's)")

// extractReceiver describes how a generated method reaches its receiver. A
// nil *extractReceiver means "generate a plain function".
type extractReceiver struct {
	// decl is the text between the parentheses of `func (…)`, e.g.
	// "c *Calculator".
	decl string
	// name is the identifier the call site invokes the method on.
	name string
	// paramIdx is the index into Analysis.Params of the captured variable
	// promoted to the receiver, or -1 when the receiver is synthesized from
	// the enclosing method's own receiver instead of from a parameter. A
	// promoted parameter must not also be passed as an argument.
	paramIdx int
}

// renderExtraction builds the declaration text and the call-site text for one
// extraction. The declaration is run through go/format, which is also what
// re-indents the body: extractsig hands back the block dedented to column
// zero, and hand-rolling the re-indentation is exactly the kind of detail the
// old implementation got wrong.
func renderExtraction(an *extractsig.Analysis, name string, recv *extractReceiver) (decl string, call string, err error) {
	recvIdx, recvName, recvDecl := -1, "", ""
	if recv != nil {
		recvIdx, recvName, recvDecl = recv.paramIdx, recv.name, recv.decl
	}

	sigParams, args := splitParamsAndArgs(an, recvIdx)

	results := an.Results
	if an.TailReturns {
		// Every path through the block returns from the enclosing function,
		// so the extracted function carries that function's result signature
		// and the call site becomes `return name(...)`. Anything the block
		// also produced for the code *after* it is unreachable by
		// construction and cannot be handed back through that single call, so
		// refuse instead of silently dropping the value.
		if len(an.Results) > 0 {
			return "", "", fmt.Errorf("block both returns from the enclosing function on every path and produces %d value(s) the following code still uses; that cannot be expressed as a single call", len(an.Results))
		}
		results = an.EnclosingResults
	}
	if an.ErrGuard {
		results = append(slices.Clone(results), extractsig.Result{Type: "error"})
	}

	var b strings.Builder
	b.WriteString("func ")
	if recvDecl != "" {
		b.WriteString("(" + recvDecl + ") ")
	}
	b.WriteString(name + "(" + strings.Join(sigParams, ", ") + ")")
	writeResultTypes(results, &b)
	b.WriteString(" {\n")
	for _, d := range an.LocalDecls {
		b.WriteString("var " + d.Name + " " + d.Type + "\n")
	}
	b.WriteString(an.BodyText)
	writeTrailingReturn(an, &b)
	b.WriteString("\n}\n")

	decl, err = formatDeclaration(b.String())
	if err != nil {
		return "", "", fmt.Errorf("render extraction: %w", err)
	}

	callExpr := name + "(" + strings.Join(args, ", ") + ")"
	if recvName != "" {
		callExpr = recvName + "." + callExpr
	}

	return decl, hoistedDecls(an) + callSite(an, callExpr), nil
}

func writeResultTypes(results []extractsig.Result, b *strings.Builder) {
	switch len(results) {
	case 0:
	case 1:
		b.WriteString(" " + results[0].Type)
	default:
		typeList := make([]string, 0, len(results))
		for _, r := range results {
			typeList = append(typeList, r.Type)
		}
		b.WriteString(" (" + strings.Join(typeList, ", ") + ")")
	}
}

func writeTrailingReturn(an *extractsig.Analysis, b *strings.Builder) {
	if an.ErrGuard {
		names := make([]string, 0, len(an.Results)+1)
		for _, r := range an.Results {
			names = append(names, r.Name)
		}
		b.WriteString("\nreturn " + strings.Join(append(names, "nil"), ", "))
	} else if !an.TailReturns && len(an.Results) > 0 {
		// TailReturns is false, so extractsig found no return bound to the
		// enclosing function anywhere in the block — the body therefore cannot
		// already end in one, and the results need an explicit return.
		names := make([]string, 0, len(an.Results))
		for _, r := range an.Results {
			names = append(names, r.Name)
		}
		b.WriteString("\nreturn " + strings.Join(names, ", "))
	}
}

// hoistedDecls renders the declarations of the no-copy variables the caller
// now owns.
func hoistedDecls(an *extractsig.Analysis) string {
	var b strings.Builder
	for _, h := range an.Hoisted {
		b.WriteString("var " + h.Name + " " + h.Type + "\n")
	}
	return b.String()
}

// callSite renders the statement(s) that replace the block.
func callSite(an *extractsig.Analysis, callExpr string) string {
	switch {
	case an.TailReturns:
		return "return " + callExpr
	case len(an.Results) == 0 && !an.ErrGuard:
		return callExpr
	}

	if an.ErrInline {
		returned := append(slices.Clone(an.GuardZeros), an.ErrName)
		return "if " + an.ErrName + " := " + callExpr + "; " + an.ErrName + " != nil {\n\treturn " + strings.Join(returned, ", ") + "\n}"
	}

	// Results that were already variables before the block must be assigned
	// with `=`; results the block declared for the first time need `:=`. A
	// mixed set cannot be written as one assignment statement, so the new
	// names get an explicit `var` on preceding lines and the assignment stays
	// a plain `=`.
	existing := make(map[string]bool, len(an.Params)+len(an.LocalDecls))
	for _, p := range an.Params {
		existing[p.Name] = true
	}
	for _, d := range an.LocalDecls {
		existing[d.Name] = true
	}
	for _, d := range an.DroppedDecls {
		delete(existing, d.Name)
	}

	outs := slices.Clone(an.Results)
	if an.ErrGuard {
		outs = append(outs, extractsig.Result{Name: an.ErrName, Type: "error"})
		existing[an.ErrName] = an.ErrExists
	}
	lhs := make([]string, 0, len(outs))
	var fresh []extractsig.Result
	for _, r := range outs {
		lhs = append(lhs, r.Name)
		if !existing[r.Name] {
			fresh = append(fresh, r)
		}
	}
	assign := strings.Join(lhs, ", ") + " := " + callExpr
	if len(fresh) != len(outs) {
		lines := make([]string, 0, len(fresh)+1)
		for _, r := range fresh {
			lines = append(lines, "var "+r.Name+" "+r.Type)
		}
		lines = append(lines, strings.Join(lhs, ", ")+" = "+callExpr)
		assign = strings.Join(lines, "\n")
	}
	if !an.ErrGuard {
		return assign
	}
	returned := append(slices.Clone(an.GuardZeros), an.ErrName)
	return assign + "\nif " + an.ErrName + " != nil {\n\treturn " + strings.Join(returned, ", ") + "\n}"
}

func splitParamsAndArgs(an *extractsig.Analysis, recvIdx int) ([]string, []string) {
	var sigParams, args []string
	for i, p := range an.Params {
		if i == recvIdx {
			continue
		}
		sigParams = append(sigParams, p.Name+" "+p.Type)
		if p.ByPointer {
			args = append(args, "&"+p.Name)
			continue
		}
		args = append(args, p.Name)
	}
	return sigParams, args
}

// formatDeclaration gofmts a single top-level declaration by wrapping it in a
// throwaway package clause and stripping that clause off again.
func formatDeclaration(declSrc string) (string, error) {
	const hdr = "package p\n\n"
	formatted, err := format.Source([]byte(hdr + declSrc))
	if err != nil {
		return "", fmt.Errorf("the generated declaration is not valid Go: %w", err)
	}
	out := strings.TrimPrefix(string(formatted), hdr)
	if strings.HasPrefix(out, "package ") {
		// gofmt reshaped the header (it should not, but a stale prefix here
		// would end up written into the user's file).
		return "", fmt.Errorf("could not strip the synthetic package clause from the generated declaration")
	}
	return strings.TrimRight(out, "\n"), nil
}

// selectionHasDefer reports whether stmts contain a defer bound to the
// function the selection lives in.
//
// Moving such a defer into an extracted function changes when it runs: it
// would fire when the extracted function returns instead of when the original
// function does. That is a silent behaviour change no type-checker can catch,
// so it is refused. A defer inside a nested function literal is fine — the
// literal travels with the defer and its timing is unchanged.
func selectionHasDefer(stmts []ast.Stmt) bool {
	found := false
	visit := func(n ast.Node) bool {
		if found {
			return false
		}
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.DeferStmt:
			found = true
			return false
		}
		return true
	}
	for _, s := range stmts {
		ast.Inspect(s, visit)
	}
	return found
}

// lineByteSpan returns the [start, end) byte offsets covering whole lines
// startLine..endLine of content, with end placed just past the last character
// of endLine (its newline is left in place).
func lineByteSpan(content string, startLine, endLine int) (int, int, bool) {
	lines := strings.Split(content, "\n")
	if startLine < 1 || endLine > len(lines) || startLine > endLine {
		return 0, 0, false
	}
	start := 0
	for i := range startLine - 1 {
		start += len(lines[i]) + 1
	}
	end := start
	for i := startLine - 1; i < endLine; i++ {
		end += len(lines[i])
		if i < endLine-1 {
			end++ // the newline joining this line to the next
		}
	}
	return start, end, true
}

// wholeLineIfAlone widens [start,end) to the full line, newline included,
// when only whitespace shares the line with it.
func wholeLineIfAlone(content string, start, end int) (int, int) {
	lineStart := strings.LastIndex(content[:start], "\n") + 1
	lineEnd := len(content)
	i := strings.Index(content[end:], "\n")
	if i >= 0 {
		lineEnd = end + i + 1
	}
	if strings.TrimSpace(content[lineStart:start]+content[end:lineEnd]) != "" {
		return start, end
	}
	return lineStart, lineEnd
}

// errNoImportChange reports that extractImportsChange has no imports to add.
var errNoImportChange = errors.New("no imports to add")

// extractImportsChange returns the change that adds the imports the rendered
// signature needs but the source file does not have yet, or errNoImportChange
// when there are none, or the file already has them.
//
// The Description text is load-bearing: the plan validator skips its
// standalone-syntax check for descriptions containing "import" (see
// (*Validator).validateCompilation), and generateNeededImportsChange already
// produces the exact wording other operations rely on.
func extractImportsChange(ws *types.Workspace, sourceFile *types.File, imports map[string]string) (*types.Change, error) {
	if len(imports) == 0 {
		return nil, errNoImportChange
	}

	paths := make([]string, 0, len(imports))
	for path := range imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	needed := make([]neededImport, 0, len(paths))
	for _, path := range paths {
		pkgName := imports[path]
		if pkgName == "" {
			return nil, fmt.Errorf("the extracted signature references package %q under an unknown name", path)
		}
		// generateNeededImportsChange writes an unaliased import as just its
		// path, which makes the local name the path's last segment. When the
		// package's real name differs from that (gopkg.in/yaml.v2 -> yaml, a
		// vN suffix, ...) the qualifier extractsig rendered would not resolve,
		// so pin it with an explicit alias.
		alias := ""
		if pkgName != lastPathSegment(path) {
			alias = pkgName
		}
		needed = append(needed, neededImport{alias: alias, path: path})
	}

	change := generateNeededImportsChange(ws, sourceFile, needed)
	if change == nil {
		return nil, errNoImportChange
	}
	return change, nil
}

func lastPathSegment(path string) string {
	_, after, ok := strings.CutLast(path, "/")
	if ok {
		return after
	}
	return path
}

// buildVerifiedExtractPlan assembles the plan for one extraction and gates it
// on VerifyPlanTypesWithImporter.
//
// The gate is the whole point of the rewrite: these operations used to report
// success while emitting code that could not compile. A plan that introduces
// a type error is rejected, and so is one whose verification was skipped —
// both operations always work on a file already present in the workspace, so
// "could not verify" means something is genuinely wrong, and reporting
// unverified output as success is precisely the bug being fixed.
//
// importer should be the importer that type-checked ws (typically
// op.Parser.Importer() — see ExtractMethodOperation/ExtractFunctionOperation,
// which call EnsureTypeChecked with that same parser just before reaching
// here). It is threaded into VerifyPlanTypesWithImporter so that verification
// resolves imports through the identical *go/types.Package instances the
// workspace was already checked with, instead of a second importer producing
// distinct-but-identical-looking packages for the same import path — see
// VerifyPlanTypesWithImporter's doc comment for why that produces spurious
// type errors. importer may be nil (falls back to VerifyPlanTypesWithImporter's own
// fresh-parser construction), which callers without a pre-checked workspace
// parser should pass.
func buildVerifiedExtractPlan(
	ws *types.Workspace,
	op types.Operation,
	sourceFile *types.File,
	sourcePackage *types.Package,
	an *extractsig.Analysis,
	startLine, endLine int,
	kind, name, decl, call string,
	importer gotypes.Importer,
) (*types.RefactoringPlan, error) {
	content := string(sourceFile.OriginalContent)
	blockStart, blockEnd, ok := lineByteSpan(content, startLine, endLine)
	if !ok {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("invalid line range: %d-%d in %s", startLine, endLine, sourceFile.Path),
		}
	}

	// Placing the declaration immediately after the enclosing function keeps
	// it next to the code it came from and — because the block is by
	// definition inside that function — guarantees the two changes cannot
	// overlap, which the plan validator rejects outright.
	insertAt := ws.FileSet.Position(an.EnclosingFunc.End()).Offset
	if insertAt <= blockEnd || insertAt > len(content) {
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("cannot place the extracted %s: the selection is not strictly inside the body of %s", kind, an.EnclosingFunc.Name.Name),
		}
	}

	changes := []types.Change{
		{
			File:        sourceFile.Path,
			Start:       blockStart,
			End:         blockEnd,
			OldText:     content[blockStart:blockEnd],
			NewText:     call,
			Description: fmt.Sprintf("Replace extracted code with call to %s", name),
		},
		{
			File:        sourceFile.Path,
			Start:       insertAt,
			End:         insertAt,
			OldText:     "",
			NewText:     "\n\n" + decl + "\n",
			Description: fmt.Sprintf("Add extracted %s %s", kind, name),
		},
	}

	for _, d := range an.DroppedDecls {
		start, end := wholeLineIfAlone(content, d.Start, d.End)
		changes = append(changes, types.Change{
			File:        sourceFile.Path,
			Start:       start,
			End:         end,
			OldText:     content[start:end],
			NewText:     "",
			Description: fmt.Sprintf("Remove declaration of %s, now declared by the call to %s", d.Name, name),
		})
	}

	importChange, err := extractImportsChange(ws, sourceFile, an.Imports)
	switch {
	case errors.Is(err, errNoImportChange):
	case err != nil:
		return nil, &types.RefactorError{
			Type:    types.InvalidOperation,
			Message: fmt.Sprintf("cannot extract %s: %v", name, err),
		}
	default:
		changes = append(changes, *importChange)
	}

	plan := &types.RefactoringPlan{
		Operations:    []types.Operation{op},
		Changes:       changes,
		AffectedFiles: []string{sourceFile.Path},
		Impact: &types.ImpactAnalysis{
			AffectedFiles:    []string{sourceFile.Path},
			AffectedPackages: []string{sourcePackage.Path},
		},
		Reversible: true,
	}

	verification := VerifyPlanTypesWithImporter(ws, plan, importer)
	if !verification.OK() {
		return nil, &types.RefactorError{
			Type: types.CompilationError,
			Message: fmt.Sprintf("extracting %s %s would not compile:\n  %s",
				kind, name, strings.Join(verification.NewErrors, "\n  ")),
		}
	}
	if verification.Skipped != "" {
		return nil, &types.RefactorError{
			Type: types.CompilationError,
			Message: fmt.Sprintf("extracting %s %s could not be verified, so it is not applied: %s",
				kind, name, verification.Skipped),
		}
	}

	return plan, nil
}

// refuseExtraction converts an extractsig failure into the error the MCP
// layer surfaces. A *extractsig.Refusal is a correct outcome — the reason is
// passed through verbatim so the caller learns what to change.
func refuseExtraction(kind, name string, err error) error {
	return &types.RefactorError{
		Type:    types.InvalidOperation,
		Message: fmt.Sprintf("cannot extract %s %s: %v", kind, name, err),
	}
}
