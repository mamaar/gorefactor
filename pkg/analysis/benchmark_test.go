package analysis

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io"
	"log/slog"
	"runtime"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

// Benchmark tests for symbol resolution performance

func BenchmarkSymbolResolution(b *testing.B) {
	workspace := createBenchmarkWorkspace(b, 50) // 50 packages
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Get a sample package for testing
	var testPkg *types.Package
	for _, pkg := range workspace.Packages {
		testPkg = pkg
		break
	}

	b.ResetTimer()

	// Find available symbols in the test package
	var availableSymbols []string
	if testPkg.Symbols != nil {
		for name := range testPkg.Symbols.Functions {
			availableSymbols = append(availableSymbols, name)
			if len(availableSymbols) >= 4 { // Limit to 4 for rotation
				break
			}
		}
		for name := range testPkg.Symbols.Types {
			availableSymbols = append(availableSymbols, name)
			if len(availableSymbols) >= 4 {
				break
			}
		}
	}

	if len(availableSymbols) == 0 {
		b.Fatal("No symbols available for benchmarking")
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		symbolName := availableSymbols[i%len(availableSymbols)]

		_, err := resolver.ResolveSymbol(testPkg, symbolName)
		if err != nil {
			b.Fatalf("Failed to resolve symbol %s: %v", symbolName, err)
		}
	}
}

func BenchmarkSymbolResolutionWithCache(b *testing.B) {
	workspace := createBenchmarkWorkspace(b, 50)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Warm up cache
	resolver.cache.WarmCache(workspace)

	var testPkg *types.Package
	for _, pkg := range workspace.Packages {
		testPkg = pkg
		break
	}

	b.ResetTimer()

	// Find available symbols
	var availableSymbols []string
	if testPkg.Symbols != nil {
		for name := range testPkg.Symbols.Functions {
			availableSymbols = append(availableSymbols, name)
			if len(availableSymbols) >= 4 {
				break
			}
		}
	}

	if len(availableSymbols) == 0 {
		b.Fatal("No symbols available for benchmarking")
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		symbolName := availableSymbols[i%len(availableSymbols)]

		_, err := resolver.ResolveSymbol(testPkg, symbolName)
		if err != nil {
			b.Fatalf("Failed to resolve symbol %s: %v", symbolName, err)
		}
	}
}

func BenchmarkMethodSetResolution(b *testing.B) {
	workspace := createBenchmarkWorkspace(b, 20)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Find a type symbol for testing
	var typeSymbol *types.Symbol
	for _, pkg := range workspace.Packages {
		if pkg.Symbols != nil {
			for _, symbol := range pkg.Symbols.Types {
				if symbol.Kind == types.TypeSymbol {
					typeSymbol = symbol
					break
				}
			}
		}
		if typeSymbol != nil {
			break
		}
	}

	if typeSymbol == nil {
		b.Fatal("No type symbol found for benchmark")
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := resolver.ResolveMethodSet(typeSymbol)
		if err != nil {
			b.Fatalf("Failed to resolve method set: %v", err)
		}
	}
}

func BenchmarkScopeAnalysis(b *testing.B) {
	workspace := createScopedWorkspaceForBenchmark(b)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	file := workspace.Packages["test/scoped"].Files["scoped.go"]

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := resolver.scopeAnalyzer.BuildScopeTree(file)
		if err != nil {
			b.Fatalf("Failed to build scope tree: %v", err)
		}
	}
}

func BenchmarkScopeAnalysisWithCache(b *testing.B) {
	workspace := createScopedWorkspaceForBenchmark(b)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	file := workspace.Packages["test/scoped"].Files["scoped.go"]

	// First call to populate cache
	_, _ = resolver.scopeAnalyzer.BuildScopeTree(file)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := resolver.scopeAnalyzer.BuildScopeTree(file)
		if err != nil {
			b.Fatalf("Failed to build scope tree: %v", err)
		}
	}
}

func BenchmarkFindReferences(b *testing.B) {
	workspace := createBenchmarkWorkspace(b, 10)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Find a function symbol for testing
	var funcSymbol *types.Symbol
	for _, pkg := range workspace.Packages {
		if pkg.Symbols != nil {
			for _, symbol := range pkg.Symbols.Functions {
				funcSymbol = symbol
				break
			}
		}
		if funcSymbol != nil {
			break
		}
	}

	if funcSymbol == nil {
		b.Fatal("No function symbol found for benchmark")
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := resolver.FindReferences(funcSymbol)
		if err != nil {
			b.Fatalf("Failed to find references: %v", err)
		}
	}
}

func BenchmarkBuildSymbolTable(b *testing.B) {
	workspace := createBenchmarkWorkspace(b, 1) // Single package for symbol table building

	var testPkg *types.Package
	for _, pkg := range workspace.Packages {
		testPkg = pkg
		break
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
		_, err := resolver.BuildSymbolTable(testPkg)
		if err != nil {
			b.Fatalf("Failed to build symbol table: %v", err)
		}
	}
}

