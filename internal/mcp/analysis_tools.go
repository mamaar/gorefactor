package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	goanalysis "golang.org/x/tools/go/analysis"

	"github.com/mamaar/gorefactor/pkg/analyzers"
	"github.com/mamaar/gorefactor/pkg/analyzers/booleanbranch"
	"github.com/mamaar/gorefactor/pkg/analyzers/boolsimplify"
	"github.com/mamaar/gorefactor/pkg/analyzers/complexity"
	"github.com/mamaar/gorefactor/pkg/analyzers/deepifelse"
	"github.com/mamaar/gorefactor/pkg/analyzers/earlyreturn"
	"github.com/mamaar/gorefactor/pkg/analyzers/envbool"
	"github.com/mamaar/gorefactor/pkg/analyzers/errorwrap"
	"github.com/mamaar/gorefactor/pkg/analyzers/ifinit"
	"github.com/mamaar/gorefactor/pkg/analyzers/missingctx"
	"github.com/mamaar/gorefactor/pkg/analyzers/nilnil"
	"github.com/mamaar/gorefactor/pkg/analyzers/nilreceiver"
	"github.com/mamaar/gorefactor/pkg/analyzers/switchchain"
	"github.com/mamaar/gorefactor/pkg/refactor"
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
	// Declarations is set when build configurations declare the symbol in
	// more than one place; File, Line and Column are the first configuration's.
	Declarations []DeclarationInfo `json:"declarations,omitempty"`
	// BuildConfigs is set when there is more than one build configuration.
	BuildConfigs []string `json:"build_configs,omitempty"`
	// UncoveredFiles are files next to the symbol or its references that no
	// configuration type-checks, so references in them are missing.
	UncoveredFiles []UncoveredFileInfo `json:"uncovered_files,omitempty"`
}

type DeclarationInfo struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Column  int      `json:"column"`
	Configs []string `json:"configs,omitempty"`
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

// --- reachable_references ---

type ReachableReferencesInput struct {
	EntryPackage string   `json:"entry_package" jsonschema:"the package main to start from, as import path, directory or workspace-relative path"`
	Targets      []string `json:"targets" jsonschema:"functions or methods to find references to: pkg.Func, pkg.Type.Method, or a package path for all its exported funcs and methods"`
	IncludeTests bool     `json:"include_tests,omitempty" jsonschema:"also treat test functions of the examined packages as entry points"`
	Within       string   `json:"within_function,omitempty" jsonschema:"only report sites in code this function (pkg.Func or pkg.Type.Method) reaches; the live types still come from the whole entry_package program"`
}

type ReachableSiteItem struct {
	Package   string   `json:"package"`
	File      string   `json:"file"`
	Line      int      `json:"line"`
	Enclosing string   `json:"enclosing"`
	Target    string   `json:"target"`
	Kind      string   `json:"kind"`
	Configs   []string `json:"configs,omitempty"`
	Path      []string `json:"path"`
}

type BodilessPackageItem struct {
	Package string   `json:"package"`
	Reason  string   `json:"reason"`
	Configs []string `json:"configs,omitempty"`
}

// --- unused ---

type UnusedInput struct {
	IncludeExported bool   `json:"include_exported,omitempty" jsonschema:"include exported (public) symbols, judged by use within the workspace only"`
	Package         string `json:"package,omitempty" jsonschema:"restrict the report to one package, given as import path, directory or workspace-relative path"`
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
	// Configs names the build configurations that declare the symbol. Omitted
	// when every configuration does.
	Configs []string `json:"configs,omitempty"`
	// ReferencedFrom are up to five declarations, tests or unused code, that
	// refer to the symbol without keeping it alive.
	ReferencedFrom []ReferrerItem `json:"referenced_from,omitempty"`
	// PossibleUse is where a file no build configuration type-checks names
	// the symbol.
	PossibleUse *PossibleUseItem `json:"possible_use,omitempty"`
}

type ReferrerItem struct {
	Name string `json:"name"`
	File string `json:"file"`
	Line int    `json:"line"`
}

type PossibleUseItem struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Configs []string `json:"configs,omitempty"`
	Reason  string   `json:"reason"`
}

// findUnused runs the unused analysis under every build configuration. The
// caller holds the read lock.
func findUnused(state *MCPServer, in UnusedInput) (*refactor.UnusedReport, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, err
	}
	req := refactor.UnusedRequest{IncludeExported: in.IncludeExported}
	if in.Package != "" {
		req.Packages = []string{in.Package, types.ResolvePackagePath(ws, in.Package)}
	}
	return state.GetEngine().UnusedAcrossConfigs(req)
}

func unusedItems(findings []refactor.UnusedFinding) []UnusedSymbolItem {
	items := make([]UnusedSymbolItem, len(findings))
	for i, f := range findings {
		items[i] = UnusedSymbolItem{
			Name:         f.Symbol.Name,
			Kind:         f.Symbol.Kind.String(),
			Package:      f.Symbol.Package,
			File:         f.Symbol.File,
			Line:         f.Symbol.Line,
			Exported:     f.Symbol.Exported,
			SafeToDelete: f.SafeToDelete,
			Reason:       f.Reason,
			Configs:      f.Configs,
		}
		for _, ref := range f.Referrers {
			items[i].ReferencedFrom = append(items[i].ReferencedFrom, ReferrerItem{Name: ref.Name, File: ref.File, Line: ref.Line})
		}
		if use := f.PossibleUse; use != nil {
			items[i].PossibleUse = &PossibleUseItem{File: use.File, Line: use.Line, Configs: use.Configs, Reason: use.Unverified}
		}
	}
	return items
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
	Kind       string   `json:"kind"`
	IsElseIf   bool     `json:"is_else_if"`
	Skipped    bool     `json:"skipped"`
	SkipReason string   `json:"skip_reason,omitempty"`
}

// --- fix_if_init_assignments ---

