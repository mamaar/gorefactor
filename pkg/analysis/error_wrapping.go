package analysis

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"github.com/mamaar/gorefactor/pkg/types"
)

// ErrorWrappingViolationType categorizes the kind of error wrapping violation.
type ErrorWrappingViolationType string

const (
	BareReturn  ErrorWrappingViolationType = "bare_return"
	FormatVerbV ErrorWrappingViolationType = "format_verb_v_instead_of_w"
	NoContext   ErrorWrappingViolationType = "no_context"
)

// ErrorWrappingSeverity indicates severity level.
type ErrorWrappingSeverity string

const (
	SeverityCritical ErrorWrappingSeverity = "critical"
	SeverityWarning  ErrorWrappingSeverity = "warning"
	SeverityInfo     ErrorWrappingSeverity = "info"
)

// ErrorWrappingViolation represents a detected improper error wrapping.
type ErrorWrappingViolation struct {
	File              string
	Line              int
	Column            int
	Function          string
	ViolationType     ErrorWrappingViolationType
	CurrentCode       string
	ContextSuggestion string
	Severity          ErrorWrappingSeverity
}

// ErrorWrappingAnalyzer detects improper error wrapping patterns.
type ErrorWrappingAnalyzer struct {
	workspace *types.Workspace
	fileSet   *token.FileSet
	severity  ErrorWrappingSeverity // filter: only report this severity or higher
}

// NewErrorWrappingAnalyzer creates a new error wrapping analyzer.
// severity filters results: "critical" only shows bare returns and %v,
// "warning" adds no-context, "info" shows all.
func NewErrorWrappingAnalyzer(ws *types.Workspace, severity ErrorWrappingSeverity) *ErrorWrappingAnalyzer {
	if severity == "" {
		severity = SeverityCritical
	}
	return &ErrorWrappingAnalyzer{
		workspace: ws,
		fileSet:   ws.FileSet,
		severity:  severity,
	}
}

// AnalyzeWorkspace analyzes all packages in the workspace.
func (a *ErrorWrappingAnalyzer) AnalyzeWorkspace() []*ErrorWrappingViolation {
	var results []*ErrorWrappingViolation
	for _, pkg := range a.workspace.Packages {
		results = append(results, a.AnalyzePackage(pkg)...)
	}
	return results
}

// AnalyzePackage analyzes all files in a package.
func (a *ErrorWrappingAnalyzer) AnalyzePackage(pkg *types.Package) []*ErrorWrappingViolation {
	var results []*ErrorWrappingViolation
	for _, file := range pkg.Files {
		results = append(results, a.AnalyzeFile(file)...)
	}
	return results
}

// AnalyzeFile analyzes a single file for error wrapping violations.
func (a *ErrorWrappingAnalyzer) AnalyzeFile(file *types.File) []*ErrorWrappingViolation {
	var results []*ErrorWrappingViolation
	var currentFunc string

	ast.Inspect(file.AST, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		switch node := n.(type) {
		case *ast.FuncDecl:
			currentFunc = node.Name.Name
			// Only analyze functions that return error
			if !a.returnsError(node) {
				return false
			}
		case *ast.ReturnStmt:
			violations := a.checkReturnStmt(file, node, currentFunc)
			results = append(results, violations...)
		}
		return true
	})

	return results
}

// returnsError checks if a function has error in its return types.
func (a *ErrorWrappingAnalyzer) returnsError(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if ident, ok := field.Type.(*ast.Ident); ok {
			if ident.Name == "error" {
				return true
			}
		}
	}
	return false
}

// checkReturnStmt analyzes a return statement for error wrapping violations.
func (a *ErrorWrappingAnalyzer) checkReturnStmt(file *types.File, ret *ast.ReturnStmt, funcName string) []*ErrorWrappingViolation {
	var results []*ErrorWrappingViolation

	for _, expr := range ret.Results {
		// Check for bare error return: `return err`
		if v := a.checkBareReturn(file, ret, expr, funcName); v != nil {
			if a.matchesSeverity(v.Severity) {
				results = append(results, v)
			}
		}

		// Check for fmt.Errorf with %v: `return fmt.Errorf("...: %v", err)`
		if v := a.checkFmtErrorf(file, ret, expr, funcName); v != nil {
			if a.matchesSeverity(v.Severity) {
				results = append(results, v)
			}
		}
	}

	return results
}

