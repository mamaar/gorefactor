package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// --- add_context_parameter ---

type AddContextParameterInput struct {
	FunctionName string `json:"function_name" jsonschema:"function or method name (use Type.Method for methods)"`
	SourceFile   string `json:"source_file" jsonschema:"file containing the function"`
	DefaultValue string `json:"default_value,omitempty" jsonschema:"value at call sites, defaults to context.TODO()"`
	Propagate    bool   `json:"propagate,omitempty" jsonschema:"propagate changes to interface declarations and sibling implementations"`
}

func registerContextTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "add_context_parameter",
		Description: `Add ctx context.Context as the first parameter to a function, updating all call sites with a default value (context.TODO() by default).
Pairs with detect_missing_context_params to find functions that need it.`,
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in AddContextParameterInput) (*mcpsdk.CallToolResult, any, error) {
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

		sourceFile := resolveFile(ws, in.SourceFile)

		// Find the function declaration and extract existing params
		funcParams, err := analysis.ExtractFuncParams(ws, sourceFile, in.FunctionName)
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
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

		state.RUnlock()

		result, err := executePlan(state, plan, fmt.Sprintf("add context parameter: %s", in.FunctionName))
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}

