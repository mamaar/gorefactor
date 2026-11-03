package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- inline_method ---

type InlineMethodInput struct {
	MethodName   string `json:"method_name" jsonschema:"name of the method to inline"`
	SourceStruct string `json:"source_struct" jsonschema:"name of the struct the method belongs to"`
	TargetFile   string `json:"target_file,omitempty" jsonschema:"specific file to inline in (empty for all call sites)"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- inline_variable ---

type InlineVariableInput struct {
	VariableName string `json:"variable_name" jsonschema:"name of the variable to inline"`
	SourceFile   string `json:"source_file" jsonschema:"file containing the variable declaration"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// --- inline_function ---

type InlineFunctionInput struct {
	FunctionName string `json:"function_name" jsonschema:"name of the function to inline"`
	SourceFile   string `json:"source_file" jsonschema:"file containing the function definition"`
	DryRun       bool   `json:"dry_run,omitempty" jsonschema:"preview the changes without writing them - returns full before/after diffs for review; run again with dry_run=false to apply"`
}

// planInlineMethod plans in under every build configuration. The caller holds
// the read lock.
func planInlineMethod(state *MCPServer, in InlineMethodInput) (*types.RefactoringPlan, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, err
	}
	tgt := in.TargetFile
	if tgt != "" {
		tgt = resolveFile(ws, tgt)
	}
	return state.GetEngine().InlineMethodAcrossConfigs(types.InlineMethodRequest{
		MethodName:   in.MethodName,
		SourceStruct: in.SourceStruct,
		TargetFile:   tgt,
	})
}

// planInlineFunction plans in under every build configuration. The caller
// holds the read lock.
func planInlineFunction(state *MCPServer, in InlineFunctionInput) (*types.RefactoringPlan, error) {
	ws, err := state.GetWorkspace()
	if err != nil {
		return nil, err
	}
	return state.GetEngine().InlineFunctionAcrossConfigs(types.InlineFunctionRequest{
		FunctionName: in.FunctionName,
		SourceFile:   resolveFile(ws, in.SourceFile),
	})
}

func registerInlineTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "inline_method",
		Description: "Inline a method: replace its call sites in the target file with the method body; the method declaration is kept, and the receiver name in the body becomes the call's receiver expression. " +
			"The target file is planned under every build configuration (GOOS/GOARCH variants, custom build tags) that includes it; the inline is refused when those configurations declare the method with different bodies.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in InlineMethodInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()
		plan, err := planInlineMethod(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}
		result, err := executePlan(state, plan, "inline method "+in.SourceStruct+"."+in.MethodName, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "inline_variable",
		Description: "Inline a variable: replace all occurrences in its source file with its assigned value and remove the declaration. " +
			"Only that file is read and rewritten, and it must be part of the default build configuration.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in InlineVariableInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().InlineVariable(ws, types.InlineVariableRequest{
			VariableName: in.VariableName,
			SourceFile:   resolveFile(ws, in.SourceFile),
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "inline variable "+in.VariableName, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "inline_function",
		Description: "Inline a function: replace the calls from its own package with the function body; the function declaration is kept. " +
			"Call sites are rewritten under every build configuration (GOOS/GOARCH variants, custom build tags) that covers the workspace; the inline is refused when configurations declare the function with different bodies.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in InlineFunctionInput) (*mcpsdk.CallToolResult, any, error) {
		// The read lock stays held until the merged plan exists: a watcher
		// batch takes the write lock and may drop a view being planned in.
		state.RLock()
		plan, err := planInlineFunction(state, in)
		state.RUnlock()
		if err != nil {
			return errResult(err), nil, nil
		}
		result, err := executePlan(state, plan, "inline function "+in.FunctionName, in.DryRun)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
