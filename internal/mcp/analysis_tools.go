package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/booleanbranch"
	"github.com/mamaar/gorefactor/pkg/analyzers/complexity"
	"github.com/mamaar/gorefactor/pkg/analyzers/deepifelse"
	"github.com/mamaar/gorefactor/pkg/analyzers/envbool"
	"github.com/mamaar/gorefactor/pkg/analyzers/errorwrap"
	"github.com/mamaar/gorefactor/pkg/analyzers/ifinit"
	"github.com/mamaar/gorefactor/pkg/analyzers/missingctx"
	"github.com/mamaar/gorefactor/pkg/types"
)

// --- analyze_symbol ---

type AnalyzeSymbolInput struct {
	Symbol  string `json:"symbol" jsonschema:"symbol name to analyze"`
	Package string `json:"package,omitempty" jsonschema:"package path to search in (empty for workspace-wide search)"`
}

type SymbolInfo struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Package   string          `json:"package"`
	File      string          `json:"file"`
	Line      int             `json:"line"`
	Column    int             `json:"column"`
	Exported  bool            `json:"exported"`
	Signature string          `json:"signature,omitempty"`
	RefCount  int             `json:"reference_count"`
	Refs      []ReferenceInfo `json:"references,omitempty"`
}

type ReferenceInfo struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// --- complexity ---

type ComplexityInput struct {
	Package       string `json:"package,omitempty" jsonschema:"package path to analyze (empty for entire workspace)"`
	MinComplexity int    `json:"min_complexity,omitempty" jsonschema:"minimum cyclomatic complexity threshold (default 10)"`
}

type ComplexityResultItem struct {
	Function             string `json:"function"`
	File                 string `json:"file"`
	Line                 int    `json:"line"`
	CyclomaticComplexity int    `json:"cyclomatic_complexity"`
	CognitiveComplexity  int    `json:"cognitive_complexity"`
	LinesOfCode          int    `json:"lines_of_code"`
	Parameters           int    `json:"parameters"`
	MaxNestingDepth      int    `json:"max_nesting_depth"`
	Level                string `json:"level"`
}

// --- unused ---

type UnusedInput struct {
	IncludeExported bool   `json:"include_exported,omitempty" jsonschema:"include exported (public) symbols in results"`
	Package         string `json:"package,omitempty" jsonschema:"filter to a specific package"`
}

type UnusedSymbolItem struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Package      string `json:"package"`
	File         string `json:"file"`
	Line         int    `json:"line"`
	Exported     bool   `json:"exported"`
	SafeToDelete bool   `json:"safe_to_delete"`
	Reason       string `json:"reason"`
}

// --- detect_if_init_assignments ---

type DetectIfInitInput struct {
	Package string `json:"package,omitempty" jsonschema:"specific package to analyze"`
}

type IfInitViolationItem struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Column     int      `json:"column"`
	Variables  []string `json:"variables"`
	Expression string   `json:"expression"`
	Snippet    string   `json:"snippet"`
	Function   string   `json:"function"`
}

// --- fix_if_init_assignments ---

type FixIfInitInput struct {
	Package string `json:"package,omitempty" jsonschema:"specific package to fix"`
}

// --- detect_missing_context_params ---

type DetectMissingContextInput struct {
	Package string `json:"package,omitempty" jsonschema:"specific package to analyze"`
}

type MissingContextViolationItem struct {
	File         string   `json:"file"`
	Line         int      `json:"line"`
	Column       int      `json:"column"`
	FunctionName string   `json:"function_name"`
	Signature    string   `json:"signature"`
	ContextCalls []string `json:"context_calls"`
}

// --- detect_boolean_branching ---

type DetectBooleanBranchingInput struct {
	Package     string `json:"package,omitempty" jsonschema:"specific package to analyze"`
	MinBranches int    `json:"min_branches,omitempty" jsonschema:"minimum number of boolean branches from the same source to trigger a violation (default 2)"`
}

type BooleanBranchingViolationItem struct {
	File             string   `json:"file"`
	Line             int      `json:"line"`
	Column           int      `json:"column"`
	Function         string   `json:"function"`
	SourceVariable   string   `json:"source_variable"`
	BooleanVariables []string `json:"boolean_variables"`
	BranchCount      int      `json:"branch_count"`
	Suggestion       string   `json:"suggestion"`
}

// --- fix_boolean_branching ---

type FixBooleanBranchingInput struct {
	Package     string `json:"package,omitempty" jsonschema:"specific package to fix"`
	MinBranches int    `json:"min_branches,omitempty" jsonschema:"minimum number of boolean branches from the same source to fix (default 2)"`
}

