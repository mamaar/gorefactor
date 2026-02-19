// Package errorwrap provides a go/analysis analyzer that detects improper error
// wrapping patterns: bare error returns, fmt.Errorf with %v instead of %w, and
// fmt.Errorf calls with no descriptive context message.
package errorwrap

import (
	"go/ast"
	"go/token"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
)

// Severity controls which violations are reported.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Violation type constants.
const (
	BareReturn  = "bare_return"
	FormatVerbV = "format_verb_v_instead_of_w"
	NoContext   = "no_context"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File              string `json:"file"`
	Line              int    `json:"line"`
	Column            int    `json:"column"`
	Function          string `json:"function_name"`
	ViolationType     string `json:"violation_type"`
	CurrentCode       string `json:"current_code"`
	ContextSuggestion string `json:"context_suggestion"`
	Severity          string `json:"severity"`
}

// Option configures the analyzer.
type Option func(*config)

type config struct {
	severity Severity
}

// WithSeverity returns an Option that sets the minimum severity level to report.
func WithSeverity(s Severity) Option {
	return func(c *config) {
		c.severity = s
	}
}

// Analyzer is the default error wrapping analyzer using SeverityCritical.
var Analyzer = NewAnalyzer()

// NewAnalyzer creates a configured *analysis.Analyzer. Without options the
// severity defaults to SeverityCritical.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := &config{severity: SeverityCritical}
	for _, o := range opts {
		o(cfg)
	}

	return &analysis.Analyzer{
		Name:       "errorwrap",
		Doc:        "detects improper error wrapping: bare returns, %v instead of %w, and no descriptive context",
		Run:        makeRun(cfg),
		Requires:   []*analysis.Analyzer{filedata.Analyzer},
		ResultType: reflect.TypeOf(([]*Result)(nil)),
	}
}

func makeRun(cfg *config) func(*analysis.Pass) (any, error) {
	return func(pass *analysis.Pass) (any, error) {
		fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
		var results []*Result

		for _, file := range pass.Files {
			filename := pass.Fset.Position(file.Pos()).Filename
			content := fd.Content[filename]
			results = append(results, analyzeFile(pass, cfg, file, content)...)
		}

		return results, nil
	}
}

func analyzeFile(pass *analysis.Pass, cfg *config, file *ast.File, content []byte) []*Result {
	var results []*Result
	var currentFunc string
	funcReturnsError := false

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		switch node := n.(type) {
		case *ast.FuncDecl:
			currentFunc = node.Name.Name
			funcReturnsError = returnsError(node)
			if !funcReturnsError {
				return false
			}
		case *ast.ReturnStmt:
			if !funcReturnsError {
				return true
			}
			violations := checkReturnStmt(pass, cfg, file, content, node, currentFunc)
			results = append(results, violations...)
		}
		return true
	})

	return results
}

func returnsError(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "error" {
			return true
		}
	}
	return false
}

func checkReturnStmt(pass *analysis.Pass, cfg *config, file *ast.File, content []byte, ret *ast.ReturnStmt, funcName string) []*Result {
	var results []*Result

	for _, expr := range ret.Results {
		if r := checkBareReturn(pass, cfg, file, content, ret, expr, funcName); r != nil {
			results = append(results, r)
		}
		if r := checkFmtErrorf(pass, cfg, file, content, ret, expr, funcName); r != nil {
			results = append(results, r)
		}
	}

	return results
}

func checkBareReturn(pass *analysis.Pass, cfg *config, file *ast.File, content []byte, ret *ast.ReturnStmt, expr ast.Expr, funcName string) *Result {
	ident, ok := expr.(*ast.Ident)
	if !ok || !isErrorVarName(ident.Name) {
		return nil
	}

	if !matchesSeverity(cfg.severity, SeverityCritical) {
		return nil
	}

	pos := pass.Fset.Position(ret.Pos())
	code := sourceText(pass.Fset, content, ret.Pos(), ret.End())
	ctx := suggestContext(funcName)

	// Build the suggested fix: replace the bare ident with fmt.Errorf(...)
	identStart := pass.Fset.Position(ident.Pos()).Offset
	identEnd := pass.Fset.Position(ident.End()).Offset

	errCtx := ctx
	if errCtx == "" {
		errCtx = "operation failed"
	}

	var fixes []analysis.SuggestedFix
	if identStart >= 0 && identEnd > identStart && identEnd <= len(content) {
		newText := `fmt.Errorf("` + errCtx + `: %w", ` + ident.Name + `)`
		fixes = append(fixes, analysis.SuggestedFix{
			Message: "Wrap bare error return with context",
			TextEdits: []analysis.TextEdit{
				{
					Pos:     ident.Pos(),
					End:     ident.End(),
					NewText: []byte(newText),
				},
			},
		})
	}

	pass.Report(analysis.Diagnostic{
		Pos:            ret.Pos(),
		End:            ret.End(),
		Message:        "bare error return without wrapping: use fmt.Errorf(\"" + errCtx + ": %w\", " + ident.Name + ")",
		SuggestedFixes: fixes,
	})

	return &Result{
		File:              pos.Filename,
		Line:              pos.Line,
		Column:            pos.Column,
		Function:          funcName,
		ViolationType:     BareReturn,
		CurrentCode:       strings.TrimSpace(code),
		ContextSuggestion: ctx,
		Severity:          string(SeverityCritical),
	}
}