type FixIfInitInput struct {
	Package string `json:"package,omitempty" jsonschema:"specific package to fix"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// IfInitSkippedItem describes a site the detector reported but could not
// rewrite safely (Result.Skipped == true, and therefore no SuggestedFix /
// types.Change was produced for it).
type IfInitSkippedItem struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Function   string `json:"function"`
	Snippet    string `json:"snippet"`
	SkipReason string `json:"skip_reason"`
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

// --- detect_variadics ---

type DetectVariadicsInput struct {
	Package string `json:"package,omitempty" jsonschema:"list only the declarations of this package; calls are still counted across the workspace"`
}

type VariadicItem struct {
	Function  string   `json:"function"`
	Signature string   `json:"signature"`
	File      string   `json:"file"`
	Line      int      `json:"line"`
	Column    int      `json:"column"`
	Configs   []string `json:"configs,omitempty"`
	CallSites int      `json:"call_sites"`
	ZeroArgs  int      `json:"zero_args"`
	OneArg    int      `json:"one_arg"`
	ManyArgs  int      `json:"many_args"`
	Spread    int      `json:"spread"`
	ValueUses int      `json:"value_uses"`
}

type VariadicTypeErrorItem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

// --- detect_nil_receiver_checks ---

type DetectNilReceiverChecksInput struct {
	Package string `json:"package,omitempty" jsonschema:"specific package to analyze"`
}

// --- detect_nil_nil_returns ---

type DetectNilNilReturnsInput struct {
	Package string `json:"package,omitempty" jsonschema:"specific package to analyze"`
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
	DryRun      bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
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
	// Fixable reports whether fix_deep_if_else_chains would actually rewrite
	// this chain. Detection is broader than rewriting, so a violation with
	// fixable=false needs to be flattened by hand.
	Fixable bool   `json:"fixable"`
	Reason  string `json:"reason,omitempty"`
}

// DeepIfElseUnfixableItem is a detected chain the fixer left alone, with the
// reason it could not be rewritten.
type DeepIfElseUnfixableItem struct {
	File         string `json:"file"`
	Line         int    `json:"line"`
	Function     string `json:"function_name"`
	NestingDepth int    `json:"nesting_depth"`
	Reason       string `json:"reason"`
}

// --- fix_deep_if_else_chains ---

type FixDeepIfElseInput struct {
	Package         string `json:"package,omitempty" jsonschema:"specific package to fix"`
	MaxNestingDepth int    `json:"max_nesting_depth,omitempty" jsonschema:"maximum acceptable nesting depth (default 2)"`
	MinElseLines    int    `json:"min_else_lines,omitempty" jsonschema:"minimum lines in else to trigger fix (default 3)"`
	DryRun          bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- detect_switchable_if_chains ---

type DetectSwitchableIfChainsInput struct {
	Package     string   `json:"package,omitempty" jsonschema:"specific package to analyze"`
	MinBranches int      `json:"min_branches,omitempty" jsonschema:"minimum number of branches (case clauses) a chain must have (default 3)"`
	Kinds       []string `json:"kinds,omitempty" jsonschema:"restrict to these switch forms: tagged, tagless, type_switch (default all)"`
}

type SwitchableIfChainViolationItem struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Column     int      `json:"column"`
	Function   string   `json:"function_name"`
	Kind       string   `json:"kind"`
	Branches   int      `json:"branches"`
	Tag        string   `json:"tag,omitempty"`
	Cases      []string `json:"cases,omitempty"`
	HasDefault bool     `json:"has_default"`
	// Fixable reports whether fix_switchable_if_chains would actually rewrite
	// this chain. Detection is broader than rewriting, so a violation with
	// fixable=false needs to be converted by hand.
	Fixable bool   `json:"fixable"`
	Reason  string `json:"reason,omitempty"`
}

// SwitchableIfChainUnfixableItem is a detected chain the fixer left alone,
// with the reason it could not be rewritten.
type SwitchableIfChainUnfixableItem struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function_name"`
	Kind     string `json:"kind"`
	Branches int    `json:"branches"`
	Reason   string `json:"reason"`
}

// --- fix_switchable_if_chains ---

type FixSwitchableIfChainsInput struct {
	Package     string   `json:"package,omitempty" jsonschema:"specific package to fix"`
	MinBranches int      `json:"min_branches,omitempty" jsonschema:"minimum number of branches (case clauses) a chain must have (default 3)"`
	Kinds       []string `json:"kinds,omitempty" jsonschema:"restrict to these switch forms: tagged, tagless, type_switch (default all)"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- detect_early_returns / fix_early_returns ---

type DetectEarlyReturnsInput struct {
	Package      string   `json:"package,omitempty" jsonschema:"specific package to analyze"`
	MinBodyLines int      `json:"min_body_lines,omitempty" jsonschema:"minimum lines a body must span before inverting is suggested (default 3); does not apply to else_after_return"`
	Patterns     []string `json:"patterns,omitempty" jsonschema:"restrict to these patterns: guard_clause, loop_continue, else_after_return (default all)"`
}

type FixEarlyReturnsInput struct {
	Package      string   `json:"package,omitempty" jsonschema:"specific package to fix"`
	MinBodyLines int      `json:"min_body_lines,omitempty" jsonschema:"minimum lines a body must span before inverting is suggested (default 3); does not apply to else_after_return"`
	Patterns     []string `json:"patterns,omitempty" jsonschema:"restrict to these patterns: guard_clause, loop_continue, else_after_return (default all)"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

type EarlyReturnViolationItem struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Function   string `json:"function_name"`
	Pattern    string `json:"pattern"`
	BodyLines  int    `json:"body_lines"`
	Fixable    bool   `json:"fixable"`
	Reason     string `json:"reason,omitempty"`
	Suggestion string `json:"suggestion"`
}

// EarlyReturnFixResult reports what the fix tool did. Fixed and Unfixable are
// derived from whether a finding actually carried a rewrite, so the counts
// describe the changes made rather than the violations seen.
type EarlyReturnFixResult struct {
	Applied       bool                       `json:"applied"`
	ChangesCount  int                        `json:"changes_count"`
	FilesModified []string                   `json:"files_modified,omitempty"`
	Fixed         []EarlyReturnViolationItem `json:"fixed"`
	Unfixable     []EarlyReturnViolationItem `json:"unfixable,omitempty"`
	Diffs         []FileDiff                 `json:"diffs,omitempty"`
	BlockingIssue string                     `json:"blocking_issue,omitempty"`
	Message       string                     `json:"message,omitempty"`
}

// --- detect_redundant_negations / fix_redundant_negations ---

type DetectRedundantNegationsInput struct {
	Package      string `json:"package,omitempty" jsonschema:"specific package to analyze"`
	MinReduction int    `json:"min_reduction,omitempty" jsonschema:"minimum number of ! operators a rewrite must remove (default 1)"`
}

