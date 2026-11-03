package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// --- change_signature ---

type ParamSpec struct {
	Name string `json:"name" jsonschema:"parameter name"`
	Type string `json:"type" jsonschema:"parameter Go type"`
}

type ChangeSignatureInput struct {
	FunctionName string      `json:"function_name" jsonschema:"function or method name (use Type.Method for methods)"`
	SourceFile   string      `json:"source_file" jsonschema:"file containing the function"`
	Subcommand   string      `json:"subcommand" jsonschema:"operation: add_param, remove_param, add_return, remove_return, or deref_param"`
	Params       []ParamSpec `json:"params,omitempty" jsonschema:"full new parameter list (for add_param/remove_param/deref_param)"`
	Returns      []string    `json:"returns,omitempty" jsonschema:"full new return type list (for add_return/remove_return)"`
	DefaultValue string      `json:"default_value,omitempty" jsonschema:"default value for new parameter at call sites"`
	Position     int         `json:"position,omitempty" jsonschema:"position index of the new parameter (for add_param)"`
	Propagate    bool        `json:"propagate,omitempty" jsonschema:"propagate changes to interface declarations and sibling implementations"`
	DryRun       bool        `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

func registerChangeSignatureTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "change_signature",
		Description: `Change a function or method signature. Supports five subcommands:
- add_param: add a new parameter (provide params list with the new param included, plus default_value and position)
- remove_param: remove a parameter (provide params list without the removed param). The argument for that parameter is dropped at every call. Refused, listing every site and leaving files unchanged: identical unnamed parameters (ambiguous), a body that still uses the parameter (also in implementations of an interface method), a reference that is not a call (a func value), a call that cannot be lined up with the parameters (f(g()) with a multi-value g, a method expression), a dropped argument that contains a call or a channel receive (type conversions are fine), and, for a variadic parameter, a call passing any variadic argument or xs.... A trailing variadic that no call fills can therefore be removed this way. An import that only the removed parameter's type or a dropped argument used is removed too, in every file the change edits; dot and blank imports are never removed.
- add_return: add a new return value (provide returns list with the new type included). Return statements of the target get the zero value, 'x := f()' becomes 'x, _ := f()' and a discarded 'f()' becomes '_, _ = f()'; go and defer statements are left alone.
- remove_return: remove the last return value (provide returns list without the removed type; empty to remove the only one). The result is dropped from 'x, err := f()' and from the target's return statements.
For add_return and remove_return the target, and with propagate its interface methods and implementations, are rewritten by object identity: a same-named function or method elsewhere is left alone. The change is refused, listing every site and leaving files unchanged, for: a call inside 'return f()', an argument or another expression or declaration, a use as a func value, a 'return g()' that forwards a multi-value call or a bare return of named results in the target, a returned value being dropped that contains a call or a receive, and, for remove_return, a variable holding the removed result that is read later. A removed error result is the exception: when the variable is bound by this call and never assigned again, its nil checks in if conditions ('err != nil', 'err == nil', alone or inside || and &&) are folded as if it were nil, so 'if err != nil { return err }' goes, 'else' and 'err == nil' branches stay, and an init statement keeps its call. Locals or imports left unused by the dropped branch are cleaned up (imports) or refused (locals); any other read, such as errors.Is(err, X), is refused.
- deref_param: change one parameter from *T to T (provide the full params list with that parameter's type as T). Every use in the body must be a plain read *p, which becomes p; any other use refuses the change. Nil checks are folded: 'p == nil || *p == nil' and 'p == nil || len(*p) == 0' (slice/map T, and the && mirrors) become value checks and accept literal nil arguments; a lone 'p == nil' folds to false/true and needs every call site to pass &x, otherwise (or if the fold is not a removable if) the whole change is refused. Call sites pass x for &x and *e for any other argument; references that are not calls, and calls that cannot be rewritten, are listed in skipped_sites. Not supported for interface methods or with propagate.
Generic functions and methods on generic types are refused.
Interfaces and implementations are found by go/types, so a same-named method with another signature is left alone. A method that satisfies an interface is refused unless propagate is set, which also changes the interface and every implementation (pointer receivers and types declared under other build configurations included). A method that satisfies an interface declared outside the workspace (io.Writer, http.Handler) is always refused, because the type would silently stop satisfying it; that check sees interfaces the workspace imports or mentions. A standard method, one whose name and signature go vet's stdmethods checks (MarshalJSON() ([]byte, error), Format(fmt.State, rune), ...) or is Error() string or String() string, is refused by every subcommand, because the interface it satisfies relies on that signature; a same-named method with another signature is allowed. find_implementations lists what a change would touch.
Every subcommand is type-checked before it is applied: the plan is applied in memory and the packages it edits are checked, and errors it would introduce refuse it, listed in the error (with dry_run: success false, the errors as blocking_issue and the diffs). Errors in files the plan reports as left unedited are not counted. Only the packages the plan edits are checked, under the default build configuration of each view; a check that cannot run is listed in skipped_sites.
All call sites are updated automatically, under every build configuration (GOOS/GOARCH variants, custom build tags) that covers the workspace; a declaration with the same name in several configurations gets the same change, and an abort names the configuration it came from.`,
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ChangeSignatureInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()
		plan, err := planChangeSignature(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}

		result, err := executePlan(state, plan, fmt.Sprintf("change signature: %s %s", in.Subcommand, in.FunctionName), in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}

// planChangeSignature plans in under every build configuration. The caller
// holds the read lock.
func planChangeSignature(state *MCPServer, in ChangeSignatureInput) (*types.RefactoringPlan, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, fmt.Errorf("plan change signature: %w", err)
	}
	newParams := make([]refactor.Parameter, len(in.Params))
	for i, p := range in.Params {
		newParams[i] = refactor.Parameter{Name: p.Name, Type: p.Type}
	}
	return state.GetEngine().ChangeSignatureAcrossConfigs(refactor.ChangeSignatureRequest{
		FunctionName:         in.FunctionName,
		SourceFile:           resolveFile(ws, in.SourceFile),
		NewParams:            newParams,
		NewReturns:           in.Returns,
		Scope:                types.WorkspaceScope,
		PropagateToInterface: in.Propagate,
		DefaultValue:         in.DefaultValue,
		NewParamPosition:     in.Position,
		Subcommand:           in.Subcommand,
	})
}
