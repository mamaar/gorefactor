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
- remove_param: remove a parameter (provide params list without the removed param). The argument for that parameter is dropped at every call. Refused, listing every site and leaving files unchanged: identical unnamed parameters (ambiguous), a body that still uses the parameter (also in implementations of an interface method), a reference that is not a call (a func value), a call that cannot be lined up with the parameters (f(g()) with a multi-value g, a method expression), a dropped argument that contains a call or a channel receive (type conversions are fine), and, for a variadic parameter, a call passing any variadic argument or xs.... A trailing variadic that no call fills can therefore be removed this way.
- add_return: add a new return value (provide returns list with the new type included)
- remove_return: remove a return value (provide returns list without the removed type)
- deref_param: change one parameter from *T to T (provide the full params list with that parameter's type as T). Every use in the body must be a plain read *p, which becomes p; any other use refuses the change. Nil checks are folded: 'p == nil || *p == nil' and 'p == nil || len(*p) == 0' (slice/map T, and the && mirrors) become value checks and accept literal nil arguments; a lone 'p == nil' folds to false/true and needs every call site to pass &x, otherwise (or if the fold is not a removable if) the whole change is refused. Call sites pass x for &x and *e for any other argument; references that are not calls, and calls that cannot be rewritten, are listed in skipped_sites. Not supported for interface methods or with propagate.
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
		return nil, err
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
