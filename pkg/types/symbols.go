package types

import "go/token"

// Symbol represents any named entity in Go code
type Symbol struct {
	Name        string
	Kind        SymbolKind
	Package     string
	File        string
	Position    token.Pos  
	End         token.Pos
	Line        int         // Line number in file
	Column      int         // Column number in file
	Exported    bool
	Signature   string      // Function signature, type def, etc.
	DocComment  string
	Parent      *Symbol     // For methods, struct fields
	Children    []*Symbol   // For types with methods/fields
	References  []Reference // References to this symbol
}

type SymbolKind int

const (
	FunctionSymbol SymbolKind = iota
	MethodSymbol
	TypeSymbol
	VariableSymbol
	ConstantSymbol
	InterfaceSymbol
	StructFieldSymbol
	PackageSymbol
)

// String returns the string representation of a SymbolKind
func (k SymbolKind) String() string {
	switch k {
	case FunctionSymbol:
		return "Function"
	case MethodSymbol:
		return "Method"
	case TypeSymbol:
		return "Type"
	case VariableSymbol:
		return "Variable"
	case ConstantSymbol:
		return "Constant"
	case InterfaceSymbol:
		return "Interface"
	case StructFieldSymbol:
		return "StructField"
	case PackageSymbol:
		return "Package"
	default:
		return "Unknown"
	}
}

// SymbolRef represents a reference to a symbol
type SymbolRef struct {
	Symbol   *Symbol
	Position token.Pos
	File     string
	Context  RefContext
}

type RefContext int

const (
	CallRef RefContext = iota
	AssignRef
	TypeRef
	ImportRef
	DeclarationRef
)

// SymbolTable holds all symbols for a package
type SymbolTable struct {
	Package   *Package
	Functions map[string]*Symbol
	Types     map[string]*Symbol
	Variables map[string]*Symbol
	Constants map[string]*Symbol
	Methods   map[string][]*Symbol  // type name -> methods
}

// FindSymbol searches the symbol table for a symbol by name across all categories.
func (st *SymbolTable) FindSymbol(name string) *Symbol {
	if st == nil {
		return nil
	}
	if s, ok := st.Functions[name]; ok {
		return s
	}
	if s, ok := st.Types[name]; ok {
		return s
	}
	if s, ok := st.Variables[name]; ok {
		return s
	}
	if s, ok := st.Constants[name]; ok {
		return s
	}
	for _, methods := range st.Methods {
		for _, m := range methods {
			if m.Name == name {
				return m
			}
		}
	}
	return nil
}

// Reference represents where a symbol is used
type Reference struct {
	Symbol   *Symbol
	Position token.Pos
	Offset   int       // Byte offset within the file
	File     string
	Line     int
	Column   int
	Context  string  // Surrounding code context
}