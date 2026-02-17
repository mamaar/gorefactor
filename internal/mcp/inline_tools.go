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
}

// --- inline_variable ---

type InlineVariableInput struct {
	VariableName string `json:"variable_name" jsonschema:"name of the variable to inline"`
	SourceFile   string `json:"source_file" jsonschema:"file containing the variable declaration"`
}

// --- inline_function ---

type InlineFunctionInput struct {
	FunctionName string `json:"function_name" jsonschema:"name of the function to inline"`
	SourceFile   string `json:"source_file" jsonschema:"file containing the function definition"`
}

func registerInlineTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "inline_method",
		Description: "Inline a method: replace all call sites with the method body.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in InlineMethodInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		tgt := in.TargetFile
		if tgt != "" {
			tgt = resolveFile(ws, tgt)
		}
		plan, err := state.GetEngine().InlineMethod(ws, types.InlineMethodRequest{
			MethodName:   in.MethodName,
			SourceStruct: in.SourceStruct,
			TargetFile:   tgt,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "inline method "+in.SourceStruct+"."+in.MethodName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "inline_variable",
		Description: "Inline a variable: replace all occurrences with its assigned value and remove the declaration.",
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
		result, err := executePlanWithUnlock(state, plan, "inline variable "+in.VariableName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "inline_function",
		Description: "Inline a function: replace all call sites with the function body.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in InlineFunctionInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		// Collect all Go files in the workspace as target files for inlining.
		var targetFiles []string
		for _, pkg := range ws.Packages {
			for _, f := range pkg.Files {
				targetFiles = append(targetFiles, f.Path)
			}
		}
		plan, err := state.GetEngine().InlineFunction(ws, types.InlineFunctionRequest{
			FunctionName: in.FunctionName,
			SourceFile:   resolveFile(ws, in.SourceFile),
			TargetFiles:  targetFiles,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "inline function "+in.FunctionName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
