package analysis

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// ErrorWrappingFixResult holds information about what was fixed.
type ErrorWrappingFixResult struct {
	ErrorsWrapped    int
	FormatVerbsFixed int
	ContextsAdded    int
}

// ErrorWrappingFixer fixes improper error wrapping violations.
type ErrorWrappingFixer struct {
	workspace *types.Workspace
	fileSet   *token.FileSet
	severity  ErrorWrappingSeverity
}

// NewErrorWrappingFixer creates a new error wrapping fixer.
func NewErrorWrappingFixer(ws *types.Workspace, severity ErrorWrappingSeverity) *ErrorWrappingFixer {
	if severity == "" {
		severity = SeverityCritical
	}
	return &ErrorWrappingFixer{
		workspace: ws,
		fileSet:   ws.FileSet,
		severity:  severity,
	}
}

// Fix analyzes the workspace and returns a plan to fix all violations.
func (f *ErrorWrappingFixer) Fix(pkg string) (*types.RefactoringPlan, *ErrorWrappingFixResult, error) {
	analyzer := NewErrorWrappingAnalyzer(f.workspace, f.severity)

	var violations []*ErrorWrappingViolation
	if pkg != "" {
		resolved := types.ResolvePackagePath(f.workspace, pkg)
		p, ok := f.workspace.Packages[resolved]
		if !ok {
			return &types.RefactoringPlan{}, &ErrorWrappingFixResult{}, nil
		}
		violations = analyzer.AnalyzePackage(p)
	} else {
		violations = analyzer.AnalyzeWorkspace()
	}

	if len(violations) == 0 {
		return &types.RefactoringPlan{}, &ErrorWrappingFixResult{}, nil
	}

	var changes []types.Change
	result := &ErrorWrappingFixResult{}
	affectedSet := make(map[string]bool)

	for _, v := range violations {
		file := f.findFile(v.File)
		if file == nil {
			continue
		}

		change, ok := f.buildChange(file, v)
		if !ok {
			continue
		}

		changes = append(changes, change)
		affectedSet[v.File] = true

		switch v.ViolationType {
		case BareReturn:
			result.ErrorsWrapped++
		case FormatVerbV:
			result.FormatVerbsFixed++
		case NoContext:
			result.ContextsAdded++
		}
	}

	var affected []string
	for path := range affectedSet {
		affected = append(affected, path)
	}

	return &types.RefactoringPlan{
		Changes:       changes,
		AffectedFiles: affected,
	}, result, nil
}

// buildChange creates a Change for a single violation.
func (f *ErrorWrappingFixer) buildChange(file *types.File, v *ErrorWrappingViolation) (types.Change, bool) {
	switch v.ViolationType {
	case BareReturn:
		return f.fixBareReturn(file, v)
	case FormatVerbV:
		return f.fixFormatVerbV(file, v)
	case NoContext:
		return f.fixNoContext(file, v)
	}
	return types.Change{}, false
}

// fixBareReturn replaces a bare error identifier with fmt.Errorf wrapping.
// e.g., `return err` → `return fmt.Errorf("context: %w", err)`
// e.g., `return nil, err` → `return nil, fmt.Errorf("context: %w", err)`
func (f *ErrorWrappingFixer) fixBareReturn(file *types.File, v *ErrorWrappingViolation) (types.Change, bool) {
	content := file.OriginalContent

	// Find the return statement at this location
	retStmt := f.findReturnStmtAt(file, v.Line)
	if retStmt == nil {
		return types.Change{}, false
	}

	// Find the error identifier in the return results
	for _, expr := range retStmt.Results {
		ident, ok := expr.(*ast.Ident)
		if !ok || !isErrorVarName(ident.Name) {
			continue
		}

		start := f.fileSet.Position(ident.Pos()).Offset
		end := f.fileSet.Position(ident.End()).Offset

		if start < 0 || end > len(content) || start >= end {
			continue
		}

		ctx := v.ContextSuggestion
		if ctx == "" {
			ctx = "operation failed"
		}

		oldText := string(content[start:end])
		newText := `fmt.Errorf("` + ctx + `: %w", ` + ident.Name + `)`

		return types.Change{
			File:        file.Path,
			Start:       start,
			End:         end,
			OldText:     oldText,
			NewText:     newText,
			Description: "Wrap bare error return with context",
		}, true
	}

	return types.Change{}, false
}

// fixFormatVerbV replaces %v with %w in the format string.
func (f *ErrorWrappingFixer) fixFormatVerbV(file *types.File, v *ErrorWrappingViolation) (types.Change, bool) {
	content := file.OriginalContent

	retStmt := f.findReturnStmtAt(file, v.Line)
	if retStmt == nil {
		return types.Change{}, false
	}

	// Find the fmt.Errorf call and its format string
	for _, expr := range retStmt.Results {
		call, ok := expr.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			continue
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "fmt" || sel.Sel.Name != "Errorf" {
			continue
		}

		formatLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || formatLit.Kind != token.STRING {
			continue
		}

		if !strings.Contains(formatLit.Value, "%v") {
			continue
		}

		start := f.fileSet.Position(formatLit.Pos()).Offset
		end := f.fileSet.Position(formatLit.End()).Offset

		if start < 0 || end > len(content) || start >= end {
			continue
		}

		oldText := string(content[start:end])
		newText := strings.Replace(oldText, "%v", "%w", 1)

		return types.Change{
			File:        file.Path,
			Start:       start,
			End:         end,
			OldText:     oldText,
			NewText:     newText,
			Description: "Replace %v with %w in error format string",
		}, true
	}

	return types.Change{}, false
}

// fixNoContext replaces a generic error message with a descriptive one.
func (f *ErrorWrappingFixer) fixNoContext(file *types.File, v *ErrorWrappingViolation) (types.Change, bool) {
	content := file.OriginalContent

	retStmt := f.findReturnStmtAt(file, v.Line)
	if retStmt == nil {
		return types.Change{}, false
	}

	for _, expr := range retStmt.Results {
		call, ok := expr.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			continue
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "fmt" || sel.Sel.Name != "Errorf" {
			continue
		}

		formatLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || formatLit.Kind != token.STRING {
			continue
		}

		if !strings.Contains(formatLit.Value, "%w") {
			continue
		}

		start := f.fileSet.Position(formatLit.Pos()).Offset
		end := f.fileSet.Position(formatLit.End()).Offset

		if start < 0 || end > len(content) || start >= end {
			continue
		}

		ctx := v.ContextSuggestion
		if ctx == "" {
			ctx = "operation failed"
		}

		oldText := string(content[start:end])
		newText := `"` + ctx + `: %w"`

		return types.Change{
			File:        file.Path,
			Start:       start,
			End:         end,
			OldText:     oldText,
			NewText:     newText,
			Description: "Replace generic error message with descriptive context",
		}, true
	}

	return types.Change{}, false
}

// findReturnStmtAt locates a return statement at the given line.
func (f *ErrorWrappingFixer) findReturnStmtAt(file *types.File, line int) *ast.ReturnStmt {
	var result *ast.ReturnStmt
	ast.Inspect(file.AST, func(n ast.Node) bool {
		if result != nil {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		pos := f.fileSet.Position(ret.Pos())
		if pos.Line == line {
			result = ret
			return false
		}
		return true
	})
	return result
}

// findFile locates a file by path across all packages.
func (f *ErrorWrappingFixer) findFile(path string) *types.File {
	for _, pkg := range f.workspace.Packages {
		if file, ok := pkg.Files[path]; ok {
			return file
		}
	}
	return nil
}