// checkBareReturn detects `return err` or `return someErr` without wrapping.
func (a *ErrorWrappingAnalyzer) checkBareReturn(file *types.File, ret *ast.ReturnStmt, expr ast.Expr, funcName string) *ErrorWrappingViolation {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return nil
	}

	// Only flag identifiers that look like error variables
	if !isErrorVarName(ident.Name) {
		return nil
	}

	pos := a.fileSet.Position(ret.Pos())
	code := a.sourceText(file, ret.Pos(), ret.End())

	return &ErrorWrappingViolation{
		File:              file.Path,
		Line:              pos.Line,
		Column:            pos.Column,
		Function:          funcName,
		ViolationType:     BareReturn,
		CurrentCode:       strings.TrimSpace(code),
		ContextSuggestion: suggestContext(funcName),
		Severity:          SeverityCritical,
	}
}

// checkFmtErrorf detects fmt.Errorf with %v instead of %w, or with no descriptive context.
func (a *ErrorWrappingAnalyzer) checkFmtErrorf(file *types.File, ret *ast.ReturnStmt, expr ast.Expr, funcName string) *ErrorWrappingViolation {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}

	// Check if it's fmt.Errorf(...)
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok || pkgIdent.Name != "fmt" || sel.Sel.Name != "Errorf" {
		return nil
	}

	if len(call.Args) < 2 {
		return nil
	}

	// Get the format string
	formatLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || formatLit.Kind != token.STRING {
		return nil
	}
	formatStr := formatLit.Value

	pos := a.fileSet.Position(ret.Pos())
	code := a.sourceText(file, ret.Pos(), ret.End())

	// Check for %v instead of %w
	if strings.Contains(formatStr, "%v") && !strings.Contains(formatStr, "%w") {
		// Check if last arg looks like an error
		lastArg := call.Args[len(call.Args)-1]
		if ident, ok := lastArg.(*ast.Ident); ok && isErrorVarName(ident.Name) {
			return &ErrorWrappingViolation{
				File:              file.Path,
				Line:              pos.Line,
				Column:            pos.Column,
				Function:          funcName,
				ViolationType:     FormatVerbV,
				CurrentCode:       strings.TrimSpace(code),
				ContextSuggestion: suggestContext(funcName),
				Severity:          SeverityCritical,
			}
		}
	}

	// Check for no descriptive context (e.g., `fmt.Errorf("error: %w", err)`)
	if strings.Contains(formatStr, "%w") {
		// Strip the format string of its quotes and the %w verb
		inner := strings.Trim(formatStr, `"`)
		inner = strings.Replace(inner, "%w", "", 1)
		inner = strings.TrimSpace(inner)
		inner = strings.TrimRight(inner, ": ")

		if isGenericMessage(inner) {
			return &ErrorWrappingViolation{
				File:              file.Path,
				Line:              pos.Line,
				Column:            pos.Column,
				Function:          funcName,
				ViolationType:     NoContext,
				CurrentCode:       strings.TrimSpace(code),
				ContextSuggestion: suggestContext(funcName),
				Severity:          SeverityWarning,
			}
		}
	}

	return nil
}

// matchesSeverity checks if a violation severity passes the filter.
func (a *ErrorWrappingAnalyzer) matchesSeverity(s ErrorWrappingSeverity) bool {
	switch a.severity {
	case SeverityCritical:
		return s == SeverityCritical
	case SeverityWarning:
		return s == SeverityCritical || s == SeverityWarning
	case SeverityInfo:
		return true
	}
	return true
}

// sourceText extracts source text between two positions.
func (a *ErrorWrappingAnalyzer) sourceText(file *types.File, from, to token.Pos) string {
	content := file.OriginalContent
	if len(content) == 0 {
		return ""
	}
	start := a.fileSet.Position(from).Offset
	end := a.fileSet.Position(to).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

// isErrorVarName returns true if the identifier name looks like an error variable.
func isErrorVarName(name string) bool {
	if name == "err" {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "err") ||
		strings.HasSuffix(lower, "error") ||
		strings.HasPrefix(lower, "err")
}

// isGenericMessage returns true if the error message lacks descriptive context.
func isGenericMessage(msg string) bool {
	lower := strings.ToLower(msg)
	generic := []string{
		"", "error", "err", "failed", "failure", "fail",
		"something went wrong", "unexpected error",
	}
	return slices.Contains(generic, lower)
}

// suggestContext generates a context suggestion from a function name.
// e.g., "CreateOrder" → "create order", "GetUser" → "get user"
func suggestContext(funcName string) string {
	if funcName == "" {
		return ""
	}

	// Split camelCase/PascalCase into words
	var words []string
	current := strings.Builder{}
	for i, r := range funcName {
		if i > 0 && r >= 'A' && r <= 'Z' {
			if current.Len() > 0 {
				words = append(words, strings.ToLower(current.String()))
				current.Reset()
			}
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		words = append(words, strings.ToLower(current.String()))
	}

	return strings.Join(words, " ")
}