// --- detect_deep_if_else_chains ---

type DetectDeepIfElseInput struct {
	Package         string `json:"package,omitempty" jsonschema:"specific package to analyze"`
	MaxNestingDepth int    `json:"max_nesting_depth,omitempty" jsonschema:"maximum acceptable nesting depth (default 2)"`
	MinElseLines    int    `json:"min_else_lines,omitempty" jsonschema:"minimum lines in else to trigger detection (default 3)"`
}

type DeepIfElseViolationItem struct {
	File                       string `json:"file"`
	Line                       int    `json:"line"`
	Column                     int    `json:"column"`
	Function                   string `json:"function_name"`
	NestingDepth               int    `json:"nesting_depth"`
	HappyPathDepth             int    `json:"happy_path_depth"`
	ErrorBranches              int    `json:"error_branches"`
	ComplexityReductionPercent int    `json:"complexity_reduction_estimate"`
	Suggestion                 string `json:"suggestion"`
}

// --- fix_deep_if_else_chains ---

type FixDeepIfElseInput struct {
	Package         string `json:"package,omitempty" jsonschema:"specific package to fix"`
	MaxNestingDepth int    `json:"max_nesting_depth,omitempty" jsonschema:"maximum acceptable nesting depth (default 2)"`
	MinElseLines    int    `json:"min_else_lines,omitempty" jsonschema:"minimum lines in else to trigger fix (default 3)"`
}

// --- detect_improper_error_wrapping ---

type DetectImproperErrorWrappingInput struct {
	Package       string `json:"package,omitempty" jsonschema:"specific package to analyze"`
	SeverityLevel string `json:"severity_level,omitempty" jsonschema:"filter by severity: critical, warning, or info (default critical)"`
}

type ErrorWrappingViolationItem struct {
	File              string `json:"file"`
	Line              int    `json:"line"`
	Column            int    `json:"column"`
	Function          string `json:"function_name"`
	ViolationType     string `json:"violation_type"`
	CurrentCode       string `json:"current_code"`
	ContextSuggestion string `json:"context_suggestion"`
	Severity          string `json:"severity"`
}

// --- fix_error_wrapping ---

type FixErrorWrappingInput struct {
	Package       string `json:"package,omitempty" jsonschema:"specific package to fix"`
	SeverityLevel string `json:"severity_level,omitempty" jsonschema:"fix violations at this severity or higher: critical, warning, or info (default critical)"`
}

// --- detect_environment_booleans ---

type DetectEnvBooleansInput struct {
	Package  string `json:"package,omitempty" jsonschema:"specific package to analyze"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"maximum propagation depth before flagging (default 1)"`
}

type EnvBooleanViolationItem struct {
	File             string   `json:"file"`
	Line             int      `json:"line"`
	Column           int      `json:"column"`
	Function         string   `json:"function_name"`
	ParameterName    string   `json:"parameter_name"`
	ParameterType    string   `json:"parameter_type"`
	PropagationDepth int      `json:"propagation_depth"`
	CallChain        []string `json:"call_chain"`
	SuggestedPattern string   `json:"suggested_pattern"`
	Suggestion       string   `json:"suggestion"`
}

// --- analyze_dependencies ---

type AnalyzeDependenciesInput struct {
	DetectBackwards bool `json:"detect_backwards,omitempty" jsonschema:"detect backwards dependencies"`
	SuggestMoves    bool `json:"suggest_moves,omitempty" jsonschema:"suggest symbol moves to improve structure"`
}

func registerAnalysisTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "analyze_symbol",
		Description: "Analyze a symbol: find its definition, properties, and all references across the workspace.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AnalyzeSymbolInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		var symbol *types.Symbol
		if in.Package != "" {
			resolved := types.ResolvePackagePath(ws, in.Package)
			if pkg, ok := ws.Packages[resolved]; ok {
				symbol = pkg.Symbols.FindSymbol(in.Symbol)
			}
		} else {
			for _, pkg := range ws.Packages {
				if s := pkg.Symbols.FindSymbol(in.Symbol); s != nil {
					symbol = s
					break
				}
			}
		}
		if symbol == nil {
			return errResult(fmt.Errorf("symbol %s not found", in.Symbol)), nil, nil
		}

		info := SymbolInfo{
			Name:      symbol.Name,
			Kind:      symbol.Kind.String(),
			Package:   symbol.Package,
			File:      symbol.File,
			Line:      symbol.Line,
			Column:    symbol.Column,
			Exported:  symbol.Exported,
			Signature: symbol.Signature,
			RefCount:  len(symbol.References),
		}
		for _, ref := range symbol.References {
			info.Refs = append(info.Refs, ReferenceInfo{
				File:   ref.File,
				Line:   ref.Line,
				Column: ref.Column,
			})
		}
		return textResult(info), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "complexity",
		Description: "Analyze cyclomatic and cognitive complexity of functions. Returns functions exceeding the threshold, sorted by complexity.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ComplexityInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		minC := in.MinComplexity
		if minC <= 0 {
			minC = 10
		}

		a := complexity.NewAnalyzer(complexity.WithMinComplexity(minC))
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []ComplexityResultItem
		if results, ok := rr.Result.([]*complexity.Result); ok {
			items = make([]ComplexityResultItem, len(results))
			for i, r := range results {
				items[i] = ComplexityResultItem{
					Function:             r.Function,
					File:                 r.File,
					Line:                 r.Line,
					CyclomaticComplexity: r.CyclomaticComplexity,
					CognitiveComplexity:  r.CognitiveComplexity,
					LinesOfCode:          r.LinesOfCode,
					Parameters:           r.Parameters,
					MaxNestingDepth:      r.MaxNestingDepth,
					Level:                r.Level,
				}
			}
		}
		return textResult(map[string]any{
			"results":        items,
			"count":          len(items),
			"min_complexity": minC,
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "unused",
		Description: "Find unused symbols in the workspace. By default only shows unexported symbols that are safe to delete.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in UnusedInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		analyzer := analysis.NewUnusedAnalyzer(ws, state.logger)
		if in.IncludeExported {
			analyzer.SetIncludeExported(true)
		}

		var unused []*analysis.UnusedSymbol
		if in.IncludeExported {
			unused, err = analyzer.FindUnusedSymbols()
		} else {
			unused, err = analyzer.GetUnusedUnexportedSymbols()
		}
		if err != nil {
			return errResult(err), nil, nil
		}

		// Filter by package if specified.
		if in.Package != "" {
			var filtered []*analysis.UnusedSymbol
			for _, u := range unused {
				if u.Symbol.Package == in.Package || u.Symbol.Package == types.ResolvePackagePath(ws, in.Package) {
					filtered = append(filtered, u)
				}
			}
			unused = filtered
		}

		items := make([]UnusedSymbolItem, len(unused))
		for i, u := range unused {
			items[i] = UnusedSymbolItem{
				Name:         u.Symbol.Name,
				Kind:         u.Symbol.Kind.String(),
				Package:      u.Symbol.Package,
				File:         u.Symbol.File,
				Line:         u.Symbol.Line,
				Exported:     u.Symbol.Exported,
				SafeToDelete: u.SafeToDelete,
				Reason:       u.Reason,
			}
		}
		return textResult(map[string]any{
			"unused_symbols": items,
			"count":          len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_if_init_assignments",
		Description: "Detect if-init assignment statements (e.g., `if x, err := f(); err != nil {}`) that violate coding conventions requiring separate assignment and error check.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectIfInitInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		rr, err := analyzers.Run(ws, ifinit.Analyzer, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []IfInitViolationItem
		if results, ok := rr.Result.([]*ifinit.Result); ok {
			items = make([]IfInitViolationItem, len(results))
			for i, v := range results {
				items[i] = IfInitViolationItem{
					File:       v.File,
					Line:       v.Line,
					Column:     v.Column,
					Variables:  v.Variables,
					Expression: v.Expression,
					Snippet:    v.Snippet,
					Function:   v.Function,
				}
			}
		}
		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "fix_if_init_assignments",
		Description: "Automatically fix if-init assignment statements by splitting them into separate assignment and if-check statements.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixIfInitInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		rr, err := analyzers.Run(ws, ifinit.Analyzer, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
		if len(changes) == 0 {
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified": []string{},
				"changes_count":  0,
				"message":        "No if-init assignment violations found",
			}), nil, nil
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Fix if-init assignments")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(map[string]any{
			"files_modified": result.ModifiedFiles,
			"changes_count":  result.ChangeCount,
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_missing_context_params",
		Description: "Detect functions that should accept ctx context.Context as a parameter but instead create context internally via context.TODO() or context.Background().",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectMissingContextInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		rr, err := analyzers.Run(ws, missingctx.Analyzer, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []MissingContextViolationItem
		if results, ok := rr.Result.([]*missingctx.Result); ok {
			items = make([]MissingContextViolationItem, len(results))
			for i, v := range results {
				items[i] = MissingContextViolationItem{
					File:         v.File,
					Line:         v.Line,
					Column:       v.Column,
					FunctionName: v.FunctionName,
					Signature:    v.Signature,
					ContextCalls: v.ContextCalls,
				}
			}
		}
		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_boolean_branching",
		Description: "Detect intermediate boolean variables used for branching that should be switch statements instead. E.g., `wantShapefile := accept == \"x-shapefile\"` followed by `if wantShapefile {` should be `switch accept { case \"x-shapefile\": }`.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectBooleanBranchingInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		minBranches := in.MinBranches
		if minBranches <= 0 {
			minBranches = 2
		}
		a := booleanbranch.NewAnalyzer(booleanbranch.WithMinBranches(minBranches))
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []BooleanBranchingViolationItem
		if results, ok := rr.Result.([]*booleanbranch.Result); ok {
			items = make([]BooleanBranchingViolationItem, len(results))
			for i, v := range results {
				items[i] = BooleanBranchingViolationItem{
					File:             v.File,
					Line:             v.Line,
					Column:           v.Column,
					Function:         v.Function,
					SourceVariable:   v.SourceVariable,
					BooleanVariables: v.BooleanVariables,
					BranchCount:      v.BranchCount,
					Suggestion:       v.Suggestion,
				}
			}
		}
		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "fix_boolean_branching",
		Description: "Convert boolean-based if/else chains to switch statements. Removes intermediate boolean variables and generates clean switch cases.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixBooleanBranchingInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		minBranches := in.MinBranches
		if minBranches <= 0 {
			minBranches = 2
		}
		a := booleanbranch.NewAnalyzer(booleanbranch.WithMinBranches(minBranches))
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
		if len(changes) == 0 {
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified":   []string{},
				"changes_count":    0,
				"booleans_removed": []string{},
				"switch_cases":     0,
				"message":          "No boolean branching violations found",
			}), nil, nil
		}

		// Collect fix metadata from results.
		var allBooleansRemoved []string
		totalCases := 0
		if results, ok := rr.Result.([]*booleanbranch.Result); ok {
			for _, r := range results {
				allBooleansRemoved = append(allBooleansRemoved, r.BooleanVariables...)
				totalCases += r.BranchCount
			}
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Fix boolean branching")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(map[string]any{
			"files_modified":   result.ModifiedFiles,
			"changes_count":    result.ChangeCount,
			"booleans_removed": allBooleansRemoved,
			"switch_cases":     totalCases,
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_deep_if_else_chains",
		Description: "Detect nested if-else chains that should use early returns (guard clauses). Reports nesting depth, happy path depth, error branch count, and complexity reduction estimate.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectDeepIfElseInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		maxNesting := in.MaxNestingDepth
		if maxNesting <= 0 {
			maxNesting = 2
		}
		minElseLines := in.MinElseLines
		if minElseLines <= 0 {
			minElseLines = 3
		}
		a := deepifelse.NewAnalyzer(
			deepifelse.WithMaxNesting(maxNesting),
			deepifelse.WithMinElseLines(minElseLines),
		)
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []DeepIfElseViolationItem
		if results, ok := rr.Result.([]*deepifelse.Result); ok {
			items = make([]DeepIfElseViolationItem, len(results))
			for i, v := range results {
				items[i] = DeepIfElseViolationItem{
					File:                       v.File,
					Line:                       v.Line,
					Column:                     v.Column,
					Function:                   v.Function,
					NestingDepth:               v.NestingDepth,
					HappyPathDepth:             v.HappyPathDepth,
					ErrorBranches:              v.ErrorBranches,
					ComplexityReductionPercent: v.ComplexityReductionPercent,
					Suggestion:                 v.Suggestion,
				}
			}
		}
		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "fix_deep_if_else_chains",
		Description: "Automatically invert conditions and use early returns to flatten deep if-else chains. Conservative mode only fixes simple patterns where each else branch contains a return statement.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixDeepIfElseInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		maxNesting := in.MaxNestingDepth
		if maxNesting <= 0 {
			maxNesting = 2
		}
		minElseLines := in.MinElseLines
		if minElseLines <= 0 {
			minElseLines = 3
		}
		a := deepifelse.NewAnalyzer(
			deepifelse.WithMaxNesting(maxNesting),
			deepifelse.WithMinElseLines(minElseLines),
		)
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
		if len(changes) == 0 {
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified":  []string{},
				"changes_count":   0,
				"functions_fixed": []string{},
				"early_returns":   0,
				"message":         "No deep if-else chain violations found",
			}), nil, nil
		}

		var functionsFixed []string
		totalEarlyReturns := 0
		if results, ok := rr.Result.([]*deepifelse.Result); ok {
			for _, r := range results {
				functionsFixed = append(functionsFixed, r.Function)
				totalEarlyReturns += r.NestingDepth // approximate early returns from depth
			}
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Fix deep if-else chains")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(map[string]any{
			"files_modified":  result.ModifiedFiles,
			"changes_count":   result.ChangeCount,
			"functions_fixed": functionsFixed,
			"early_returns":   totalEarlyReturns,
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_improper_error_wrapping",
		Description: "Detect errors returned without wrapping context or using wrong format verb. Finds bare `return err`, `fmt.Errorf` with `%v` instead of `%w`, and error messages without descriptive context.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectImproperErrorWrappingInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		sev := errorwrap.Severity(in.SeverityLevel)
		if sev == "" {
			sev = errorwrap.SeverityCritical
		}
		a := errorwrap.NewAnalyzer(errorwrap.WithSeverity(sev))
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []ErrorWrappingViolationItem
		if results, ok := rr.Result.([]*errorwrap.Result); ok {
			items = make([]ErrorWrappingViolationItem, len(results))
			for i, v := range results {
				items[i] = ErrorWrappingViolationItem{
					File:              v.File,
					Line:              v.Line,
					Column:            v.Column,
					Function:          v.Function,
					ViolationType:     v.ViolationType,
					CurrentCode:       v.CurrentCode,
					ContextSuggestion: v.ContextSuggestion,
					Severity:          v.Severity,
				}
			}
		}
		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "fix_error_wrapping",
		Description: "Automatically fix improper error wrapping. Wraps bare `return err` with `fmt.Errorf`, replaces `%v` with `%w`, and replaces generic error messages with descriptive context derived from the function name.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixErrorWrappingInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		sev := errorwrap.Severity(in.SeverityLevel)
		if sev == "" {
			sev = errorwrap.SeverityCritical
		}
		a := errorwrap.NewAnalyzer(errorwrap.WithSeverity(sev))
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
		if len(changes) == 0 {
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified":     []string{},
				"changes_count":      0,
				"errors_wrapped":     0,
				"format_verbs_fixed": 0,
				"contexts_added":     0,
				"message":            "No error wrapping violations found",
			}), nil, nil
		}

		// Count fix types from results.
		errorsWrapped := 0
		formatVerbsFixed := 0
		contextsAdded := 0
		if results, ok := rr.Result.([]*errorwrap.Result); ok {
			for _, r := range results {
				switch r.ViolationType {
				case errorwrap.BareReturn:
					errorsWrapped++
				case errorwrap.FormatVerbV:
					formatVerbsFixed++
				case errorwrap.NoContext:
					contextsAdded++
				}
			}
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Fix error wrapping")
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(map[string]any{
			"files_modified":     result.ModifiedFiles,
			"changes_count":      result.ChangeCount,
			"errors_wrapped":     errorsWrapped,
			"format_verbs_fixed": formatVerbsFixed,
			"contexts_added":     contextsAdded,
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_environment_booleans",
		Description: "Detect isProd/isTest/devMode boolean parameters passed down call stacks. These should be replaced with interface implementations or concrete values resolved at initialization time.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectEnvBooleansInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		maxDepth := in.MaxDepth
		if maxDepth <= 0 {
			maxDepth = 1
		}
		a := envbool.NewAnalyzer(envbool.WithMaxDepth(maxDepth))
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []EnvBooleanViolationItem
		if results, ok := rr.Result.([]*envbool.Result); ok {
			items = make([]EnvBooleanViolationItem, len(results))
			for i, v := range results {
				items[i] = EnvBooleanViolationItem{
					File:             v.File,
					Line:             v.Line,
					Column:           v.Column,
					Function:         v.Function,
					ParameterName:    v.ParameterName,
					ParameterType:    v.ParameterType,
					PropagationDepth: v.PropagationDepth,
					CallChain:        v.CallChain,
					SuggestedPattern: v.SuggestedPattern,
					Suggestion:       v.Suggestion,
				}
			}
		}
		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "analyze_dependencies",
		Description: "Analyze the dependency graph of the workspace. Optionally detect backwards dependencies and suggest moves.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AnalyzeDependenciesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().AnalyzeDependencies(ws, types.AnalyzeDependenciesRequest{
			Workspace:           ws.RootPath,
			DetectBackwardsDeps: in.DetectBackwards,
			SuggestMoves:        in.SuggestMoves,
		})
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(map[string]any{
			"affected_files":   plan.AffectedFiles,
			"change_count":     len(plan.Changes),
			"impact":           plan.Impact,
			"dependency_graph": ws.Dependencies,
		}), nil, nil
	})
}