func BenchmarkCacheOperations(b *testing.B) {
	cache := NewSymbolCache()

	// Create test symbol
	testSymbol := &types.Symbol{
		Name: "TestSymbol",
		Kind: types.FunctionSymbol,
	}

	b.ResetTimer()

	b.Run("Set", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			cache.SetResolvedRef(fmt.Sprintf("key%d", i), testSymbol)
		}
	})

	b.Run("Get", func(b *testing.B) {
		// Pre-populate cache
		for i := range 1000 {
			cache.SetResolvedRef(fmt.Sprintf("key%d", i), testSymbol)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			cache.GetResolvedRef(fmt.Sprintf("key%d", i%1000))
		}
	})

	b.Run("Invalidate", func(b *testing.B) {
		// Pre-populate cache
		for i := range 1000 {
			cache.SetResolvedRef(fmt.Sprintf("test/pkg%d:symbol", i), testSymbol)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			cache.InvalidatePackage(fmt.Sprintf("test/pkg%d", i%1000))
		}
	})
}

func BenchmarkLargeCodebase(b *testing.B) {
	sizes := []int{10, 50, 100, 200}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("Packages_%d", size), func(b *testing.B) {
			workspace := createBenchmarkWorkspace(b, size)
			resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

			// Warm up cache
			resolver.cache.WarmCache(workspace)

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Perform various operations across the workspace
				for _, pkg := range workspace.Packages {
					if pkg.Symbols != nil {
						// Resolve a few symbols from each package
						count := 0
						for name := range pkg.Symbols.Functions {
							if count >= 3 { // Limit to avoid excessive operations
								break
							}
							_, _ = resolver.ResolveSymbol(pkg, name)
							count++
						}
					}
				}
			}
		})
	}
}

func BenchmarkMemoryUsage(b *testing.B) {
	workspace := createBenchmarkWorkspace(b, 100)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// Perform operations that populate cache
		for _, pkg := range workspace.Packages {
			if pkg.Symbols != nil {
				for name := range pkg.Symbols.Functions {
					_, _ = resolver.ResolveSymbol(pkg, name)
					break // Just one per package
				}
			}
		}

		// Periodically clear cache to test memory cleanup
		if i%100 == 0 {
			resolver.cache.Clear()
		}
	}
}

// Helper functions for benchmark workspaces

func createBenchmarkWorkspace(b *testing.B, numPackages int) *types.Workspace {
	fileSet := token.NewFileSet()
	workspace := &types.Workspace{
		Packages: make(map[string]*types.Package),
		FileSet:  fileSet,
	}

	for pkgNum := range numPackages {
		// Create a more complex package with multiple symbols
		src := fmt.Sprintf(`package pkg%d

import "fmt"

// Types
type Type%d struct {
	Field1 string
	Field2 int
	Field3 bool
}

type Interface%d interface {
	Method1() string
	Method2(int) bool
}

// Methods
func (t *Type%d) Method1() string {
	return t.Field1
}

func (t *Type%d) Method2(val int) bool {
	return t.Field2 == val
}

func (t *Type%d) Method3() {
	fmt.Println(t.Field3)
}

// Functions
func Function%d() {
	fmt.Println("Function %d")
}

func AuxFunction%d(param string) string {
	return fmt.Sprintf("aux: %%s", param)
}

func HelperFunction%d(a, b int) int {
	return a + b
}

// Constants and Variables
const (
	Const%d = %d
	StringConst%d = "constant%d"
	BoolConst%d = true
)

var (
	Var%d = "var%d"
	IntVar%d = %d
	SliceVar%d = []string{"a", "b", "c"}
)

// Additional complexity
func ComplexFunction%d() {
	localVar := "local"
	for i := 0; i < 10; i++ {
		innerVar := i * 2
		if innerVar > 5 {
			nestedVar := "nested"
			fmt.Println(localVar, innerVar, nestedVar)
		}
	}
}
`, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum*100, pkgNum, pkgNum)

		ast, err := parser.ParseFile(fileSet, fmt.Sprintf("pkg%d.go", pkgNum), src, parser.ParseComments)
		if err != nil {
			b.Fatalf("Failed to parse package %d source: %v", pkgNum, err)
		}

		file := &types.File{
			Path:            fmt.Sprintf("pkg%d.go", pkgNum),
			AST:             ast,
			OriginalContent: []byte(src),
		}

		pkg := &types.Package{
			Name:  fmt.Sprintf("pkg%d", pkgNum),
			Path:  fmt.Sprintf("test/pkg%d", pkgNum),
			Files: map[string]*types.File{fmt.Sprintf("pkg%d.go", pkgNum): file},
		}
		file.Package = pkg

		workspace.Packages[pkg.Path] = pkg
	}

	// Build symbol tables for all packages
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, pkg := range workspace.Packages {
		if _, err := resolver.BuildSymbolTable(pkg); err != nil {
			b.Fatalf("Failed to build symbol table: %v", err)
		}
	}

	return workspace
}

