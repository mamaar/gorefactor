package deepifelse

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers/filedata"
	"github.com/mamaar/gorefactor/pkg/analyzers/srctext"
)

// Result is the typed result returned for MCP consumption.
type Result struct {
	File                       string `json:"file"`
	Line                       int    `json:"line"`
	Column                     int    `json:"column"`
	Function                   string `json:"function_name"`
	NestingDepth               int    `json:"nesting_depth"`
	HappyPathDepth             int    `json:"happy_path_depth"`
	ErrorBranches              int    `json:"error_branches"`
	ComplexityReductionPercent int    `json:"complexity_reduction_estimate"`
	Suggestion                 string `json:"suggestion"`

	// Fixable reports whether a rewrite was attached to this finding. The
	// detector fires on any chain past the depth threshold, but extractChain
	// only rewrites a narrow shape, so a violation is routinely reported with
	// no fix behind it. Reason explains what blocked it.
	Fixable bool   `json:"fixable"`
	Reason  string `json:"reason,omitempty"`
	// EarlyReturns is how many guard clauses the rewrite emits — the real
	// count from the extracted chain, not an estimate from nesting depth.
	// Zero when Fixable is false.
	EarlyReturns int `json:"early_returns"`
}

type config struct {
	maxNesting   int
	minElseLines int
}

// Option configures the analyzer.
type Option func(*config)

// WithMaxNesting sets the maximum acceptable nesting depth.
func WithMaxNesting(n int) Option {
	return func(c *config) { c.maxNesting = n }
}

// WithMinElseLines sets the minimum else branch lines to trigger detection.
func WithMinElseLines(n int) Option {
	return func(c *config) { c.minElseLines = n }
}

var Analyzer = &analysis.Analyzer{
	Name:     "deepifelse",
	Doc:      "detects nested if-else chains that should use early returns (guard clauses)",
	Run:      makeRun(config{maxNesting: 2, minElseLines: 3}),
	Requires: []*analysis.Analyzer{filedata.Analyzer},
}

// NewAnalyzer creates a configured analyzer.
func NewAnalyzer(opts ...Option) *analysis.Analyzer {
	cfg := config{maxNesting: 2, minElseLines: 3}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &analysis.Analyzer{
		Name:     "deepifelse",
		Doc:      "detects nested if-else chains that should use early returns (guard clauses)",
		Run:      makeRun(cfg),
		Requires: []*analysis.Analyzer{filedata.Analyzer},
	}
}

func makeRun(cfg config) func(*analysis.Pass) (any, error) {
	return func(pass *analysis.Pass) (any, error) {
		fd := pass.ResultOf[filedata.Analyzer].(*filedata.Data)
		var results []*Result

		for _, file := range pass.Files {
			pos := pass.Fset.Position(file.Pos())
			content := fd.Content[pos.Filename]
			results = append(results, analyzeFile(pass, file, content, cfg)...)
		}

		return results, nil
	}
}

func analyzeFile(pass *analysis.Pass, file *ast.File, content []byte, cfg config) []*Result {
	var results []*Result

	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok || funcDecl.Body == nil {
			continue
		}
		results = append(results, analyzeFunction(pass, file, content, funcDecl, cfg)...)
	}

	return results
}

func analyzeFunction(pass *analysis.Pass, file *ast.File, content []byte, fn *ast.FuncDecl, cfg config) []*Result {
	var results []*Result

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}

		if ifStmt.Else == nil {
			return true
		}

		depth := measureIfElseDepth(ifStmt, 1)
		if depth <= cfg.maxNesting {
			return true
		}

		elseLines := countElseLines(pass.Fset, ifStmt)
		if elseLines < cfg.minElseLines {
			return true
		}

		errorBranches := countErrorBranches(ifStmt)
		happyDepth := measureHappyPathDepth(ifStmt, 1)

		reductionPercent := 0
		if depth > 0 {
			reductionPercent = ((depth - 1) * 100) / depth
		}

		pos := pass.Fset.Position(ifStmt.Pos())

		// Build suggested fix. Detection is broader than rewriting: a chain
		// deep enough to report is often not a shape extractChain can flatten,
		// so track whether a fix was actually produced.
		var fixes []analysis.SuggestedFix
		fixable := false
		reason := ""
		earlyReturns := 0

		guards, happyPath, ok2 := extractChain(pass.Fset, content, pass.TypesInfo, ifStmt)
		switch {
		case !ok2:
			reason = "chain shape is not a simple guard sequence: every else must be a block ending in return, and each body a single nested if-else"
		default:
			startOffset := pass.Fset.Position(ifStmt.Pos()).Offset
			endOffset := pass.Fset.Position(ifStmt.End()).Offset
			if startOffset < 0 || endOffset > len(content) || startOffset >= endOffset {
				reason = "source text for the chain is unavailable"
				break
			}
			indent := srctext.Indentation(content, startOffset)
			newText := buildReplacementText(guards, happyPath, indent)
			fixes = append(fixes, analysis.SuggestedFix{
				Message: "Flatten deep if-else chain with early returns",
				TextEdits: []analysis.TextEdit{
					{Pos: ifStmt.Pos(), End: ifStmt.End(), NewText: []byte(newText)},
				},
			})
			fixable = true
			earlyReturns = len(guards)
		}

		pass.Report(analysis.Diagnostic{
			Pos:            ifStmt.Pos(),
			End:            ifStmt.End(),
			Message:        fmt.Sprintf("deep if-else chain (depth %d) should use early returns", depth),
			SuggestedFixes: fixes,
		})

		results = append(results, &Result{
			File:                       pos.Filename,
			Line:                       pos.Line,
			Column:                     pos.Column,
			Function:                   fn.Name.Name,
			NestingDepth:               depth,
			HappyPathDepth:             happyDepth,
			ErrorBranches:              errorBranches,
			ComplexityReductionPercent: reductionPercent,
			Suggestion:                 fmt.Sprintf("Invert conditions and use early returns for error cases (depth %d -> 0)", depth),
			Fixable:                    fixable,
			Reason:                     reason,
			EarlyReturns:               earlyReturns,
		})

		return false
	})

	return results
}

