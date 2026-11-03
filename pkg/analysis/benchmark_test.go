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
		_, err := resolver.BuildSymbolTable(pkg)
		if err != nil {
			b.Fatalf("Failed to build symbol table: %v", err)
		}
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
		_, err := resolver.BuildSymbolTable(pkg)
		if err != nil {
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

func BenchmarkBuildUsageGraph(b *testing.B) {
	workspace := createTypedBenchmarkWorkspace(b, 20)
	resolver := NewSymbolResolver(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	idx := resolver.BuildReferenceIndex()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = buildUsageGraph(workspace, resolver, idx)
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

func BenchmarkFindUnusedSymbols(b *testing.B) {
	workspace := createTypedBenchmarkWorkspace(b, 100)
	analyzer := NewUnusedAnalyzer(workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	analyzer.SetIncludeExported(true)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := analyzer.FindUnusedSymbols()
		if err != nil {
			b.Fatal(err)
		}
	}
}
