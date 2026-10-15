package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// --- add_context_parameter ---

type AddContextParameterInput struct {
	FunctionName string   `json:"function_name" jsonschema:"function or method name (use Type.Method for methods)"`
	SourceFile   string   `json:"source_file" jsonschema:"file containing the function"`
	DefaultValue string   `json:"default_value,omitempty" jsonschema:"value at call sites, defaults to context.TODO()"`
	Propagate    bool     `json:"propagate,omitempty" jsonschema:"propagate changes to interface declarations and sibling implementations"`
	Thread       bool     `json:"thread,omitempty" jsonschema:"thread ctx transitively through all callers up to entrypoints, instead of injecting context.TODO() at each call site"`
	StopAt       []string `json:"stop_at,omitempty" jsonschema:"functions where the thread climb stops (requires thread): Name, Type.Method or path/file.go:Type.Method; a stop keeps its signature and passes its in-scope ctx or context.TODO()"`
	EntryPackage string   `json:"entry_package,omitempty" jsonschema:"a package main (import path, directory or workspace-relative path; requires thread): the climb stops at every production caller its program does not reach, which keeps its signature and passes its in-scope ctx or context.TODO(); reported in skipped_sites as outside entry"`
	DryRun       bool     `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

func registerContextTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "add_context_parameter",
		Description: `Add ctx context.Context as the first parameter to a function, updating all call sites with a default value (context.TODO() by default).
Pairs with detect_missing_context_params to find functions that need it.

Set thread: true to thread a real context.Context transitively through every call chain in the workspace that reaches the function, instead of injecting context.TODO() at each of its direct call sites. Climbing stops at the first caller that already has a context.Context in scope — a parameter, a captured variable, or a derivable r.Context() on an *http.Request — and at any root that cannot take one: main, init, any function or method with a named *testing.T/B/F or testing.TB parameter (tests and test helpers alike), closures, and package-level var initializers. Roots get context.TODO() (or <param>.Context() for a testing parameter), never a new parameter. t.Context() is cancelled before t.Cleanup functions run, so a testing-parameter helper called directly from a Cleanup closure is climbed like an ordinary function and the closure passes context.TODO(); a helper reached only transitively from a Cleanup closure still passes t.Context(), so review such call sites. When the seed or something it reaches is a method on a type that implements a workspace-declared interface, the interface declaration and every workspace implementation of it are rewritten together, so the result compiles regardless of which implementation a caller holds. Threading aborts, making no changes at all, if it would need to add a context.Context to a method satisfying an interface declared outside the workspace (e.g. http.Handler, fmt.Stringer), or if the function is ever referenced as a value rather than called (assigned to a variable, passed as a callback, taken as a method value) — both cases would otherwise leave the workspace uncompilable. Threading is planned under every build configuration that covers the workspace (GOOS/GOARCH variants, custom build tags) and merged into one plan, so files only another configuration compiles are rewritten too and a function that gains ctx in one configuration gains it in all; the result lists those configurations as build_configs when there is more than one, and an abort names the configuration it came from. A file in a package the operation reached that no configuration type-checks (a _test.go file with a type error such as an unresolvable import, or a file no configuration can include) is listed in uncovered_files with the configurations that include it and the reason; it is not rewritten, and when it names a function that gained ctx it also appears in skipped_sites to be fixed by hand. Without thread the same holds: the function, with propagate its interface declarations and implementations, and the direct call sites are rewritten under every configuration, a declaration that gains ctx in one configuration gains it in all, and a missing function is an error only when no configuration declares it. The operation aborts when a reached package had all its test files dropped from type-checking without a per-file cause. With thread, stop_at lists functions (Name, Type.Method or path/file.go:Type.Method) where the climb stops: a stop keeps its signature and passes its in-scope ctx or context.TODO() to the callee, and an unknown or ambiguous entry is an error. Each stop is reported in skipped_sites with a reason starting stop_at: — that marks where the climb stopped, the place for a hand-written ...WithContext variant, and is not a build break. With entry_package (a package main), the climb also stops at every production caller that the program started by that main does not reach, judged by rapid type analysis as in reachable_references and merged across build configurations: such a caller keeps its signature, passes its in-scope ctx or context.TODO(), and is reported in skipped_sites with a reason starting outside entry:. Functions in _test.go files are climbed as usual, interface implementations of a reached method still change so the code compiles, and generated code is refused only when the scoped climb reaches it. An entry that cannot be analysed under any build configuration is an error. A caller that already takes a blank (_) or unnamed context.Context is not given a second one: the parameter is named ctx and used, unless ctx is already declared in that function, which aborts and names the conflict. thread and default_value are mutually exclusive: with thread, each call site resolves its own context expression, so a single fixed default_value has no meaning.`,
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AddContextParameterInput) (*mcpsdk.CallToolResult, any, error) {
		if in.Thread && in.DefaultValue != "" {
			return errResult(fmt.Errorf(
				"default_value is not meaningful with thread: true; each call site resolves its own " +
					"context expression, so a single fixed default cannot apply to all of them")), nil, nil
		}

		if in.EntryPackage != "" && !in.Thread {
			return errResult(fmt.Errorf(
				"entry_package is only meaningful with thread: true; without it no caller is climbed")), nil, nil
		}

		if len(in.StopAt) > 0 && !in.Thread {
			return errResult(fmt.Errorf(
				"stop_at is only meaningful with thread: true; without it no caller is climbed")), nil, nil
		}

		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()
		plan, desc, err := planAddContextParameter(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}

		result, err := executePlan(state, plan, desc, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}

// planAddContextParameter plans in under every build configuration, threading
// through callers when in.Thread is set. The caller holds the read lock.
func planAddContextParameter(state *MCPServer, in AddContextParameterInput) (*pkgtypes.RefactoringPlan, string, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, "", err
	}
	sourceFile := resolveFile(ws, in.SourceFile)
	if in.Thread {
		plan, err := state.GetEngine().ThreadContextAcrossConfigs(pkgtypes.ThreadContextRequest{
			FunctionName: in.FunctionName,
			SourceFile:   sourceFile,
			StopAt:       in.StopAt,
			EntryPackage: in.EntryPackage,
		})
		return plan, fmt.Sprintf("thread context through: %s", in.FunctionName), err
	}
	plan, err := state.GetEngine().AddContextParameterAcrossConfigs(pkgtypes.AddContextParameterRequest{
		FunctionName: in.FunctionName,
		SourceFile:   sourceFile,
		DefaultValue: in.DefaultValue,
		Propagate:    in.Propagate,
	})
	return plan, fmt.Sprintf("add context parameter: %s", in.FunctionName), err
}
