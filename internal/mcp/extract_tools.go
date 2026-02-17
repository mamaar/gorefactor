package mcp

import (
	"context"
	"path/filepath"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/pkg/types"
)

// --- extract_method ---

type ExtractMethodInput struct {
	SourceFile    string `json:"source_file" jsonschema:"path to the source file (absolute or relative to workspace root)"`
	StartLine     int    `json:"start_line" jsonschema:"first line of the code block to extract"`
	EndLine       int    `json:"end_line" jsonschema:"last line of the code block to extract"`
	NewMethodName string `json:"new_method_name" jsonschema:"name for the new method"`
	TargetStruct  string `json:"target_struct" jsonschema:"name of the struct to attach the method to"`
}

// --- extract_function ---

type ExtractFunctionInput struct {
	SourceFile      string `json:"source_file" jsonschema:"path to the source file"`
	StartLine       int    `json:"start_line" jsonschema:"first line of the code block to extract"`
	EndLine         int    `json:"end_line" jsonschema:"last line of the code block to extract"`
	NewFunctionName string `json:"new_function_name" jsonschema:"name for the new function"`
}

// --- extract_interface ---

type ExtractInterfaceInput struct {
	SourceStruct  string   `json:"source_struct" jsonschema:"name of the struct to extract methods from"`
	InterfaceName string   `json:"interface_name" jsonschema:"name for the new interface"`
	Methods       []string `json:"methods" jsonschema:"list of method names to include in the interface"`
	TargetPackage string   `json:"target_package,omitempty" jsonschema:"package to place the new interface in (empty for same package)"`
}

// --- extract_variable ---

type ExtractVariableInput struct {
	SourceFile   string `json:"source_file" jsonschema:"path to the source file"`
	StartLine    int    `json:"start_line" jsonschema:"line of the expression to extract"`
	EndLine      int    `json:"end_line" jsonschema:"end line of the expression"`
	VariableName string `json:"variable_name" jsonschema:"name for the new variable"`
	Expression   string `json:"expression,omitempty" jsonschema:"the expression text to extract (helps disambiguation)"`
}

func resolveFile(ws *types.Workspace, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(ws.RootPath, path)
}

func registerExtractTools(s *mcpsdk.Server, state *MCPServer) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "extract_method",
		Description: "Extract a block of code into a new method on a struct. Parameters and return values are inferred automatically.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ExtractMethodInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().ExtractMethod(ws, types.ExtractMethodRequest{
			SourceFile:    resolveFile(ws, in.SourceFile),
			StartLine:     in.StartLine,
			EndLine:       in.EndLine,
			NewMethodName: in.NewMethodName,
			TargetStruct:  in.TargetStruct,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		// executePlanWithUnlock releases the read lock, so no defer RUnlock needed
		result, err := executePlanWithUnlock(state, plan, "extract method "+in.NewMethodName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "extract_function",
		Description: "Extract a block of code into a new standalone function. Parameters and return values are inferred automatically.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ExtractFunctionInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().ExtractFunction(ws, types.ExtractFunctionRequest{
			SourceFile:      resolveFile(ws, in.SourceFile),
			StartLine:       in.StartLine,
			EndLine:         in.EndLine,
			NewFunctionName: in.NewFunctionName,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "extract function "+in.NewFunctionName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "extract_interface",
		Description: "Extract an interface from a struct's method set. The new interface contains the specified methods.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ExtractInterfaceInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		tgt := in.TargetPackage
		if tgt != "" {
			tgt = types.ResolvePackagePath(ws, tgt)
		}
		plan, err := state.GetEngine().ExtractInterface(ws, types.ExtractInterfaceRequest{
			SourceStruct:  in.SourceStruct,
			InterfaceName: in.InterfaceName,
			Methods:       in.Methods,
			TargetPackage: tgt,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "extract interface "+in.InterfaceName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "extract_variable",
		Description: "Extract an expression into a named variable. The variable is declared just before its first usage.",
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest, in ExtractVariableInput) (*mcpsdk.CallToolResult, any, error) {
		state.RLock()

		ws, err := state.GetWorkspace()
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		plan, err := state.GetEngine().ExtractVariable(ws, types.ExtractVariableRequest{
			SourceFile:   in.SourceFile,
			StartLine:    in.StartLine,
			EndLine:      in.EndLine,
			VariableName: in.VariableName,
			Expression:   in.Expression,
		})
		if err != nil {
			state.RUnlock()
			return errResult(err), nil, nil
		}
		result, err := executePlanWithUnlock(state, plan, "extract variable "+in.VariableName)
		if err != nil {
			return errResult(err), nil, nil
		}
		return textResult(result), nil, nil
	})
}
