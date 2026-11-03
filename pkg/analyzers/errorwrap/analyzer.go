// Package errorwrap provides a go/analysis analyzer that detects improper error
// wrapping patterns: bare error returns, fmt.Errorf with %v instead of %w, and
// fmt.Errorf calls with no descriptive context message.
package errorwrap

import (
	"cmp"
	"go/ast"
	"go/token"
	"path"
	"reflect"
	"slices"
	"strconv"
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

	// Skipped marks a violation the fixer leaves alone, for SkipReason.
	Skipped    bool   `json:"skipped,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	// NeedsFmtImport is set when the fix writes fmt.Errorf into a file that
	// does not import fmt.
	NeedsFmtImport bool `json:"-"`
}

const skipExcluded = "excluded"

// Option configures the analyzer.
type Option func(*config)

type config struct {
	severity Severity
	excluded []string
}

// WithExcludedFunctions leaves violations inside the named functions, and the
// function literals within them, unfixed. Names are Func or Type.Method.
func WithExcludedFunctions(names ...string) Option {
	return func(c *config) {
		c.excluded = names
	}
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
		ResultType: reflect.TypeFor[[]*Result](),
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
	var site siteInfo
	funcReturnsError := false
	fmtSkip, needsFmt := fmtAvailability(pass, file)
	var stack []ast.Node

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)

		switch node := n.(type) {
		case *ast.FuncDecl:
			currentFunc = node.Name.Name
			site = siteInfo{content: content, funcName: currentFunc, fmtSkip: fmtSkip, needsFmt: needsFmt}
			if slices.Contains(cfg.excluded, qualifiedName(node)) {
				site.skip = skipExcluded
			}
			funcReturnsError = returnsError(node)
			if !funcReturnsError {
				stack = stack[:len(stack)-1]
				return false
			}
		case *ast.ReturnStmt:
			if !funcReturnsError {
				return true
			}
			site.stack = stack
			violations := checkReturnStmt(pass, cfg, site, node)
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
		ident, ok := field.Type.(*ast.Ident)
		if ok && ident.Name == "error" {
			return true
		}
	}
	return false
}

// siteInfo is what the checks need to know about the function they are in.
type siteInfo struct {
	content  []byte
	funcName string
	skip     string     // why every fix in this function is skipped
	fmtSkip  string     // why fmt.Errorf cannot be written in this file
	needsFmt bool       // the file does not import fmt
	stack    []ast.Node // ancestors of the return being checked, outermost first
}

// qualifiedName returns Func or Type.Method for a declaration.
func qualifiedName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
			continue
		case *ast.IndexExpr:
			t = x.X
			continue
		case *ast.IndexListExpr:
			t = x.X
			continue
		case *ast.ParenExpr:
			t = x.X
			continue
		}
		break
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// fmtAvailability reports why fmt.Errorf cannot be written in file (another
// package is named fmt there), and whether the file lacks the fmt import.
func fmtAvailability(pass *analysis.Pass, file *ast.File) (skip string, needsImport bool) {
	if pass.Pkg.Scope().Lookup("fmt") != nil {
		return "the package declares an identifier named fmt", false
	}
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(importPath)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if importPath == "fmt" && name == "fmt" {
			return "", false
		}
		if name == "fmt" {
			return "the file imports " + importPath + " under the name fmt", false
		}
	}
	return "", true
}

func checkReturnStmt(pass *analysis.Pass, cfg *config, site siteInfo, ret *ast.ReturnStmt) []*Result {
	var results []*Result

	for _, expr := range ret.Results {
		r := checkBareReturn(pass, cfg, site, ret, expr)
		if r != nil {
			results = append(results, r)
		}
		r = checkFmtErrorf(pass, cfg, site, ret, expr)
		if r != nil {
			results = append(results, r)
		}
	}

	return results
}

func checkBareReturn(pass *analysis.Pass, cfg *config, site siteInfo, ret *ast.ReturnStmt, expr ast.Expr) *Result {
	content, funcName := site.content, site.funcName
	ident, ok := expr.(*ast.Ident)
	if !ok || !isErrorVarName(ident.Name) {
		return nil
	}

	if !matchesSeverity(cfg.severity, SeverityCritical) || !provablyNonNil(site.stack, ret, ident.Name) {
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

	skip := cmp.Or(site.skip, site.fmtSkip)
	if skip != "" {
		fixes = nil
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
		Skipped:           skip != "",
		SkipReason:        skip,
		NeedsFmtImport:    skip == "" && site.needsFmt,
	}
}

// provablyNonNil reports whether the variable name is non-nil at ret: ret sits
// in a branch of an enclosing if whose condition proves it, and nothing in
// that branch assigns the name before ret.
func provablyNonNil(stack []ast.Node, ret *ast.ReturnStmt, name string) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		ifStmt, ok := stack[i].(*ast.IfStmt)
		if !ok {
			continue
		}
		branch := stack[i+1]
		var proven bool
		switch branch {
		case ifStmt.Body:
			proven = nonNilWhen(ifStmt.Cond, name, true)
		case ifStmt.Else:
			proven = nonNilWhen(ifStmt.Cond, name, false)
		}
		if proven && !assignsBefore(branch, name, ret.Pos()) {
			return true
		}
	}
	return false
}

// nonNilWhen reports whether cond evaluating to truth implies name != nil.
func nonNilWhen(cond ast.Expr, name string, truth bool) bool {
	switch c := ast.Unparen(cond).(type) {
	case *ast.UnaryExpr:
		return c.Op == token.NOT && nonNilWhen(c.X, name, !truth)
	case *ast.BinaryExpr:
		switch {
		case c.Op == token.LAND && truth, c.Op == token.LOR && !truth:
			return nonNilWhen(c.X, name, truth) || nonNilWhen(c.Y, name, truth)
		case c.Op == token.NEQ && truth, c.Op == token.EQL && !truth:
			return comparesToNil(c, name)
		}
	}
	return false
}

func comparesToNil(c *ast.BinaryExpr, name string) bool {
	x, xok := c.X.(*ast.Ident)
	y, yok := c.Y.(*ast.Ident)
	return xok && yok && (x.Name == name && y.Name == "nil" || y.Name == name && x.Name == "nil")
}

// assignsBefore reports whether anything under root before pos may write name.
func assignsBefore(root ast.Node, name string, pos token.Pos) bool {
	isName := func(e ast.Expr) bool {
		id, ok := ast.Unparen(e).(*ast.Ident)
		return ok && id.Name == name
	}
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil || n.Pos() >= pos || found {
			return false
		}
		switch s := n.(type) {
		case *ast.AssignStmt:
			found = slices.ContainsFunc(s.Lhs, isName)
		case *ast.IncDecStmt:
			found = isName(s.X)
		case *ast.RangeStmt:
			found = s.Key != nil && isName(s.Key) || s.Value != nil && isName(s.Value)
		case *ast.UnaryExpr:
			found = s.Op == token.AND && isName(s.X)
		case *ast.ValueSpec:
			found = slices.ContainsFunc(s.Names, func(id *ast.Ident) bool { return id.Name == name })
		}
		return !found
	})
	return found
}

func checkFmtErrorf(pass *analysis.Pass, cfg *config, site siteInfo, ret *ast.ReturnStmt, expr ast.Expr) *Result {
	content, funcName := site.content, site.funcName
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

			if site.skip != "" {
				fixes = nil
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
				Skipped:           site.skip != "",
				SkipReason:        site.skip,
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
