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
	FunctionName string     `json:"function_name" jsonschema:"function or method name (use Type.Method for methods)"`
	SourceFile   string     `json:"source_file" jsonschema:"file containing the function"`
	Subcommand   string     `json:"subcommand" jsonschema:"operation: add_param, remove_param, add_return, or remove_return"`
	Params       []ParamSpec `json:"params,omitempty" jsonschema:"full new parameter list (for add_param/remove_param)"`
	Returns      []string   `json:"returns,omitempty" jsonschema:"full new return type list (for add_return/remove_return)"`
	DefaultValue string     `json:"default_value,omitempty" jsonschema:"default value for new parameter at call sites"`
	Position     int        `json:"position,omitempty" jsonschema:"position index of the new parameter (for add_param)"`
	Propagate    bool       `json:"propagate,omitempty" jsonschema:"propagate changes to interface declarations and sibling implementations"`
}

func registerChangeSignatureTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "change_signature",
		Description: `Change a function or method signature. Supports four subcommands:
- add_param: add a new parameter (provide params list with the new param included, plus default_value and position)
- remove_param: remove a parameter (provide params list without the removed param)
- add_return: add a new return value (provide returns list with the new type included)
- remove_return: remove a return value (provide returns list without the removed type)
All call sites are updated automatically.`,
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ChangeSignatureInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		idx, err := state.EnsureReferenceIndex(ws)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		newParams := make([]refactor.Parameter, len(in.Params))
		for i, p := range in.Params {
			newParams[i] = refactor.Parameter{Name: p.Name, Type: p.Type}
		}

		plan, err := state.GetEngine().ChangeSignature(ws, refactor.ChangeSignatureRequest{
			FunctionName:         in.FunctionName,
			SourceFile:           resolveFile(ws, in.SourceFile),
			NewParams:            newParams,
			NewReturns:           in.Returns,
			Scope:                types.WorkspaceScope,
			PropagateToInterface: in.Propagate,
			DefaultValue:         in.DefaultValue,
			NewParamPosition:     in.Position,
			CachedIndex:          idx,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}

		state.RUnlock()

		result, err := executePlan(state, plan, fmt.Sprintf("change signature: %s %s", in.Subcommand, in.FunctionName))
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