func createScopedWorkspaceForBenchmark(b *testing.B) *types.Workspace {
	fileSet := token.NewFileSet()

	// Create a file with deep nesting for scope analysis benchmarking
	src := `package scoped

import "fmt"

var globalVar = "global"

func ComplexFunction(param1 string, param2 int) {
	localVar1 := "local1"
	localVar2 := param2 * 2
	
	if param2 > 0 {
		ifVar := "in if block"
		
		for i := 0; i < param2; i++ {
			loopVar := i * 2
			
			switch loopVar {
			case 0:
				caseVar := "case 0"
				fmt.Println(caseVar)
			case 2:
				caseVar := "case 2"
				fmt.Println(caseVar)
			default:
				defaultVar := "default"
				fmt.Println(defaultVar)
			}
			
			if loopVar > 5 {
				nestedVar := "deeply nested"
				
				func() {
					closureVar := "closure"
					fmt.Println(globalVar, param1, localVar1, localVar2, ifVar, loopVar, nestedVar, closureVar)
				}()
			}
		}
	}
	
	switch param1 {
	case "test":
		switchVar1 := "test case"
		fmt.Println(switchVar1)
	case "benchmark":
		switchVar2 := "benchmark case"
		
		select {
		case <-make(chan bool):
			selectVar := "select var"
			fmt.Println(selectVar)
		default:
			defaultSelectVar := "default select"
			fmt.Println(defaultSelectVar)
		}
	}
}

type ComplexType struct {
	field1 string
	field2 int
}

func (ct *ComplexType) ComplexMethod(methodParam bool) {
	methodLocal := ct.field1 + "modified"
	
	if methodParam {
		methodIf := "method if"
		fmt.Println(methodIf)
	}
	
	for j := 0; j < ct.field2; j++ {
		methodLoop := j * ct.field2
		fmt.Println(methodLocal, methodLoop)
	}
}
`

	ast, err := parser.ParseFile(fileSet, "scoped.go", src, parser.ParseComments)
	if err != nil {
		b.Fatalf("Failed to parse scoped source: %v", err)
	}

	file := &types.File{
		Path:            "scoped.go",
		AST:             ast,
		OriginalContent: []byte(src),
	}

	pkg := &types.Package{
		Name:  "scoped",
		Path:  "test/scoped",
		Files: map[string]*types.File{"scoped.go": file},
	}
	file.Package = pkg

	workspace := &types.Workspace{
		Packages: map[string]*types.Package{
			"test/scoped": pkg,
		},
		FileSet: fileSet,
	}

	// Build symbol tables
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := resolver.BuildSymbolTable(pkg); err != nil {
		b.Fatalf("Failed to build symbol table: %v", err)
	}

	return workspace
}

// createTypedBenchmarkWorkspace creates a workspace with full type-checking information.
// This enables the object index path in benchmarks.
func createTypedBenchmarkWorkspace(tb testing.TB, numPackages int) *types.Workspace {
	tb.Helper()
	fileSet := token.NewFileSet()
	workspace := &types.Workspace{
		Packages:     make(map[string]*types.Package),
		FileSet:      fileSet,
		ImportToPath: make(map[string]string),
	}

	for pkgNum := range numPackages {
		src := fmt.Sprintf(`package pkg%d

type MyStruct%d struct {
	Field1 string
	Field2 int
}

func (s *MyStruct%d) Method1() string { return s.Field1 }

func Function%d() string { return "hello" }

func Caller%d() {
	s := &MyStruct%d{}
	_ = s.Method1()
	_ = Function%d()
}

const Const%d = %d
var Var%d = "var"
`, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum, pkgNum)

		fileName := fmt.Sprintf("pkg%d.go", pkgNum)
		astFile, err := parser.ParseFile(fileSet, fileName, src, parser.ParseComments)
		if err != nil {
			tb.Fatalf("Failed to parse package %d: %v", pkgNum, err)
		}

		// Type-check the package
		conf := gotypes.Config{
			Importer: importer.Default(),
		}
		info := &gotypes.Info{
			Defs: make(map[*ast.Ident]gotypes.Object),
			Uses: make(map[*ast.Ident]gotypes.Object),
		}
		typesPkg, err := conf.Check(fmt.Sprintf("pkg%d", pkgNum), fileSet, []*ast.File{astFile}, info)
		if err != nil {
			// Type errors are expected (no cross-package resolution), continue
			_ = err
		}

		file := &types.File{
			Path:            fileName,
			AST:             astFile,
			OriginalContent: []byte(src),
		}
		pkg := &types.Package{
			Name:      fmt.Sprintf("pkg%d", pkgNum),
			Path:      fmt.Sprintf("test/pkg%d", pkgNum),
			Files:     map[string]*types.File{fileName: file},
			TypesInfo: info,
			TypesPkg:  typesPkg,
		}
		file.Package = pkg
		workspace.Packages[pkg.Path] = pkg
	}

	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, pkg := range workspace.Packages {
		if _, err := resolver.BuildSymbolTable(pkg); err != nil {
			tb.Fatalf("Failed to build symbol table: %v", err)
		}
	}

	return workspace
}