func checkFmtErrorf(pass *analysis.Pass, cfg *config, file *ast.File, content []byte, ret *ast.ReturnStmt, expr ast.Expr, funcName string) *Result {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}

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

	formatLit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || formatLit.Kind != token.STRING {
		return nil
	}
	formatStr := formatLit.Value

	pos := pass.Fset.Position(ret.Pos())
	code := sourceText(pass.Fset, content, ret.Pos(), ret.End())
	ctx := suggestContext(funcName)

	// Check for %v instead of %w
	if strings.Contains(formatStr, "%v") && !strings.Contains(formatStr, "%w") {
		lastArg := call.Args[len(call.Args)-1]
		ident, ok := lastArg.(*ast.Ident)
		if ok && isErrorVarName(ident.Name) && matchesSeverity(cfg.severity, SeverityCritical) {
			litStart := pass.Fset.Position(formatLit.Pos()).Offset
			litEnd := pass.Fset.Position(formatLit.End()).Offset

			var fixes []analysis.SuggestedFix
			if litStart >= 0 && litEnd > litStart && litEnd <= len(content) {
				oldLit := string(content[litStart:litEnd])
				newLit := strings.Replace(oldLit, "%v", "%w", 1)
				fixes = append(fixes, analysis.SuggestedFix{
					Message: "Replace %v with %w in error format string",
					TextEdits: []analysis.TextEdit{
						{
							Pos:     formatLit.Pos(),
							End:     formatLit.End(),
							NewText: []byte(newLit),
						},
					},
				})
			}

			pass.Report(analysis.Diagnostic{
				Pos:            ret.Pos(),
				End:            ret.End(),
				Message:        "use %w instead of %v to allow error unwrapping",
				SuggestedFixes: fixes,
			})

			return &Result{
				File:              pos.Filename,
				Line:              pos.Line,
				Column:            pos.Column,
				Function:          funcName,
				ViolationType:     FormatVerbV,
				CurrentCode:       strings.TrimSpace(code),
				ContextSuggestion: ctx,
				Severity:          string(SeverityCritical),
			}
		}
	}

	// Check for %w with no descriptive context
	if strings.Contains(formatStr, "%w") {
		inner := strings.Trim(formatStr, `"`)
		inner = strings.Replace(inner, "%w", "", 1)
		inner = strings.TrimSpace(inner)
		inner = strings.TrimRight(inner, ": ")

		if isGenericMessage(inner) && matchesSeverity(cfg.severity, SeverityWarning) {
			litStart := pass.Fset.Position(formatLit.Pos()).Offset
			litEnd := pass.Fset.Position(formatLit.End()).Offset

			errCtx := ctx
			if errCtx == "" {
				errCtx = "operation failed"
			}

			var fixes []analysis.SuggestedFix
			if litStart >= 0 && litEnd > litStart && litEnd <= len(content) {
				newLit := `"` + errCtx + `: %w"`
				fixes = append(fixes, analysis.SuggestedFix{
					Message: "Replace generic error message with descriptive context",
					TextEdits: []analysis.TextEdit{
						{
							Pos:     formatLit.Pos(),
							End:     formatLit.End(),
							NewText: []byte(newLit),
						},
					},
				})
			}

			pass.Report(analysis.Diagnostic{
				Pos:            ret.Pos(),
				End:            ret.End(),
				Message:        "error message lacks descriptive context: use \"" + errCtx + ": %w\"",
				SuggestedFixes: fixes,
			})

			return &Result{
				File:              pos.Filename,
				Line:              pos.Line,
				Column:            pos.Column,
				Function:          funcName,
				ViolationType:     NoContext,
				CurrentCode:       strings.TrimSpace(code),
				ContextSuggestion: ctx,
				Severity:          string(SeverityWarning),
			}
		}
	}

	return nil
}

// matchesSeverity returns true if the violation severity meets or exceeds the
// configured threshold.
//
//	threshold: critical → only critical passes
//	threshold: warning  → critical and warning pass
//	threshold: info     → all pass
func matchesSeverity(threshold, violation Severity) bool {
	switch threshold {
	case SeverityCritical:
		return violation == SeverityCritical
	case SeverityWarning:
		return violation == SeverityCritical || violation == SeverityWarning
	case SeverityInfo:
		return true
	}
	return true
}

func sourceText(fset *token.FileSet, content []byte, from, to token.Pos) string {
	if len(content) == 0 {
		return ""
	}
	start := fset.Position(from).Offset
	end := fset.Position(to).Offset
	if start < 0 || end < 0 || start >= len(content) || end > len(content) || start >= end {
		return ""
	}
	return string(content[start:end])
}

func isErrorVarName(name string) bool {
	if name == "err" {
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "err") ||
		strings.HasSuffix(lower, "error") ||
		strings.HasPrefix(lower, "err")
}

func isGenericMessage(msg string) bool {
	lower := strings.ToLower(msg)
	generic := []string{
		"", "error", "err", "failed", "failure", "fail",
		"something went wrong", "unexpected error",
	}
	return slices.Contains(generic, lower)
}

// suggestContext derives a human-readable context string from a camelCase or
// PascalCase function name. E.g. "CreateOrder" → "create order".
func suggestContext(funcName string) string {
	if funcName == "" {
		return ""
	}

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
