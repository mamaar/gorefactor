package types

import (
	"go/token"
	"testing"
)

func TestSymbol(t *testing.T) {
	symbol := &Symbol{
		Name:       "TestFunction",
		Kind:       FunctionSymbol,
		Package:    "test/package",
		File:       "/test/file.go",
		Position:   token.Pos(100),
		End:        token.Pos(200),
		Exported:   true,
		Signature:  "func TestFunction() error",
		DocComment: "TestFunction does something useful",
	}

	if symbol.Name != "TestFunction" {
		t.Errorf("Expected Name to be 'TestFunction', got '%s'", symbol.Name)
	}

	if symbol.Kind != FunctionSymbol {
		t.Errorf("Expected Kind to be FunctionSymbol, got %v", symbol.Kind)
	}

	if symbol.Package != "test/package" {
		t.Errorf("Expected Package to be 'test/package', got '%s'", symbol.Package)
	}

	if symbol.Position != token.Pos(100) {
		t.Errorf("Expected Position to be 100, got %v", symbol.Position)
	}

	if !symbol.Exported {
		t.Error("Expected symbol to be exported")
	}
}

func TestSymbolKind(t *testing.T) {
	testCases := []struct {
		name     string
		kind     SymbolKind
		expected SymbolKind
	}{
		{"FunctionSymbol", FunctionSymbol, 0},
		{"MethodSymbol", MethodSymbol, 1},
		{"TypeSymbol", TypeSymbol, 2},
		{"VariableSymbol", VariableSymbol, 3},
		{"ConstantSymbol", ConstantSymbol, 4},
		{"InterfaceSymbol", InterfaceSymbol, 5},
		{"StructFieldSymbol", StructFieldSymbol, 6},
		{"PackageSymbol", PackageSymbol, 7},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.kind != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.kind)
			}
		})
	}
}

func TestSymbolRef(t *testing.T) {
	symbol := &Symbol{Name: "TestSymbol", Kind: FunctionSymbol}
	ref := &SymbolRef{
		Symbol:   symbol,
		Position: token.Pos(150),
		File:     "/test/file.go",
		Context:  CallRef,
	}

	if ref.Symbol.Name != "TestSymbol" {
		t.Errorf("Expected Symbol.Name to be 'TestSymbol', got '%s'", ref.Symbol.Name)
	}

	if ref.Position != token.Pos(150) {
		t.Errorf("Expected Position to be 150, got %v", ref.Position)
	}

	if ref.Context != CallRef {
		t.Errorf("Expected Context to be CallRef, got %v", ref.Context)
	}
}

func TestRefContext(t *testing.T) {
	testCases := []struct {
		name     string
		context  RefContext
		expected RefContext
	}{
		{"CallRef", CallRef, 0},
		{"AssignRef", AssignRef, 1},
		{"TypeRef", TypeRef, 2},
		{"ImportRef", ImportRef, 3},
		{"DeclarationRef", DeclarationRef, 4},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.context != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.context)
			}
		})
	}
}

func TestSymbolTable(t *testing.T) {
	pkg := &Package{Name: "testpkg", Path: "test/package"}
	symbolTable := &SymbolTable{
		Package:   pkg,
		Functions: make(map[string]*Symbol),
		Types:     make(map[string]*Symbol),
		Variables: make(map[string]*Symbol),
		Constants: make(map[string]*Symbol),
		Methods:   make(map[string][]*Symbol),
	}

	// Add function symbol
	funcSymbol := &Symbol{Name: "TestFunc", Kind: FunctionSymbol}
	symbolTable.Functions["TestFunc"] = funcSymbol

	// Add type symbol
	typeSymbol := &Symbol{Name: "TestType", Kind: TypeSymbol}
	symbolTable.Types["TestType"] = typeSymbol

	// Add variable symbol
	varSymbol := &Symbol{Name: "testVar", Kind: VariableSymbol}
	symbolTable.Variables["testVar"] = varSymbol

	// Add constant symbol
	constSymbol := &Symbol{Name: "TestConst", Kind: ConstantSymbol}
	symbolTable.Constants["TestConst"] = constSymbol

	// Add method symbols
	methodSymbol := &Symbol{Name: "TestMethod", Kind: MethodSymbol}
	symbolTable.Methods["TestType"] = []*Symbol{methodSymbol}

	// Test retrievals
	if symbolTable.Functions["TestFunc"] != funcSymbol {
		t.Error("Expected to retrieve function symbol")
	}

	if symbolTable.Types["TestType"] != typeSymbol {
		t.Error("Expected to retrieve type symbol")
	}

	if symbolTable.Variables["testVar"] != varSymbol {
		t.Error("Expected to retrieve variable symbol")
	}

	if symbolTable.Constants["TestConst"] != constSymbol {
		t.Error("Expected to retrieve constant symbol")
	}

	if len(symbolTable.Methods["TestType"]) != 1 {
		t.Errorf("Expected 1 method for TestType, got %d", len(symbolTable.Methods["TestType"]))
	}

	if symbolTable.Methods["TestType"][0] != methodSymbol {
		t.Error("Expected to retrieve method symbol")
	}
}

func TestReference(t *testing.T) {
	symbol := &Symbol{Name: "RefSymbol", Kind: FunctionSymbol}
	ref := &Reference{
		Symbol:   symbol,
		Position: token.Pos(300),
		File:     "/test/ref.go",
		Line:     15,
		Column:   10,
		Context:  "fmt.Println(RefSymbol())",
	}

	if ref.Symbol.Name != "RefSymbol" {
		t.Errorf("Expected Symbol.Name to be 'RefSymbol', got '%s'", ref.Symbol.Name)
	}

	if ref.Line != 15 {
		t.Errorf("Expected Line to be 15, got %d", ref.Line)
	}

	if ref.Column != 10 {
		t.Errorf("Expected Column to be 10, got %d", ref.Column)
	}

	if ref.Context != "fmt.Println(RefSymbol())" {
		t.Errorf("Expected Context to be 'fmt.Println(RefSymbol())', got '%s'", ref.Context)
	}
}

func TestSymbolHierarchy(t *testing.T) {
	// Test parent-child relationships
	parent := &Symbol{
		Name:     "ParentType",
		Kind:     TypeSymbol,
		Children: make([]*Symbol, 0),
	}

	child1 := &Symbol{
		Name:   "Method1",
		Kind:   MethodSymbol,
		Parent: parent,
	}

	child2 := &Symbol{
		Name:   "Field1",
		Kind:   StructFieldSymbol,
		Parent: parent,
	}

	parent.Children = append(parent.Children, child1, child2)

	if len(parent.Children) != 2 {
		t.Errorf("Expected parent to have 2 children, got %d", len(parent.Children))
	}

	if child1.Parent != parent {
		t.Error("Expected child1's parent to be parent")
	}

	if child2.Parent != parent {
		t.Error("Expected child2's parent to be parent")
	}

	if parent.Children[0].Name != "Method1" {
		t.Errorf("Expected first child to be 'Method1', got '%s'", parent.Children[0].Name)
	}

	if parent.Children[1].Name != "Field1" {
		t.Errorf("Expected second child to be 'Field1', got '%s'", parent.Children[1].Name)
	}
}