type FixRedundantNegationsInput struct {
	Package      string `json:"package,omitempty" jsonschema:"specific package to fix"`
	MinReduction int    `json:"min_reduction,omitempty" jsonschema:"minimum number of ! operators a rewrite must remove (default 1)"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

type RedundantNegationViolationItem struct {
	File            string `json:"file"`
	Line            int    `json:"line"`
	Column          int    `json:"column"`
	Function        string `json:"function_name"`
	Before          string `json:"before"`
	After           string `json:"after"`
	NegationsBefore int    `json:"negations_before"`
	NegationsAfter  int    `json:"negations_after"`
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
	DryRun        bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
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
		Description: "Analyze a symbol: find its definition, properties, and all references across the workspace under every build configuration, so declarations and callers in files excluded by build constraints are found. When configurations declare it in more than one place, `declarations` lists each with the configurations that declare it; `build_configs` and `uncovered_files` appear as for the planning tools. Only package-level symbols are addressable; function-local constants, types and variables are not.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AnalyzeSymbolInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		report, err := state.GetEngine().AnalyzeSymbolAcrossConfigs(in.Symbol, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		symbol := report.Symbol
		info := SymbolInfo{
			Name:      symbol.Name,
			Kind:      symbol.Kind.String(),
			Package:   symbol.Package,
			File:      symbol.File,
			Line:      symbol.Line,
			Column:    symbol.Column,
			Exported:  symbol.Exported,
			Signature: symbol.Signature,
			RefCount:  len(report.References),
		}
		if len(report.Declarations) > 1 {
			for _, d := range report.Declarations {
				info.Declarations = append(info.Declarations, DeclarationInfo{File: d.File, Line: d.Line, Column: d.Column, Configs: d.Configs})
			}
		}
		for _, ref := range report.References {
			info.Refs = append(info.Refs, ReferenceInfo{File: ref.File, Line: ref.Line, Column: ref.Column})
		}
		if len(report.Configs) > 1 {
			info.BuildConfigs = report.Configs
		}
		if len(report.Unchecked) > 0 {
			info.UncoveredFiles = uncheckedFileInfos(report.Unchecked, ws.RootPath)
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
		Name: "unused",
		Description: "Find unused symbols in the workspace. A symbol is reported when no build configuration (GOOS/GOARCH variants, custom build tags) covering the workspace uses it. " +
			"References from _test.go files do not count for symbols declared in non-test files, and neither do references from code that is itself unused. " +
			"By default only unexported symbols are reported; include_exported adds exported ones, judged by use within the workspace only. " +
			"safe_to_delete is true only for an unexported symbol with no references outside its own declaration. When referenced_from is present, those tests or unused callers must go first and safe_delete refuses while they exist. Exported symbols are never marked safe, because use outside the workspace is not visible. " +
			"A symbol that a file no configuration type-checks mentions, or that is named unresolved in a file with a type error, is listed under possibly_used instead.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in UnusedInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the report exists: a watcher batch
		// takes the write lock and may drop a view being analysed.
		state.RLock()
		report, err := findUnused(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}

		out := map[string]any{
			"unused_symbols": unusedItems(report.Unused),
			"count":          len(report.Unused),
		}
		if len(report.PossiblyUsed) > 0 {
			out["possibly_used"] = unusedItems(report.PossiblyUsed)
		}
		if len(report.Configs) > 1 {
			out["build_configs"] = report.Configs
		}
		return textResult(out), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "reachable_references",
		Description: "Find the call sites and references of target functions or methods that run when a program runs: those in code reachable from main of entry_package, across the workspace packages it imports. " +
			"Each site lists the declarations from a root (main, init, package-level var, or a method called by code outside the workspace) to the enclosing one in path, and kind is call or reference (a func value that is not called). " +
			"Reachability is rapid type analysis: a function is reachable if it is called, taken as a value, or dispatched through an interface that a live conversion of its type satisfies; a method named by an interface outside the workspace (String, ServeHTTP, ...) counts as called when its type is converted; exported funcs are not roots. A call to an interface method that may dispatch to a target method is not counted as a site of that target. Dynamic dispatch through reflection, go:linkname and assembly is not modelled. " +
			"Packages whose code cannot be analysed (cgo, unresolved identifiers, test packages that do not type-check) are listed in bodiless_packages: calls inside them are missing from sites. An entry_package of that kind is an error. " +
			"Sites are merged across build configurations; configs names those that reach a site, omitted when all do. Test code counts only with include_tests.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ReachableReferencesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		ws, err := state.GetWorkspace()
		var report *refactor.ReachableReport
		if err == nil {
			report, err = state.GetEngine().ReachableAcrossConfigs(refactor.ReachableRequest{Entry: in.EntryPackage, Targets: in.Targets, IncludeTests: in.IncludeTests, Within: in.Within})
		}
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}

		items := make([]ReachableSiteItem, len(report.Sites))
		byPackage, byTarget := map[string]int{}, map[string]int{}
		for i, f := range report.Sites {
			items[i] = ReachableSiteItem{Package: f.Package, File: f.File, Line: f.Line, Enclosing: f.Enclosing, Target: f.Target, Kind: f.Kind, Configs: f.Configs, Path: f.Path}
			byPackage[f.Package]++
			byTarget[f.Target]++
		}
		out := map[string]any{
			"sites":   items,
			"count":   len(items),
			"summary": map[string]any{"by_package": byPackage, "by_target": byTarget},
		}
		if len(report.Configs) > 1 {
			out["build_configs"] = report.Configs
		}
		if len(report.Bodiless) > 0 {
			bodiless := make([]BodilessPackageItem, len(report.Bodiless))
			for i, b := range report.Bodiless {
				bodiless[i] = BodilessPackageItem{Package: b.Package, Reason: b.Reason, Configs: b.Configs}
			}
			out["bodiless_packages"] = bodiless
		}
		if len(report.Unchecked) > 0 {
			out["uncovered_files"] = uncheckedFileInfos(report.Unchecked, ws.RootPath)
		}
		return textResult(out), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "detect_variadics",
		Description: "Detect variadic functions, methods and methods of top-level named interfaces, and how the workspace calls each one, to find variadics added to avoid updating call sites: a parameter that every call leaves empty or fills with one value is a candidate for removal or a plain parameter; when zero_args equals call_sites and the body does not use it, change_signature remove_param removes it. " +
			"Each finding counts call_sites, split into zero_args, one_arg and many_args by the number of variadic arguments passed, and spread for calls passing a slice (xs...), plus value_uses for uses as a func value, whose calls cannot be counted. " +
			"Calls come from the whole workspace including test files and generated code, merged across build configurations; package limits only which declarations are listed. A call through an interface counts for the interface method and not for its implementations, which are listed separately. Calls from outside the workspace are not visible, so low counts on an exported function prove nothing about its external callers. " +
			"Declarations in generated files, func literals, named func types and anonymous or local interfaces are not listed. Files no configuration type-checks are listed in uncovered_files: calls in them are missing from the counts, and variadics they declare are missing from the list. Non-test files with a type error under some configuration are listed with their first error in type_error_files: calls inside an erroring expression may be missing from the counts. configs names the configurations declaring a finding, omitted when all do.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectVariadicsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}
		request := refactor.VariadicsRequest{}
		if in.Package != "" {
			// The joined path covers a directory only another configuration holds a package in.
			request.Packages = []string{in.Package, types.ResolvePackagePath(ws, in.Package), filepath.Join(ws.RootPath, in.Package)}
		}
		report, err := state.GetEngine().VariadicsAcrossConfigs(request)
		if err != nil {
			return errResult(err), nil, nil
		}

		items := make([]VariadicItem, len(report.Findings))
		for i, f := range report.Findings {
			items[i] = VariadicItem{
				Function:  f.Function,
				Signature: f.Signature,
				File:      relativeTo(ws.RootPath, f.File),
				Line:      f.Line,
				Column:    f.Column,
				Configs:   f.Configs,
				CallSites: f.CallSites,
				ZeroArgs:  f.ZeroArgs,
				OneArg:    f.OneArg,
				ManyArgs:  f.ManyArgs,
				Spread:    f.Spread,
				ValueUses: f.ValueUses,
			}
		}
		out := map[string]any{
			"variadics":   items,
			"total_count": len(items),
		}
		if len(report.Configs) > 1 {
			out["build_configs"] = report.Configs
		}
		if len(report.Unchecked) > 0 {
			out["uncovered_files"] = uncheckedFileInfos(report.Unchecked, ws.RootPath)
		}
		if len(report.TypeErrors) > 0 {
			typeErrors := make([]VariadicTypeErrorItem, len(report.TypeErrors))
			for i, e := range report.TypeErrors {
				typeErrors[i] = VariadicTypeErrorItem{File: relativeTo(ws.RootPath, e.File.Path), Error: e.Err.Error()}
			}
			out["type_error_files"] = typeErrors
		}
		return textResult(out), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_if_init_assignments",
		Description: "Detect if/switch/type-switch init assignments (both `:=` and `=`, e.g. `if x, err := f(); err != nil {}` or `if err = f(); err != nil {}`) that should be split into a separate assignment and check. Each result reports its kind, whether it's part of an else-if chain, and whether the site was skipped as unsafe to rewrite (with skip_reason); for-loop and range inits are never flagged.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectIfInitInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

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
					Kind:       v.Kind,
					IsElseIf:   v.IsElseIf,
					Skipped:    v.Skipped,
					SkipReason: v.SkipReason,
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
		Description: "Split if/switch/type-switch init assignments into a separate assignment and check statement. Else-if inits are de-sugared into a nested block, and a `:=` is downgraded to `=` when every declared name already exists in the target scope. Sites the analyzer judges unsafe to rewrite are left unmodified and reported in `skipped` rather than silently dropped; for loops and range statements are never touched.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixIfInitInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		rr, err := analyzers.Run(ws, ifinit.Analyzer, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		var skipped []IfInitSkippedItem
		if results, ok := rr.Result.([]*ifinit.Result); ok {
			for _, v := range results {
				if v.Skipped {
					skipped = append(skipped, IfInitSkippedItem{
						File:       v.File,
						Line:       v.Line,
						Function:   v.Function,
						Snippet:    v.Snippet,
						SkipReason: v.SkipReason,
					})
				}
			}
		}

		changes := analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics)
		if len(changes) == 0 {
			state.RUnlock()
			message := "No if-init assignment violations found"
			if len(skipped) > 0 {
				message = "All if-init assignment sites found were skipped as unsafe to rewrite; see skipped"
			}
			return textResult(map[string]any{
				"files_modified": []string{},
				"changes_count":  0,
				"skipped":        skipped,
				"skipped_count":  len(skipped),
				"message":        message,
			}), nil, nil
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Fix if-init assignments", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		out := map[string]any{
			"files_modified": result.ModifiedFiles,
			"changes_count":  result.ChangeCount,
			"skipped":        skipped,
			"skipped_count":  len(skipped),
		}
		return textResult(withDryRun(out, result)), nil, nil
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
		Name:        "detect_nil_receiver_checks",
		Description: "Detect methods that compare their own receiver to nil, anywhere in the body and including compound conditions. Generated files are skipped. first_statement marks checks in the condition of the method's first statement; receiver_type shows whether the receiver is a pointer or another nillable type such as a map.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectNilReceiverChecksInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		rr, err := analyzers.Run(ws, nilreceiver.Analyzer, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		results, _ := rr.Result.([]*nilreceiver.Result)
		return textResult(map[string]any{
			"violations":  results,
			"total_count": len(results),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_nil_nil_returns",
		Description: "Detect returns of nil for every result of a function whose last result is error, such as `return nil, nil`, which leave callers with neither a value nor an error; a sentinel error is usually clearer. Func literals are judged by their own signature (in_closure). result_types lists the non-error result types as written, so nil slices and maps, which often mean an empty result, can be filtered out. Bare returns of named results, `return f()` and generated files are not checked.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectNilNilReturnsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		rr, err := analyzers.Run(ws, nilnil.Analyzer, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		results, _ := rr.Result.([]*nilnil.Result)
		return textResult(map[string]any{
			"violations":  results,
			"total_count": len(results),
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
			for _, fix := range booleanbranch.CollectFixResults(results) {
				allBooleansRemoved = append(allBooleansRemoved, fix.BooleansRemoved...)
				totalCases += fix.SwitchCases
			}
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Fix boolean branching", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		out := map[string]any{
			"files_modified":   result.ModifiedFiles,
			"changes_count":    result.ChangeCount,
			"booleans_removed": allBooleansRemoved,
			"switch_cases":     totalCases,
		}
		return textResult(withDryRun(out, result)), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_deep_if_else_chains",
		Description: "Detect nested if-else chains that should use early returns (guard clauses). Reports nesting depth, happy path depth, error branch count, and complexity reduction estimate. `fixable` means fix_deep_if_else_chains would produce some rewrite for this chain, not that it flattens all the way to depth 0 — check `reason` for whether (and why) the rewrite would still leave some nesting behind.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectDeepIfElseInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		// InvertCondition only skips unsafe float comparison flips when it can
		// see operand types, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

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
					Fixable:                    v.Fixable,
					Reason:                     v.Reason,
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
		Description: "Flatten deep if/else-if/else chains into early returns. Drops the `else` after any branch that hands control away (return, continue, break, panic, ...), and inverts the chain's last condition into a guard clause when that pays off. Each rewrite is verified against go/types before being applied; one that would not type-check is left alone and reported in `unfixable` with the type error as the reason, and a chain flattened only partway is reported in `partial` with the reason flattening stopped there. Does not hoist an `if init; cond` header — run fix_if_init_assignments first on chains that declare variables in their if headers, then run this.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixDeepIfElseInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// InvertCondition only skips unsafe float comparison flips when it can
		// see operand types, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

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

		// Only chains that actually carried a rewrite count as fixed. The
		// detector fires on any chain past the depth threshold, but the
		// rewriter handles a narrower shape, so the two sets differ.
		//
		// results and rr.Diagnostics are produced in lockstep, one diagnostic
		// per result, in the same order, both within a single package
		// (makeRun reports then appends for every chain it visits) and across
		// packages (Run concatenates both slices per package in the same
		// order) — see deepifelse.walker.tryChain. Filtering both to their
		// Fixable/has-a-fix subsequence therefore keeps them aligned index for
		// index, which is what lets fixableResults[i] below be paired with
		// changes[i].
		var unfixable []DeepIfElseUnfixableItem
		var fixableResults []*deepifelse.Result
		if results, ok := rr.Result.([]*deepifelse.Result); ok {
			for _, r := range results {
				if !r.Fixable {
					unfixable = append(unfixable, DeepIfElseUnfixableItem{
						File:         r.File,
						Line:         r.Line,
						Function:     r.Function,
						NestingDepth: r.NestingDepth,
						Reason:       r.Reason,
					})
					continue
				}
				fixableResults = append(fixableResults, r)
			}
		}

		changes := analyzers.FillOldText(ws, analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics))
		if len(changes) == 0 {
			state.RUnlock()
			message := "No deep if-else chain violations found"
			if len(unfixable) > 0 {
				message = fmt.Sprintf("Found %d deep if-else chain(s), none of a shape this fixer can rewrite", len(unfixable))
			}
			return textResult(map[string]any{
				"files_modified":  []string{},
				"changes_count":   0,
				"functions_fixed": []string{},
				"early_returns":   0,
				"unfixable":       unfixable,
				"partial":         []DeepIfElseUnfixableItem{},
				"message":         message,
			}), nil, nil
		}

		if len(changes) != len(fixableResults) {
			// Should not happen (see the invariant above); refuse rather than
			// guess which change belongs to which finding.
			state.RUnlock()
			state.logger.Error("fix_deep_if_else_chains: diagnostics/results count mismatch, refusing to apply",
				"changes", len(changes), "fixable_results", len(fixableResults))
			return errResult(fmt.Errorf("internal error: could not correlate %d rewrite(s) with %d fixable finding(s)", len(changes), len(fixableResults))), nil, nil
		}

		// Every rewrite this fixer proposes is verified against go/types
		// before it is applied, since the rewriter dedents blocks into
		// enclosing scopes and the only other pre-apply check
		// (Validator.validateCompilation) merely parses the new text for
		// syntax. The importer must be the one that type-checked ws — see
		// VerifyPlanTypesWithImporter's doc comment — which is why
		// EnsureTypeCheckedPackage above and this share state.GetEngine().
		importer := state.GetEngine().Importer(ws)
		plan := analyzers.ChangesToPlan(changes)
		verification := refactor.VerifyPlanTypesWithImporter(ws, plan, importer)

		if verification.Skipped != "" {
			// TypeCheckResult.OK() says nothing about skipped packages, so an
			// unverified plan must not be treated as clean. Nothing is
			// applied; the caller is told why.
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified":  []string{},
				"changes_count":   0,
				"functions_fixed": []string{},
				"early_returns":   0,
				"unfixable":       unfixable,
				"partial":         []DeepIfElseUnfixableItem{},
				"message": fmt.Sprintf(
					"Found %d rewrite(s) but could not verify them against go/types, so nothing was applied: %s",
					len(changes), verification.Skipped),
			}), nil, nil
		}

		var appliedResults []*deepifelse.Result
		var appliedChanges []types.Change

		if verification.OK() {
			appliedResults = fixableResults
			appliedChanges = changes
		} else {
			// The plan as a whole would not type-check. Re-verify each
			// rewrite on its own so the ones that are actually safe still get
			// applied, instead of refusing the entire batch over one bad
			// chain.
			for i, c := range changes {
				r := fixableResults[i]
				single := refactor.VerifyPlanTypesWithImporter(ws, analyzers.ChangesToPlan([]types.Change{c}), importer)
				switch {
				case single.Skipped != "":
					unfixable = append(unfixable, DeepIfElseUnfixableItem{
						File: r.File, Line: r.Line, Function: r.Function, NestingDepth: r.NestingDepth,
						Reason: fmt.Sprintf("could not verify this rewrite against go/types: %s", single.Skipped),
					})
				case !single.OK():
					unfixable = append(unfixable, DeepIfElseUnfixableItem{
						File: r.File, Line: r.Line, Function: r.Function, NestingDepth: r.NestingDepth,
						Reason: fmt.Sprintf("applying this rewrite would not type-check: %s", strings.Join(single.NewErrors, "; ")),
					})
				default:
					appliedResults = append(appliedResults, r)
					appliedChanges = append(appliedChanges, c)
				}
			}
		}

		if len(appliedChanges) == 0 {
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified":  []string{},
				"changes_count":   0,
				"functions_fixed": []string{},
				"early_returns":   0,
				"unfixable":       unfixable,
				"partial":         []DeepIfElseUnfixableItem{},
				"message":         fmt.Sprintf("Found %d deep if-else chain(s), none of a shape this fixer could safely apply", len(unfixable)),
			}), nil, nil
		}

		// functions_fixed/early_returns/partial only ever describe what was
		// actually applied: a fixable finding dropped above for failing
		// verification is reported in unfixable, not counted here.
		var functionsFixed []string
		var partial []DeepIfElseUnfixableItem
		totalEarlyReturns := 0
		for _, r := range appliedResults {
			functionsFixed = append(functionsFixed, r.Function)
			totalEarlyReturns += r.EarlyReturns
			if r.Reason != "" {
				partial = append(partial, DeepIfElseUnfixableItem{
					File:         r.File,
					Line:         r.Line,
					Function:     r.Function,
					NestingDepth: r.NestingDepth,
					Reason:       r.Reason,
				})
			}
		}

		plan = analyzers.ChangesToPlan(appliedChanges)
		result, err := executePlanWithUnlock(state, plan, "Fix deep if-else chains", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}

		out := map[string]any{
			"files_modified":  result.ModifiedFiles,
			"changes_count":   result.ChangeCount,
			"functions_fixed": functionsFixed,
			"early_returns":   totalEarlyReturns,
			"unfixable":       unfixable,
			"partial":         partial,
		}
		return textResult(withDryRun(out, result)), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_switchable_if_chains",
		Description: "Detect if/else-if/else chains that map onto a switch statement, in three forms: tagged (`switch x { case a, b: }`, when every branch compares the same operand against values), type switch (`switch v := x.(type)`, when every branch is a `v, ok := x.(T)` assertion), and tagless (`switch { case cond: }`, the general form used when neither of the other two applies). A chain whose branches all hand control away and whose `if` headers declare nothing is left entirely to fix_deep_if_else_chains instead, since guard clauses are the better idiom there — this tool only reports the dispatch-shaped chains that one refuses. `fixable` means fix_switchable_if_chains would actually rewrite this chain; check `reason` for why a violation was left unconverted, including chains where a later `else if` header declares variables (reason names fix_if_init_assignments — run that first).",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectSwitchableIfChainsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		// The analyzer's stmtfacts.Terminates panic detection, its
		// constant-duplicate detection, and its type-identity comparisons all
		// need real type information to fire, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		minBranches := in.MinBranches
		if minBranches <= 0 {
			minBranches = 3
		}
		opts := []switchchain.Option{switchchain.WithMinBranches(minBranches)}
		if len(in.Kinds) > 0 {
			opts = append(opts, switchchain.WithKinds(in.Kinds...))
		}
		a := switchchain.NewAnalyzer(opts...)
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		var items []SwitchableIfChainViolationItem
		if results, ok := rr.Result.([]*switchchain.Result); ok {
			items = make([]SwitchableIfChainViolationItem, len(results))
			for i, v := range results {
				items[i] = SwitchableIfChainViolationItem{
					File:       v.File,
					Line:       v.Line,
					Column:     v.Column,
					Function:   v.Function,
					Kind:       v.Kind,
					Branches:   v.Branches,
					Tag:        v.Tag,
					Cases:      v.Cases,
					HasDefault: v.HasDefault,
					Fixable:    v.Fixable,
					Reason:     v.Reason,
				}
			}
		}
		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "fix_switchable_if_chains",
		Description: "Convert if/else-if/else chains into switch statements, in three forms — tagged, type switch, and tagless (see detect_switchable_if_chains for what each form requires). This is the deliberate complement of fix_deep_if_else_chains: a chain whose branches all hand control away and whose `if` headers declare nothing is left entirely to that tool, since guard clauses are the better idiom there; this tool converts the dispatch-shaped chains that one refuses. A chain that declares variables in a later `else if` header is refused with a reason naming fix_if_init_assignments — run that first and this second to convert roughly a third more chains. Every rewrite is verified against go/types before being applied; one that would not type-check is left alone and reported in `unfixable` with the type error as the reason.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixSwitchableIfChainsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// The analyzer's stmtfacts.Terminates panic detection, its
		// constant-duplicate detection, and its type-identity comparisons all
		// need real type information to fire, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		minBranches := in.MinBranches
		if minBranches <= 0 {
			minBranches = 3
		}
		opts := []switchchain.Option{switchchain.WithMinBranches(minBranches)}
		if len(in.Kinds) > 0 {
			opts = append(opts, switchchain.WithKinds(in.Kinds...))
		}
		a := switchchain.NewAnalyzer(opts...)
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// Only chains that actually carried a rewrite count as fixed. The
		// detector fires on any chain that maps onto one of the requested
		// switch forms, but the rewriter handles a narrower shape (a pure
		// tag, no declarations in a later else-if header, ...), so the two
		// sets differ.
		//
		// results and rr.Diagnostics are produced in lockstep, one diagnostic
		// per result, in the same order, both within a single package and
		// across packages (Run concatenates both slices per package in the
		// same order) — the analyzer reports a diagnostic and appends a
		// Result together, one per chain, and attaches a SuggestedFix only
		// when Fixable. Filtering both to their Fixable subsequence therefore
		// keeps them aligned index for index, which is what lets
		// fixableResults[i] below be paired with changes[i].
		var unfixable []SwitchableIfChainUnfixableItem
		var fixableResults []*switchchain.Result
		if results, ok := rr.Result.([]*switchchain.Result); ok {
			for _, r := range results {
				if !r.Fixable {
					unfixable = append(unfixable, SwitchableIfChainUnfixableItem{
						File:     r.File,
						Line:     r.Line,
						Function: r.Function,
						Kind:     r.Kind,
						Branches: r.Branches,
						Reason:   r.Reason,
					})
					continue
				}
				fixableResults = append(fixableResults, r)
			}
		}

		changes := analyzers.FillOldText(ws, analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics))
		if len(changes) == 0 {
			state.RUnlock()
			message := "No switchable if-else chains found"
			if len(unfixable) > 0 {
				message = fmt.Sprintf("Found %d if-else chain(s) that map onto a switch, none of a shape this fixer can rewrite", len(unfixable))
			}
			return textResult(map[string]any{
				"files_modified":   []string{},
				"changes_count":    0,
				"functions_fixed":  []string{},
				"chains_converted": 0,
				"unfixable":        unfixable,
				"message":          message,
			}), nil, nil
		}

		if len(changes) != len(fixableResults) {
			// Should not happen (see the invariant above); refuse rather than
			// guess which change belongs to which finding.
			state.RUnlock()
			state.logger.Error("fix_switchable_if_chains: diagnostics/results count mismatch, refusing to apply",
				"changes", len(changes), "fixable_results", len(fixableResults))
			return errResult(fmt.Errorf("internal error: could not correlate %d rewrite(s) with %d fixable finding(s)", len(changes), len(fixableResults))), nil, nil
		}

		// Every rewrite this fixer proposes is verified against go/types
		// before it is applied, since duplicate case values or a tag whose
		// evaluation order or side effects differ from the chain's are the
		// kind of mistake only the type checker (not
		// Validator.validateCompilation, which merely parses the new text for
		// syntax) will catch. The importer must be the one that type-checked
		// ws — see VerifyPlanTypesWithImporter's doc comment — which is why
		// EnsureTypeCheckedPackage above and this share state.GetEngine().
		importer := state.GetEngine().Importer(ws)
		plan := analyzers.ChangesToPlan(changes)
		verification := refactor.VerifyPlanTypesWithImporter(ws, plan, importer)

		if verification.Skipped != "" {
			// TypeCheckResult.OK() says nothing about skipped packages, so an
			// unverified plan must not be treated as clean. Nothing is
			// applied; the caller is told why.
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified":   []string{},
				"changes_count":    0,
				"functions_fixed":  []string{},
				"chains_converted": 0,
				"unfixable":        unfixable,
				"message": fmt.Sprintf(
					"Found %d rewrite(s) but could not verify them against go/types, so nothing was applied: %s",
					len(changes), verification.Skipped),
			}), nil, nil
		}

		var appliedResults []*switchchain.Result
		var appliedChanges []types.Change

		if verification.OK() {
			appliedResults = fixableResults
			appliedChanges = changes
		} else {
			// The plan as a whole would not type-check. Re-verify each
			// rewrite on its own so the ones that are actually safe still get
			// applied, instead of refusing the entire batch over one bad
			// chain (e.g. one with duplicate case values that only the type
			// checker can see).
			for i, c := range changes {
				r := fixableResults[i]
				single := refactor.VerifyPlanTypesWithImporter(ws, analyzers.ChangesToPlan([]types.Change{c}), importer)
				switch {
				case single.Skipped != "":
					unfixable = append(unfixable, SwitchableIfChainUnfixableItem{
						File: r.File, Line: r.Line, Function: r.Function, Kind: r.Kind, Branches: r.Branches,
						Reason: fmt.Sprintf("could not verify this rewrite against go/types: %s", single.Skipped),
					})
				case !single.OK():
					unfixable = append(unfixable, SwitchableIfChainUnfixableItem{
						File: r.File, Line: r.Line, Function: r.Function, Kind: r.Kind, Branches: r.Branches,
						Reason: fmt.Sprintf("applying this rewrite would not type-check: %s", strings.Join(single.NewErrors, "; ")),
					})
				default:
					appliedResults = append(appliedResults, r)
					appliedChanges = append(appliedChanges, c)
				}
			}
		}

		if len(appliedChanges) == 0 {
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified":   []string{},
				"changes_count":    0,
				"functions_fixed":  []string{},
				"chains_converted": 0,
				"unfixable":        unfixable,
				"message":          fmt.Sprintf("Found %d switchable if-else chain(s), none of a shape this fixer could safely apply", len(unfixable)),
			}), nil, nil
		}

		// functions_fixed/chains_converted only ever describe what was
		// actually applied: a fixable finding dropped above for failing
		// verification is reported in unfixable, not counted here.
		var functionsFixed []string
		for _, r := range appliedResults {
			functionsFixed = append(functionsFixed, r.Function)
		}

		plan = analyzers.ChangesToPlan(appliedChanges)
		result, err := executePlanWithUnlock(state, plan, "Convert if-else chains to switch statements", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}

		out := map[string]any{
			"files_modified":   result.ModifiedFiles,
			"changes_count":    result.ChangeCount,
			"functions_fixed":  functionsFixed,
			"chains_converted": len(appliedResults),
			"unfixable":        unfixable,
		}
		return textResult(withDryRun(out, result)), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_early_returns",
		Description: "Detect conditionals that keep the happy path indented and could be inverted into early-return guard clauses. Covers `if cond { body }` at the end of a function (guard_clause) or of a loop body (loop_continue), and `if cond { ...; return } else { body }` (else_after_return). Reports which findings can be rewritten safely and why the rest cannot.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectEarlyReturnsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		// InvertCondition only skips unsafe float comparison flips when it can
		// see operand types, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		a, err := earlyReturnAnalyzer(in.MinBodyLines, in.Patterns)
		if err != nil {
			return errResult(err), nil, nil
		}

		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		items := earlyReturnItems(rr.Result)
		fixable := 0
		for _, item := range items {
			if item.Fixable {
				fixable++
			}
		}

		return textResult(map[string]any{
			"violations":    items,
			"total_count":   len(items),
			"fixable_count": fixable,
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "fix_early_returns",
		Description: "Invert conditionals into early-return guard clauses so the happy path is no longer nested. Skips any rewrite that would redeclare a name in the scope it dedents into, or that would require inventing return values.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixEarlyReturnsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// InvertCondition only skips unsafe float comparison flips when it can
		// see operand types, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		a, err := earlyReturnAnalyzer(in.MinBodyLines, in.Patterns)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		var fixed, unfixable []EarlyReturnViolationItem
		for _, item := range earlyReturnItems(rr.Result) {
			if item.Fixable {
				fixed = append(fixed, item)
				continue
			}
			unfixable = append(unfixable, item)
		}

		changes := analyzers.FillOldText(ws, analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics))
		if len(changes) == 0 {
			state.RUnlock()
			return textResult(&EarlyReturnFixResult{
				Applied:   !in.DryRun,
				Fixed:     []EarlyReturnViolationItem{},
				Unfixable: unfixable,
				Message:   "No rewritable early-return opportunities found",
			}), nil, nil
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Invert conditionals into early returns", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}

		return textResult(&EarlyReturnFixResult{
			Applied:       !in.DryRun,
			ChangesCount:  result.ChangeCount,
			FilesModified: result.ModifiedFiles,
			Fixed:         fixed,
			Unfixable:     unfixable,
			Diffs:         result.Diffs,
			BlockingIssue: result.BlockingIssue,
			Message:       result.Description,
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "detect_redundant_negations",
		Description: "Detect negated boolean expressions that De Morgan's laws would simplify, such as `!(!a && !b)`. Only reports rewrites that strictly reduce the number of `!` operators; `!(a && b)` is left alone. Useful as a cleanup pass after any refactoring that inverts conditions.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in DetectRedundantNegationsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}

		// The float/NaN guard in pushNeg needs operand types to skip unsafe
		// ordering-operator flips, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		a := redundantNegationsAnalyzer(in.MinReduction)
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			return errResult(err), nil, nil
		}

		items := redundantNegationItems(rr.Result)

		return textResult(map[string]any{
			"violations":  items,
			"total_count": len(items),
		}), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "fix_redundant_negations",
		Description: "Simplify negated boolean expressions via De Morgan's laws where doing so strictly reduces the number of `!` operators. Safe to run after any other fixer as a cleanup pass, and safe to run repeatedly.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in FixRedundantNegationsInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// The float/NaN guard in pushNeg needs operand types to skip unsafe
		// ordering-operator flips, and type-checking is lazy.
		state.GetEngine().EnsureTypeCheckedPackage(ws, in.Package)

		a := redundantNegationsAnalyzer(in.MinReduction)
		rr, err := analyzers.Run(ws, a, in.Package)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		items := redundantNegationItems(rr.Result)

		changes := analyzers.FillOldText(ws, analyzers.DiagnosticsToChanges(ws.FileSet, rr.Diagnostics))
		if len(changes) == 0 {
			state.RUnlock()
			return textResult(map[string]any{
				"files_modified": []string{},
				"changes_count":  0,
				"fixed":          []RedundantNegationViolationItem{},
				"message":        "No redundant negations found",
			}), nil, nil
		}

		plan := analyzers.ChangesToPlan(changes)
		result, err := executePlanWithUnlock(state, plan, "Simplify redundant negations", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}

		out := map[string]any{
			"files_modified": result.ModifiedFiles,
			"changes_count":  result.ChangeCount,
			"fixed":          items,
		}
		return textResult(withDryRun(out, result)), nil, nil
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
		result, err := executePlanWithUnlock(state, plan, "Fix error wrapping", in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		out := map[string]any{
			"files_modified":     result.ModifiedFiles,
			"changes_count":      result.ChangeCount,
			"errors_wrapped":     errorsWrapped,
			"format_verbs_fixed": formatVerbsFixed,
			"contexts_added":     contextsAdded,
		}
		return textResult(withDryRun(out, result)), nil, nil
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
		Description: "Analyze the dependency graph of the workspace without changing it. Covers every build configuration: imports, dependencies and symbol counts are those of all files, and build_configs lists the configurations when there is more than one. Returns package_coupling (import and symbol counts per package) and dependency_graph: PackageImports (package directory to the import paths of its non-test files), PackageDeps (package directory to the directories of the workspace packages it depends on, transitively, through non-test files), and ImportCycles and TestImportCycles (each {packages: closed path of package directories, configs: the configurations it occurs in, omitted when all}; cycles are found under each configuration separately, so edges from different configurations never combine). detect_backwards adds backwards_dependencies, imports from an inner layer (domain, core, model) to an outer one (handler, infra, repository), classified by package name. suggest_moves adds suggested_moves, exported symbols referenced from two or more other packages, with the referencing packages, computed in the default build configuration only (as move_by_dependencies picks its candidates).",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AnalyzeDependenciesInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()
		defer state.RUnlock()

		ws, err := state.GetWorkspace()
		if err != nil {
			return errResult(err), nil, nil
		}
		report, err := state.GetEngine().AnalyzeDependenciesAcrossConfigs(types.AnalyzeDependenciesRequest{
			Workspace:           ws.RootPath,
			DetectBackwardsDeps: in.DetectBackwards,
			SuggestMoves:        in.SuggestMoves,
		})
		if err != nil {
			return errResult(err), nil, nil
		}
		plan := report.Plan
		impact := *plan.Impact
		result := map[string]any{
			"affected_files":   plan.AffectedFiles,
			"change_count":     len(plan.Changes),
			"package_coupling": impact.PackageCoupling,
			"dependency_graph": report.Graph,
		}
		if len(report.Configs) > 1 {
			result["build_configs"] = report.Configs
		}
		if in.SuggestMoves {
			result["suggested_moves"] = impact.SuggestedMoves
		}
		if in.DetectBackwards {
			result["backwards_dependencies"] = impact.BackwardsDependencies
		}
		impact.PackageCoupling, impact.SuggestedMoves, impact.BackwardsDependencies = nil, nil, nil
		result["impact"] = impact
		return textResult(result), nil, nil
	})
}

// earlyReturnAnalyzer builds a configured earlyreturn analyzer, rejecting
// unknown pattern names rather than silently analysing more than was asked for.
func earlyReturnAnalyzer(minBodyLines int, patternNames []string) (*goanalysis.Analyzer, error) {
	var opts []earlyreturn.Option
	if minBodyLines > 0 {
		opts = append(opts, earlyreturn.WithMinBodyLines(minBodyLines))
	}

	if len(patternNames) > 0 {
		patterns := make([]earlyreturn.Pattern, 0, len(patternNames))
		for _, name := range patternNames {
			p, ok := earlyreturn.ParsePattern(name)
			if !ok {
				return nil, fmt.Errorf("unknown pattern %q: want one of guard_clause, loop_continue, else_after_return", name)
			}
			patterns = append(patterns, p)
		}
		opts = append(opts, earlyreturn.WithPatterns(patterns...))
	}

	return earlyreturn.NewAnalyzer(opts...), nil
}

// earlyReturnItems converts analyzer results into their MCP representation.
func earlyReturnItems(result any) []EarlyReturnViolationItem {
	results, ok := result.([]*earlyreturn.Result)
	if !ok {
		return nil
	}

	items := make([]EarlyReturnViolationItem, len(results))
	for i, v := range results {
		items[i] = EarlyReturnViolationItem{
			File:       v.File,
			Line:       v.Line,
			Column:     v.Column,
			Function:   v.Function,
			Pattern:    string(v.Pattern),
			BodyLines:  v.BodyLines,
			Fixable:    v.Fixable,
			Reason:     v.Reason,
			Suggestion: v.Suggestion,
		}
	}
	return items
}

// redundantNegationsAnalyzer builds a configured boolsimplify analyzer.
// minReduction <= 0 keeps the analyzer's own default (1).
func redundantNegationsAnalyzer(minReduction int) *goanalysis.Analyzer {
	var opts []boolsimplify.Option
	if minReduction > 0 {
		opts = append(opts, boolsimplify.WithMinReduction(minReduction))
	}
	return boolsimplify.NewAnalyzer(opts...)
}

// redundantNegationItems converts analyzer results into their MCP
// representation. Every boolsimplify.Result already carries a rewrite - the
// analyzer only reports a site once it has found one - so there is no
// separate fixable/unfixable split to make here, unlike earlyReturnItems.
func redundantNegationItems(result any) []RedundantNegationViolationItem {
	results, ok := result.([]*boolsimplify.Result)
	if !ok {
		return nil
	}

	items := make([]RedundantNegationViolationItem, len(results))
	for i, v := range results {
		items[i] = RedundantNegationViolationItem{
			File:            v.File,
			Line:            v.Line,
			Column:          v.Column,
			Function:        v.Function,
			Before:          v.Before,
			After:           v.After,
			NegationsBefore: v.NegationsBefore,
			NegationsAfter:  v.NegationsAfter,
		}
	}
	return items
}
