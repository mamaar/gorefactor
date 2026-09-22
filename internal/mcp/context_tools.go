package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// packageForFile returns the workspace package that owns sourceFile (matched
// the same way analysis.ExtractFuncParams matches it: by map key or by
// File.Path), or nil if no package claims it. Mirrors the lookup pattern used
// throughout pkg/analysis/funcparams.go, kept local here since only this
// file's TestTypesExcluded guard needs the owning *pkgtypes.Package rather
// than just the *pkgtypes.File.
func packageForFile(ws *pkgtypes.Workspace, sourceFile string) *pkgtypes.Package {
	for _, pkg := range ws.Packages {
		if _, ok := pkg.Files[sourceFile]; ok {
			return pkg
		}
		for filePath, f := range pkg.Files {
			if filePath == sourceFile || f.Path == sourceFile {
				return pkg
			}
		}
	}
	return nil
}

// --- add_context_parameter ---

type AddContextParameterInput struct {
	FunctionName string `json:"function_name" jsonschema:"function or method name (use Type.Method for methods)"`
	SourceFile   string `json:"source_file" jsonschema:"file containing the function"`
	DefaultValue string `json:"default_value,omitempty" jsonschema:"value at call sites, defaults to context.TODO()"`
	Propagate    bool   `json:"propagate,omitempty" jsonschema:"propagate changes to interface declarations and sibling implementations"`
	Thread       bool   `json:"thread,omitempty" jsonschema:"thread ctx transitively through all callers up to entrypoints, instead of injecting context.TODO() at each call site"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

func registerContextTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "add_context_parameter",
		Description: `Add ctx context.Context as the first parameter to a function, updating all call sites with a default value (context.TODO() by default).
Pairs with detect_missing_context_params to find functions that need it.

Set thread: true to thread a real context.Context transitively through every call chain in the workspace that reaches the function, instead of injecting context.TODO() at each of its direct call sites. Climbing stops at the first caller that already has a context.Context in scope — a parameter, a captured variable, or a derivable r.Context() on an *http.Request — and at any root that cannot take one: main, init, Test/Benchmark/Fuzz declarations, closures, and package-level var initializers. Roots get context.TODO() (or t.Context() inside a *testing.T/B/F-scoped function or subtest), never a new parameter. When the seed or something it reaches is a method on a type that implements a workspace-declared interface, the interface declaration and every workspace implementation of it are rewritten together, so the result compiles regardless of which implementation a caller holds. Threading aborts, making no changes at all, if it would need to add a context.Context to a method satisfying an interface declared outside the workspace (e.g. http.Handler, fmt.Stringer), or if the function is ever referenced as a value rather than called (assigned to a variable, passed as a callback, taken as a method value) — both cases would otherwise leave the workspace uncompilable. thread and default_value are mutually exclusive: with thread, each call site resolves its own context expression, so a single fixed default_value has no meaning.`,
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AddContextParameterInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		if in.Thread {
			if in.DefaultValue != "" {
				state.RUnlock()
				return errResult(fmt.Errorf(
					"default_value is not meaningful with thread: true; each call site resolves its own " +
						"context expression, so a single fixed default cannot apply to all of them")), nil, nil
			}

			sourceFile := resolveFile(ws, in.SourceFile)

			// Deliberately does NOT call state.EnsureReferenceIndex / set
			// CachedIndex, unlike the non-threading path below. That cache may
			// have been built from a workspace that was loaded but never
			// type-checked (BuildReferenceIndex silently skips any package
			// whose TypesPkg/TypesInfo is nil), which would hand the operation
			// an index with an empty object-identity map — CallSitesOf would
			// find no callers and threading would "succeed" having changed
			// nothing. Leaving the request's index unset makes
			// DefaultEngine.ThreadContext type-check every package itself and
			// then build a fresh, accurate index.
			plan, err := state.GetEngine().ThreadContext(ws, pkgtypes.ThreadContextRequest{
				FunctionName: in.FunctionName,
				SourceFile:   sourceFile,
			})
			if err != nil {
				state.RUnlock()
				return errResult(err), nil, nil
			}

			state.RUnlock()

			result, err := executePlan(state, plan, fmt.Sprintf("thread context through: %s", in.FunctionName), in.DryRun)
			if err != nil {
				return errResult(err), nil, nil
			}
			return textResult(result), nil, nil
		}

		idx, err := state.EnsureReferenceIndex(ws)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		sourceFile := resolveFile(ws, in.SourceFile)

		// Find the function declaration and extract existing params
		funcParams, err := analysis.ExtractFuncParams(ws, sourceFile, in.FunctionName)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// A signature change is only safe if every call site gets rewritten —
		// a caller left with the old arity does not compile. idx (built above)
		// resolves call sites by go/types object identity, which is blind to
		// any _test.go file whose package had its test files excluded from
		// type-checking (pkgtypes.Package.TestTypesExcluded — see
		// (*analysis.GoParser).TypeCheckPackage's "retrying without" fallback).
		// The seed's own package is where that risk is concentrated: an
		// in-package test file calling the function directly is the single
		// most common call-site shape this tool handles, and it is exactly
		// what silently went unrewritten before this check existed.
		if pkg := packageForFile(ws, sourceFile); pkg != nil && pkg.TestTypesExcluded && len(pkg.TestFiles) > 0 {
			state.RUnlock()
			return errResult(fmt.Errorf(
				"cannot add context parameter: package %s has %d test file(s) that could not be "+
					"type-checked together with the package (cause: %v), so call sites in them are "+
					"invisible to this operation; proceeding would silently leave those test files "+
					"uncompilable once the signature changes — fix the underlying type-check failure "+
					"(often an unresolvable import in one _test.go file, which currently costs the whole "+
					"package's tests their type info) and re-run",
				pkg.ImportPath, len(pkg.TestFiles), pkg.TestTypesExcludedErr)), nil, nil
		}

		// Check if ctx context.Context already exists as first param
		if len(funcParams) > 0 && funcParams[0].Type == "context.Context" {
			state.RUnlock()
			return errResult(fmt.Errorf("function %s already has context.Context as first parameter", in.FunctionName)), nil, nil
		}

		// Convert to refactor.Parameter and prepend ctx context.Context
		newParams := make([]refactor.Parameter, 0, len(funcParams)+1)
		newParams = append(newParams, refactor.Parameter{Name: "ctx", Type: "context.Context"})
		for _, fp := range funcParams {
			newParams = append(newParams, refactor.Parameter{Name: fp.Name, Type: fp.Type})
		}

		defaultValue := in.DefaultValue
		if defaultValue == "" {
			defaultValue = "context.TODO()"
		}

		op := &refactor.ChangeSignatureOperation{
			FunctionName:         in.FunctionName,
			SourceFile:           sourceFile,
			NewParams:            newParams,
			Scope:                pkgtypes.WorkspaceScope,
			PropagateToInterface: in.Propagate,
			DefaultValue:         defaultValue,
			NewParamPosition:     0,
			NewReturnPosition:    -1,
			RemovedReturnIndex:   -1,
			CachedIndex:          idx,
			Logger:               state.logger,
		}

		if err := op.Validate(ws); err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := op.Execute(ws)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		// Bug 2 fix: remove internal "ctx := context.Background()" / "ctx := context.TODO()"
		// statements from the function body.  Now that ctx is a parameter, the internal
		// root-context creation is a redeclaration that prevents compilation.
		internalCtxChanges := refactor.RemoveInternalCtxCreation(ws, sourceFile, in.FunctionName)
		plan.Changes = append(plan.Changes, internalCtxChanges...)
		for _, c := range internalCtxChanges {
			found := false
			for _, f := range plan.AffectedFiles {
				if f == c.File {
					found = true
					break
				}
			}
			if !found {
				plan.AffectedFiles = append(plan.AffectedFiles, c.File)
			}
		}

		state.RUnlock()

		result, err := executePlan(state, plan, fmt.Sprintf("add context parameter: %s", in.FunctionName), in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
