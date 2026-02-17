package refactor

import (
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestExtractMethodTypeInference(t *testing.T) {
	sourceCode := `package example

type MyStruct struct {
	Name string
}

func (m *MyStruct) ProcessData(items []string, count int) error {
	total := 0
	seen := make(map[string]bool)

	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		total += count
	}

	if total > 100 {
		return nil
	}
	return nil
}
`
	ws := &types.Workspace{
		RootPath: "/tmp/test",
		Packages: map[string]*types.Package{
			"/tmp/test": {
				Path: "/tmp/test",
				Name: "example",
				Files: map[string]*types.File{
					"example.go": {
						Path:            "/tmp/test/example.go",
						OriginalContent: []byte(sourceCode),
					},
				},
				Symbols: &types.SymbolTable{},
			},
		},
	}

	op := &ExtractMethodOperation{
		SourceFile:    "example.go",
		StartLine:     11, // for _, item := range items {
		EndLine:       16, // total += count
		NewMethodName: "processItems",
		TargetStruct:  "MyStruct",
	}

	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var methodDef string
	for _, change := range plan.Changes {
		if strings.Contains(change.NewText, "func (") {
			methodDef = change.NewText
			break
		}
	}

	if methodDef == "" {
		t.Fatal("No method definition found in changes")
	}

	t.Logf("Generated method:\n%s", methodDef)

	// No interface{} types anywhere
	if strings.Contains(methodDef, "interface{}") {
		t.Errorf("Generated method contains interface{} types:\n%s", methodDef)
	}

	// Params should have real types
	if !strings.Contains(methodDef, "[]string") {
		t.Errorf("Expected []string type for items param, got:\n%s", methodDef)
	}
	if !strings.Contains(methodDef, "int") {
		t.Errorf("Expected int type, got:\n%s", methodDef)
	}

	// total (compound-assigned via +=, used after block) should be a return
	// Find the signature line (first non-empty line containing "func")
	var sigLine string
	for _, line := range strings.Split(methodDef, "\n") {
		if strings.Contains(line, "func (") {
			sigLine = line
			break
		}
	}
	if !strings.Contains(sigLine, ") int") && !strings.Contains(sigLine, ") (int") {
		t.Errorf("Expected int return type for total, got signature:\n%s", sigLine)
	}

	// seen should be a param (outer variable used in block) but NOT a return
	// (map mutations are visible through the reference, so no need to return it)
	// Extract just the return part after the last closing paren
	lastParen := strings.LastIndex(sigLine, ")")
	if lastParen > 0 {
		returnPart := sigLine[lastParen:]
		if strings.Contains(returnPart, "map[string]bool") {
			t.Errorf("seen should not be a return type (map mutations are visible through reference), got signature:\n%s", sigLine)
		}
	}
}

func TestExtractMethodPointerFieldAndLoopScope(t *testing.T) {
	sourceCode := `package example

type Result struct {
	Items []string
	Count int
}

type Processor struct {
	Name string
}

func (p *Processor) Run(result *Result, data []string) {
	counter := 0

	for _, d := range data {
		result.Items = append(result.Items, d)
		counter += 1
	}

	result.Count = counter
	_ = counter
}
`
	ws := &types.Workspace{
		RootPath: "/tmp/test2",
		Packages: map[string]*types.Package{
			"/tmp/test2": {
				Path: "/tmp/test2",
				Name: "example",
				Files: map[string]*types.File{
					"example.go": {
						Path:            "/tmp/test2/example.go",
						OriginalContent: []byte(sourceCode),
					},
				},
				Symbols: &types.SymbolTable{},
			},
		},
	}

	op := &ExtractMethodOperation{
		SourceFile:    "example.go",
		StartLine:     15, // for _, d := range data {
		EndLine:       18, // }
		NewMethodName: "processData",
		TargetStruct:  "Processor",
	}

	plan, err := op.Execute(ws)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	var methodDef string
	for _, change := range plan.Changes {
		if strings.Contains(change.NewText, "func (") {
			methodDef = change.NewText
			break
		}
	}
	if methodDef == "" {
		t.Fatal("No method definition found in changes")
	}

	t.Logf("Generated method:\n%s", methodDef)
	sigLine := strings.Split(methodDef, "\n")[0]

	// No interface{} types
	if strings.Contains(methodDef, "interface{}") {
		t.Errorf("Generated method contains interface{} types:\n%s", methodDef)
	}

	// result should NOT be a return (field mutations via pointer are visible to caller)
	if strings.Count(sigLine, "*Result") > 1 {
		t.Errorf("result should not appear as both param and return:\n%s", sigLine)
	}

	// d (for-range loop var) should NOT be a return even if 'd' appears in afterCode
	returnPart := ""
	if idx := strings.LastIndex(sigLine, ")"); idx > 0 {
		returnPart = sigLine[idx:]
	}
	// d should not appear in returns
	if strings.Contains(returnPart, "string") && !strings.Contains(returnPart, "[]string") {
		t.Errorf("loop variable 'd' should not be a return:\n%s", sigLine)
	}
}
