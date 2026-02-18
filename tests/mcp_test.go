package tests_test

import (
	"context"
	"flag"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

var transportFlag = flag.String("transport", "inprocess", "MCP transport: inprocess or process")
var binFlag = flag.String("bin", "./gorefactor-mcp", "path to gorefactor-mcp binary (used with -transport=process)")

func mcpTransport() mcptest.Transport {
	switch *transportFlag {
	case "process":
		return mcptest.Subprocess(*binFlag)
	default:
		return mcptest.InProcess()
	}
}

func TestMCPTools(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		tool    string
		args    func(dir string) map[string]any
	}{
		// --- Core renaming/moving ---
		{
			name: "rename_symbol", fixture: "rename_symbol", tool: "rename_symbol",
			args: func(dir string) map[string]any {
				return map[string]any{"symbol": "Add", "new_name": "Sum"}
			},
		},
		{
			name: "rename_method", fixture: "rename_method", tool: "rename_method",
			args: func(dir string) map[string]any {
				return map[string]any{
					"type_name":       "Calculator",
					"method_name":     "Add",
					"new_method_name": "Plus",
				}
			},
		},
		{
			name: "rename_package", fixture: "rename_package", tool: "rename_package",
			args: func(dir string) map[string]any {
				return map[string]any{
					"package_path":     filepath.Join(dir, "pkg", "oldname"),
					"new_package_name": "newname",
				}
			},
		},
		{
			name: "move_symbol", fixture: "move_symbol", tool: "move_symbol",
			args: func(dir string) map[string]any {
				return map[string]any{
					"symbol":       "Multiply",
					"from_package": dir,
					"to_package":   filepath.Join(dir, "pkg", "target"),
				}
			},
		},
		// --- Extract operations ---
		{
			name: "extract_function", fixture: "extract_function", tool: "extract_function",
			args: func(dir string) map[string]any {
				return map[string]any{
					"source_file":       "main.go",
					"start_line":        8,
					"end_line":          9,
					"new_function_name": "computeSum",
				}
			},
		},
		{
			name: "extract_method", fixture: "extract_method", tool: "extract_method",
			args: func(dir string) map[string]any {
				return map[string]any{
					"source_file":     "main.go",
					"start_line":      11,
					"end_line":        12,
					"new_method_name": "computeResult",
					"target_struct":   "Calculator",
				}
			},
		},
		{
			name: "extract_interface", fixture: "extract_interface", tool: "extract_interface",
			args: func(dir string) map[string]any {
				return map[string]any{
					"source_struct":  "Store",
					"interface_name": "Storage",
					"methods":        []string{"Get", "Set"},
				}
			},
		},
		{
			name: "extract_variable", fixture: "extract_variable", tool: "extract_variable",
			args: func(dir string) map[string]any {
				return map[string]any{
					"source_file":   "main.go",
					"start_line":    6,
					"end_line":      6,
					"variable_name": "result",
					"expression":    "2*3 + 4*5",
				}
			},
		},
		// --- Inline operations ---
		{
			name: "inline_function", fixture: "inline_function", tool: "inline_function",
			args: func(dir string) map[string]any {
				return map[string]any{
					"function_name": "double",
					"source_file":   "helper.go",
				}
			},
		},
		{
			name: "inline_method", fixture: "inline_method", tool: "inline_method",
			args: func(dir string) map[string]any {
				return map[string]any{
					"method_name":   "Square",
					"source_struct": "Math",
					"target_file":   "main.go",
				}
			},
		},
		{
			name: "inline_variable", fixture: "inline_variable", tool: "inline_variable",
			args: func(dir string) map[string]any {
				return map[string]any{
					"variable_name": "msg",
					"source_file":   "main.go",
				}
			},
		},
		// --- Signature & delete tools ---
		{
			name: "change_signature", fixture: "change_signature", tool: "change_signature",
			args: func(dir string) map[string]any {
				return map[string]any{
					"function_name": "Greet",
					"source_file":   "main.go",
					"subcommand":    "add_param",
					"params": []map[string]any{
						{"name": "greeting", "type": "string"},
						{"name": "name", "type": "string"},
					},
					"default_value": `"Hello"`,
					"position":      0,
				}
			},
		},
		{
			name: "add_context_parameter", fixture: "add_context_parameter", tool: "add_context_parameter",
			args: func(dir string) map[string]any {
				return map[string]any{
					"function_name": "Process",
					"source_file":   "main.go",
				}
			},
		},
		{
			name: "safe_delete", fixture: "safe_delete", tool: "safe_delete",
			args: func(dir string) map[string]any {
				return map[string]any{
					"symbol":      "deprecatedHelper",
					"source_file": "main.go",
				}
			},
		},
		// --- Code smell fixers ---
		{
			name: "fix_if_init", fixture: "fix_if_init", tool: "fix_if_init_assignments",
			args: func(dir string) map[string]any {
				return map[string]any{}
			},
		},
		{
			name: "fix_boolean_branching", fixture: "fix_boolean_branching", tool: "fix_boolean_branching",
			args: func(dir string) map[string]any {
				return map[string]any{}
			},
		},
		{
			name: "fix_deep_if_else", fixture: "fix_deep_if_else", tool: "fix_deep_if_else_chains",
			args: func(dir string) map[string]any {
				return map[string]any{}
			},
		},
		{
			name: "fix_error_wrapping", fixture: "fix_error_wrapping", tool: "fix_error_wrapping",
			args: func(dir string) map[string]any {
				return map[string]any{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := copyFixture(t, tt.fixture)

			ctx := context.Background()
			sess := mcptest.Dial(ctx, t, mcpTransport(), tmpDir)
			defer sess.Close()

			result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
				Name:      tt.tool,
				Arguments: tt.args(tmpDir),
			})
			if err != nil {
				t.Fatalf("CallTool(%s): %v", tt.tool, err)
			}
			if result.IsError {
				var errMsg string
				for _, c := range result.Content {
					if tc, ok := c.(*mcpsdk.TextContent); ok {
						errMsg += tc.Text
					}
				}
				t.Fatalf("CallTool(%s) returned error: %s", tt.tool, errMsg)
			}

			compareGoldenFiles(t, tt.fixture, tmpDir)
			checkDeleted(t, tt.fixture, tmpDir)
		})
	}
}