func measureIfElseDepth(ifStmt *ast.IfStmt, currentDepth int) int {
	maxDepth := currentDepth

	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			if innerIf.Else != nil {
				d := measureIfElseDepth(innerIf, currentDepth+1)
				if d > maxDepth {
					maxDepth = d
				}
			}
		}
	}

	if elseIf, ok := ifStmt.Else.(*ast.IfStmt); ok {
		if elseIf.Else != nil {
			d := measureIfElseDepth(elseIf, currentDepth+1)
			if d > maxDepth {
				maxDepth = d
			}
		}
	} else if elseBlock, ok := ifStmt.Else.(*ast.BlockStmt); ok {
		for _, stmt := range elseBlock.List {
			if innerIf, ok := stmt.(*ast.IfStmt); ok {
				if innerIf.Else != nil {
					d := measureIfElseDepth(innerIf, currentDepth+1)
					if d > maxDepth {
						maxDepth = d
					}
				}
			}
		}
	}

	return maxDepth
}

func countElseLines(fset *token.FileSet, ifStmt *ast.IfStmt) int {
	if ifStmt.Else == nil {
		return 0
	}

	total := 0

	switch e := ifStmt.Else.(type) {
	case *ast.BlockStmt:
		startLine := fset.Position(e.Lbrace).Line
		endLine := fset.Position(e.Rbrace).Line
		total += endLine - startLine + 1
	case *ast.IfStmt:
		startLine := fset.Position(e.Body.Lbrace).Line
		endLine := fset.Position(e.Body.Rbrace).Line
		total += endLine - startLine + 1
		total += countElseLines(fset, e)
	}

	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			total += countElseLines(fset, innerIf)
		}
	}

	return total
}

func countErrorBranches(ifStmt *ast.IfStmt) int {
	count := 0

	if ifStmt.Else != nil {
		switch e := ifStmt.Else.(type) {
		case *ast.BlockStmt:
			if blockLooksLikeErrorReturn(e) {
				count++
			}
		case *ast.IfStmt:
			count += countErrorBranches(e)
		}
	}

	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			count += countErrorBranches(innerIf)
		}
	}

	return count
}

func blockLooksLikeErrorReturn(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

func measureHappyPathDepth(ifStmt *ast.IfStmt, currentDepth int) int {
	maxDepth := currentDepth

	for _, stmt := range ifStmt.Body.List {
		if innerIf, ok := stmt.(*ast.IfStmt); ok {
			if innerIf.Else != nil {
				d := measureHappyPathDepth(innerIf, currentDepth+1)
				if d > maxDepth {
					maxDepth = d
				}
			}
		}
	}

	return maxDepth
}

// --- Fixer logic (for SuggestedFixes) ---

type guardClause struct {
	invertedCond string
	body         string
}

func extractChain(fset *token.FileSet, content []byte, info *types.Info, ifStmt *ast.IfStmt) ([]guardClause, string, bool) {
	var guards []guardClause

	current := ifStmt
	for {
		if current.Else == nil {
			return nil, "", false
		}

		elseBlock, ok := current.Else.(*ast.BlockStmt)
		if !ok {
			return nil, "", false
		}
		if !blockHasReturn(elseBlock) {
			return nil, "", false
		}

		inverted := srctext.InvertCondition(fset, content, info, current.Cond)
		if inverted == "" {
			return nil, "", false
		}

		elseBody := srctext.BlockText(fset, content, elseBlock)
		guards = append(guards, guardClause{
			invertedCond: inverted,
			body:         elseBody,
		})

		innerIf := singleNestedIfElse(current.Body)
		if innerIf != nil {
			current = innerIf
			continue
		}

		happyPath := srctext.BlockText(fset, content, current.Body)
		return guards, happyPath, true
	}
}

func singleNestedIfElse(block *ast.BlockStmt) *ast.IfStmt {
	if len(block.List) != 1 {
		return nil
	}
	ifStmt, ok := block.List[0].(*ast.IfStmt)
	if !ok || ifStmt.Else == nil {
		return nil
	}
	return ifStmt
}

func blockHasReturn(block *ast.BlockStmt) bool {
	for _, stmt := range block.List {
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

func buildReplacementText(guards []guardClause, happyPath, indent string) string {
	var sb strings.Builder

	for _, g := range guards {
		sb.WriteString("if ")
		sb.WriteString(g.invertedCond)
		sb.WriteString(" {\n")

		for line := range strings.SplitSeq(g.body, "\n") {
			if strings.TrimSpace(line) == "" {
				sb.WriteByte('\n')
				continue
			}
			sb.WriteString(indent)
			sb.WriteByte('\t')
			sb.WriteString(strings.TrimSpace(line))
			sb.WriteByte('\n')
		}

		sb.WriteString(indent)
		sb.WriteString("}\n")
		sb.WriteString(indent)
	}

	lines := strings.Split(happyPath, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if i < len(lines)-1 {
				sb.WriteByte('\n')
			}
			continue
		}
		sb.WriteString(trimmed)
		if i < len(lines)-1 {
			sb.WriteByte('\n')
			sb.WriteString(indent)
		}
	}

	return sb.String()
}
