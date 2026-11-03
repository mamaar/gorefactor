package types

import (
	"go/token"
	"strings"
)

// Symbol represents any named entity in Go code
type Symbol struct {
	Name       string
	Kind       SymbolKind
	Package    string
	File       string
	Position   token.Pos
	End        token.Pos
	Line       int // Line number in file
	Column     int // Column number in file
	Exported   bool
	Signature  string // Function signature, type def, etc.
	DocComment string
	Parent     *Symbol     // For methods, struct fields
	Children   []*Symbol   // For types with methods/fields
	References []Reference // References to this symbol
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

// SymbolTable holds all symbols for a package
type SymbolTable struct {
	Package   *Package
	Functions map[string]*Symbol
	Types     map[string]*Symbol
	Variables map[string]*Symbol
	Constants map[string]*Symbol
	Methods   map[string][]*Symbol // type name -> methods
	Fields    map[string][]*Symbol // type name -> struct fields
}

// FindSymbol searches the symbol table for a symbol by name across all categories.
func (st *SymbolTable) FindSymbol(name string) *Symbol {
	// Dotted Type.Member form: resolve methods and struct fields explicitly.
	// Method first, then field (Go forbids a field and method of the same name
	// on a type). Fields are reachable ONLY via this dotted form — they are
	// deliberately excluded from the bare-name lookup below to avoid shadowing
	// top-level symbols. This branch is purely additive; dotted names previously
	// returned nil.
	if strings.Contains(name, ".") {
		parts := strings.Split(name, ".")
		if len(parts) == 2 {
			typeName, member := parts[0], parts[1]
			for _, m := range st.Methods[typeName] {
				if m.Name == member {
					return m
				}
			}

			for _, f := range st.Fields[typeName] {
				if f.Name == member {
					return f
				}
			}
		}

		return nil
	}

	s, ok := st.Functions[name]
	if ok {
		return s
	}

	s, ok = st.Types[name]
	if ok {
		return s
	}

	s, ok = st.Variables[name]
	if ok {
		return s
	}

	s, ok = st.Constants[name]
	if ok {
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
	Offset   int // Byte offset within the file
	File     string
	Line     int
	Column   int
	Context  string // Surrounding code context
}