func BenchmarkBuildReferenceIndex(b *testing.B) {
	sizes := []int{10, 50, 100}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("Typed_%d_pkgs", size), func(b *testing.B) {
			workspace := createTypedBenchmarkWorkspace(b, size)
			resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = resolver.BuildReferenceIndex()
			}
		})
		b.Run(fmt.Sprintf("Untyped_%d_pkgs", size), func(b *testing.B) {
			workspace := createBenchmarkWorkspace(b, size)
			resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = resolver.BuildReferenceIndex()
			}
		})
	}
}

func BenchmarkFindReferencesIndexed_ObjectPath(b *testing.B) {
	workspace := createTypedBenchmarkWorkspace(b, 20)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	idx := resolver.BuildReferenceIndex()

	// Find a function symbol to query
	var symbol *types.Symbol
	for _, pkg := range workspace.Packages {
		if pkg.Symbols != nil {
			for _, s := range pkg.Symbols.Functions {
				symbol = s
				break
			}
		}
		if symbol != nil {
			break
		}
	}
	if symbol == nil {
		b.Fatal("No function symbol found")
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = resolver.FindReferencesIndexed(symbol, idx)
	}
}

func BenchmarkFindReferencesIndexed_NamePath(b *testing.B) {
	workspace := createBenchmarkWorkspace(b, 20) // untyped — forces name path
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	idx := resolver.BuildReferenceIndex()

	var symbol *types.Symbol
	for _, pkg := range workspace.Packages {
		if pkg.Symbols != nil {
			for _, s := range pkg.Symbols.Functions {
				symbol = s
				break
			}
		}
		if symbol != nil {
			break
		}
	}
	if symbol == nil {
		b.Fatal("No function symbol found")
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = resolver.FindReferencesIndexed(symbol, idx)
	}
}

func BenchmarkHasNonDeclarationReference_ObjectPath(b *testing.B) {
	workspace := createTypedBenchmarkWorkspace(b, 20)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	idx := resolver.BuildReferenceIndex()

	var symbol *types.Symbol
	for _, pkg := range workspace.Packages {
		if pkg.Symbols != nil {
			for _, s := range pkg.Symbols.Functions {
				symbol = s
				break
			}
		}
		if symbol != nil {
			break
		}
	}
	if symbol == nil {
		b.Fatal("No function symbol found")
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = resolver.HasNonDeclarationReference(symbol, idx)
	}
}

func BenchmarkBuildReferenceIndex_TypeIndex(b *testing.B) {
	sizes := []int{10, 50, 100}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("%d_pkgs", size), func(b *testing.B) {
			workspace := createTypedBenchmarkWorkspace(b, size)
			resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = resolver.BuildReferenceIndex()
			}
		})
	}
}

func BenchmarkFindReferencesIndexed_TypeIndexPath(b *testing.B) {
	workspace := createTypedBenchmarkWorkspace(b, 20)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	idx := resolver.BuildReferenceIndex()

	var symbol *types.Symbol
	for _, pkg := range workspace.Packages {
		if pkg.Symbols != nil {
			for _, s := range pkg.Symbols.Functions {
				symbol = s
				break
			}
		}
		if symbol != nil {
			break
		}
	}
	if symbol == nil {
		b.Fatal("No function symbol found")
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = resolver.FindReferencesIndexed(symbol, idx)
	}
}

func BenchmarkMemoryUsage_TypeIndex(b *testing.B) {
	sizes := []int{10, 50, 100}
	for _, size := range sizes {
		b.Run(fmt.Sprintf("%d_pkgs", size), func(b *testing.B) {
			workspace := createTypedBenchmarkWorkspace(b, size)
			resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))

			var memBefore, memAfter runtime.MemStats
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				runtime.GC()
				runtime.ReadMemStats(&memBefore)

				idx := resolver.BuildReferenceIndex()

				runtime.GC()
				runtime.ReadMemStats(&memAfter)

				// Keep idx alive past the memory measurement
				_ = idx
				b.ReportMetric(float64(memAfter.HeapAlloc-memBefore.HeapAlloc), "heap-bytes/op")
			}
		})
	}
}
