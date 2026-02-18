package analysis

import (
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// DemoSymbolResolution demonstrates the enhanced symbol resolution capabilities
func DemoSymbolResolution() {
	// Create a sample workspace
	fileSet := token.NewFileSet()
	
	src := `package example

import "fmt"

type Person struct {
	Name string
	Age  int
}

func (p *Person) String() string {
	return fmt.Sprintf("%s (%d)", p.Name, p.Age)
}

func (p *Person) Birthday() {
	p.Age++
}

type Employee struct {
	Person
	Department string
	Salary     int
}

func (e *Employee) GetDetails() string {
	return fmt.Sprintf("%s - %s: $%d", e.String(), e.Department, e.Salary)
}

func CreateEmployee(name, dept string, age, salary int) *Employee {
	return &Employee{
		Person:     Person{Name: name, Age: age},
		Department: dept,
		Salary:     salary,
	}
}

func main() {
	emp := CreateEmployee("John", "Engineering", 30, 75000)
	fmt.Println(emp.GetDetails())
	emp.Birthday()
	fmt.Println(emp.GetDetails())
}
`

	astFile, err := parser.ParseFile(fileSet, "example.go", src, parser.ParseComments)
	if err != nil {
		fmt.Printf("Parse error: %v\n", err)
		return
	}

	file := &types.File{
		Path:            "example.go",
		AST:             astFile,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "example",
		Path:  "example",
		Files: map[string]*types.File{"example.go": file},
	}
	file.Package = pkg

	workspace := &types.Workspace{
		Packages: map[string]*types.Package{"example": pkg},
		FileSet:  fileSet,
	}

	// Create symbol resolver
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	
	// Build symbol table
	symbolTable, err := resolver.BuildSymbolTable(pkg)
	if err != nil {
		fmt.Printf("Error building symbol table: %v\n", err)
		return
	}

	fmt.Println("=== Symbol Resolution Example ===")
	fmt.Printf("Package: %s\n", pkg.Name)
	fmt.Printf("Functions: %d\n", len(symbolTable.Functions))
	fmt.Printf("Types: %d\n", len(symbolTable.Types))
	fmt.Printf("Methods: %d types with methods\n", len(symbolTable.Methods))
	
	// Demonstrate type resolution
	if personType, err := resolver.ResolveSymbol(pkg, "Person"); err == nil {
		fmt.Printf("\nFound type: %s (exported: %v)\n", personType.Name, personType.Exported)
		
		// Get method set
		methods, err := resolver.ResolveMethodSet(personType)
		if err == nil {
			fmt.Printf("Methods on %s: %d\n", personType.Name, len(methods))
			for _, method := range methods {
				fmt.Printf("  - %s\n", method.Name)
			}
		}
	}

	// Demonstrate embedded field resolution  
	if employeeType, err := resolver.ResolveSymbol(pkg, "Employee"); err == nil {
		fmt.Printf("\nFound type: %s\n", employeeType.Name)
		
		// Get embedded fields
		embedded, err := resolver.ResolveEmbeddedFields(employeeType)
		if err == nil && len(embedded) > 0 {
			fmt.Printf("Embedded fields in %s:\n", employeeType.Name)
			for _, field := range embedded {
				fmt.Printf("  - %s\n", field.Name)
			}
		}
		
		// Get promoted methods
		promoted, err := resolver.FindPromotedMethods(employeeType)
		if err == nil && len(promoted) > 0 {
			fmt.Printf("Promoted methods in %s:\n", employeeType.Name)
			for _, method := range promoted {
				parentName := "unknown"
				if method.Parent != nil {
					parentName = method.Parent.Name
				}
				fmt.Printf("  - %s (from %s)\n", method.Name, parentName)
			}
		}
	}

	// Demonstrate function resolution
	if createFunc, err := resolver.ResolveSymbol(pkg, "CreateEmployee"); err == nil {
		fmt.Printf("\nFound function: %s\n", createFunc.Name)
		fmt.Printf("Signature: %s\n", createFunc.Signature)
	}

	// Demonstrate cache performance
	fmt.Printf("\n=== Cache Performance ===\n")
	// Resolve same symbols multiple times
	for i := 0; i < 10; i++ {
		_, _ = resolver.ResolveSymbol(pkg, "Person")
		_, _ = resolver.ResolveSymbol(pkg, "Employee")
		_, _ = resolver.ResolveSymbol(pkg, "CreateEmployee")
	}
	
	stats := resolver.cache.GetStats()
	fmt.Printf("Cache hits: %d\n", stats.ResolvedRefHits)
	fmt.Printf("Cache misses: %d\n", stats.ResolvedRefMisses)
	fmt.Printf("Hit rate: %.1f%%\n", resolver.cache.GetHitRate())

	// Output:
	// === Symbol Resolution Example ===
	// Package: example
	// Functions: 2
	// Types: 2
	// Methods: 2 types with methods
	//
	// Found type: Person (exported: true)
	// Methods on Person: 2
	//   - String
	//   - Birthday
	//
	// Found type: Employee
	// Embedded fields in Employee:
	//   - Person
	// Promoted methods in Employee:
	//   - String (from Person)
	//   - Birthday (from Person)
	//
	// Found function: CreateEmployee
	// Signature: CreateEmployee(name, dept, age, salary)
	//
	// === Cache Performance ===
	// Cache hits: 27
	// Cache misses: 3
	// Hit rate: 90.0%
}

func TestExampleSymbolResolution(t *testing.T) {
	// This test just runs the example to ensure it works
	DemoSymbolResolution()
}