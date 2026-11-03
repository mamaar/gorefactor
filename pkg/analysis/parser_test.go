package analysis

import (
	"errors"
	"fmt"
	gotypes "go/types"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestNewParser(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if parser == nil {
		t.Fatal("Expected NewParser to return a non-nil parser")
	}

	if parser.fileSet == nil {
		t.Error("Expected parser to have a non-nil fileSet")
	}
}

func TestParser_ParsePackage(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tempDir := t.TempDir()

	writeTestFile(t, tempDir, "main.go", `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`)
	writeTestFile(t, tempDir, "utils.go", `package main

import "strings"

func ToUpper(s string) string {
	return strings.ToUpper(s)
}
`)
	writeTestFile(t, tempDir, "main_test.go", `package main

import "testing"

func TestToUpper(t *testing.T) {
	result := ToUpper("hello")
	if result != "HELLO" {
		t.Errorf("Expected HELLO, got %s", result)
	}
}
`)

	pkg, err := parser.ParsePackage(tempDir)
	if err != nil {
		t.Fatalf("Failed to parse package: %v", err)
	}

	t.Run("name, dir and file split", func(t *testing.T) {
		if pkg.Name != "main" {
			t.Errorf("Expected package name to be 'main', got '%s'", pkg.Name)
		}
		if pkg.Dir != tempDir {
			t.Errorf("Expected Dir to be '%s', got '%s'", tempDir, pkg.Dir)
		}
		if len(pkg.Files) != 2 {
			t.Errorf("Expected 2 regular files, got %d", len(pkg.Files))
		}
		if len(pkg.TestFiles) != 1 {
			t.Errorf("Expected 1 test file, got %d", len(pkg.TestFiles))
		}
	})

	t.Run("imports", func(t *testing.T) {
		expectedImports := []string{"fmt", "strings"}
		if len(pkg.Imports) != len(expectedImports) {
			t.Errorf("Expected %d imports, got %d", len(expectedImports), len(pkg.Imports))
		}
		for _, expectedImport := range expectedImports {
			if !slices.Contains(pkg.Imports, expectedImport) {
				t.Errorf("Expected import '%s' not found", expectedImport)
			}
		}
	})

	t.Run("files link back to package", func(t *testing.T) {
		for _, file := range pkg.Files {
			if file.Package != pkg {
				t.Error("Expected file to have back-reference to package")
			}
		}
		for _, file := range pkg.TestFiles {
			if file.Package != pkg {
				t.Error("Expected test file to have back-reference to package")
			}
		}
	})
}

func TestParser_ParsePackage_Empty(t *testing.T) {
	parser := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Create an empty directory
	tempDir := t.TempDir()

	// Try to parse empty package
	_, err := parser.ParsePackage(tempDir)
	if err == nil {
		t.Error("Expected error when parsing empty package")
	}

	// Check that it's a RefactorError
	var refErr *types.RefactorError
	if errors.As(err, &refErr) {
		if refErr.Type != types.ParseError {
			t.Errorf("Expected ParseError, got %v", refErr.Type)
		}
	} else {
		t.Error("Expected RefactorError")
	}
}

func TestParser_ParseWorkspace(t *testing.T) {
	tempDir := t.TempDir()

	writeTestFile(t, tempDir, "go.mod", "module test/workspace\n\ngo 1.21\n")
	writeTestFile(t, tempDir, "cmd/main/main.go", `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`)
	writeTestFile(t, tempDir, "pkg/lib/lib.go", `package lib

func Add(a, b int) int {
	return a + b
}
`)

	_, ws, err := parseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("Failed to parse workspace: %v", err)
	}

	t.Run("root, module and file set", func(t *testing.T) {
		if ws.RootPath != tempDir {
			t.Errorf("Expected RootPath to be '%s', got '%s'", tempDir, ws.RootPath)
		}
		if ws.Module == nil {
			t.Error("Expected Module to be non-nil")
		} else if ws.Module.Path != "test/workspace" {
			t.Errorf("Expected module path to be 'test/workspace', got '%s'", ws.Module.Path)
		}
		if ws.FileSet == nil {
			t.Error("Expected FileSet to be non-nil")
		}
	})

	t.Run("packages are named and populated", func(t *testing.T) {
		if len(ws.Packages) < 2 {
			t.Errorf("Expected at least 2 packages, got %d", len(ws.Packages))
		}
		for _, pkg := range ws.Packages {
			if pkg.Name == "" {
				t.Error("Expected package to have a name")
			}
			if len(pkg.Files) == 0 {
				t.Error("Expected package to have files")
			}
		}
	})
}

// TestParser_TypeCheckPackage_DiamondMemoization is a regression test for the
// exponential re-checking bug: a package that FAILS to type-check (here,
// because it imports an unresolvable third-party module) must still be
// memoised, or every dependent that imports it re-triggers a full re-check —
// recursively, through every package IT depends on.
//
// The workspace is a diamond: d imports b and c, both of which import a, and
// a imports a module path that can never resolve. Under the old
// "TypesPkg != nil" memo guard, a package that fails never gets cached, so
// checking a is repeated once per importer (here 2x instead of 1x). This
// test fails on the pre-fix code and passes after it — verified by stashing
// the parser.go fix and re-running.
func TestParser_TypeCheckPackage_DiamondMemoization(t *testing.T) {
	tempDir := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(tempDir, rel)
		err := os.MkdirAll(filepath.Dir(full), 0755)
		if err != nil {
			t.Fatalf("failed to create dir for %s: %v", rel, err)
		}
		err = os.WriteFile(full, []byte(content), 0644)
		if err != nil {
			t.Fatalf("failed to write %s: %v", rel, err)
		}
	}

	writeFile("go.mod", "module test/diamond\n\ngo 1.21\n")

	// a imports a third-party module that can never resolve (workspaceImporter
	// falls back to importer.Default(), which is GOPATH-based and can't see
	// it), so a's own type-check always fails.
	writeFile("a/a.go", `package a

import bad "example.com/nonexistent/thirdparty"

type T int

func UseBad() bad.Thing {
	var v bad.Thing
	return v
}
`)

	writeFile("b/b.go", `package b

import "test/diamond/a"

func UseA() a.T {
	var v a.T
	return v
}
`)

	writeFile("c/c.go", `package c

import "test/diamond/a"

func UseA() a.T {
	var v a.T
	return v
}
`)

	writeFile("d/d.go", `package d

import (
	"test/diamond/b"
	"test/diamond/c"
)

func UseBoth() {
	b.UseA()
	c.UseA()
}
`)

	parser, ws, err := parseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	counts := make(map[string]int)
	parser.typeCheckHook = func(pkg *types.Package) {
		counts[pkg.ImportPath]++
	}

	fsPath, ok := ws.ImportToPath["test/diamond/d"]
	if !ok {
		t.Fatal("expected test/diamond/d to be in ImportToPath")
	}
	pkgD, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatal("expected package d to be parsed")
	}

	parser.EnsureTypeChecked(ws, pkgD)

	for _, name := range []string{"test/diamond/a", "test/diamond/b", "test/diamond/c", "test/diamond/d"} {
		got := counts[name]
		if got != 1 {
			t.Errorf("expected package %s to be type-checked exactly once, got %d", name, got)
		}
	}
}

// TestParser_TypeCheckPackage_CycleThroughTestFiles is a regression test for
// the recursion guard: TypeCheckPackage folds in-package _test.go files into
// the same check as the regular files (see the comment on TypeCheckPackage),
// so a cycle "a_test.go imports b, b imports a" is reachable even though Go
// forbids import cycles in non-test code. Without a guard this recurses
// forever (stack overflow); the test asserts it terminates instead.
func TestParser_TypeCheckPackage_CycleThroughTestFiles(t *testing.T) {
	tempDir := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(tempDir, rel)
		err := os.MkdirAll(filepath.Dir(full), 0755)
		if err != nil {
			t.Fatalf("failed to create dir for %s: %v", rel, err)
		}
		err = os.WriteFile(full, []byte(content), 0644)
		if err != nil {
			t.Fatalf("failed to write %s: %v", rel, err)
		}
	}

	writeFile("go.mod", "module test/cycle\n\ngo 1.21\n")

	writeFile("a/a.go", `package a

func F() {}
`)
	writeFile("a/a_test.go", `package a

import "test/cycle/b"

func UseB() {
	b.G()
}
`)
	writeFile("b/b.go", `package b

import "test/cycle/a"

func G() {
	a.F()
}
`)

	parser, ws, err := parseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	fsPath, ok := ws.ImportToPath["test/cycle/a"]
	if !ok {
		t.Fatal("expected test/cycle/a to be in ImportToPath")
	}
	pkgA, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatal("expected package a to be parsed")
	}

	done := make(chan struct{})
	go func() {
		parser.EnsureTypeChecked(ws, pkgA)
		close(done)
	}()

	select {
	case <-done:
		// Terminated, as expected.
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureTypeChecked did not terminate — cycle guard is broken")
	}

	if !pkgA.TypesChecked {
		t.Error("expected package a to be marked TypesChecked after the call returns")
	}

	// Terminating by handing b an empty placeholder for a would fail b's use
	// of a.F; b must resolve a's real exported symbols instead.
	if len(pkgA.ExcludedTestFiles) > 0 {
		t.Errorf("expected package a's test files to be included despite the cycle, got excluded (cause: %v)",
			pkgA.ExcludedTestFiles)
	}
	if pkgA.TypesErr != nil {
		t.Errorf("expected package a to type-check cleanly despite the cycle, got: %v", pkgA.TypesErr)
	}
}

// TestParser_TypeCheckPackage_DiamondRescueIsConsistentAcrossImporters is a
// regression test for a correctness bug in an earlier version of the
// production-only rescue used to break the FALSE "cycle" TypeCheckPackage's
// always-fold-test-files design can manufacture even for entirely valid Go
// (see TestParser_TypeCheckPackage_CycleThroughTestFiles for the genuine,
// Go-forbidden case; this one is the other kind).
//
// Note the shape here is deliberately NOT "a_test.go imports b, b imports a"
// — real Go itself rejects that outright ("import cycle not allowed in
// test"), confirmed by running `go vet`/`go test` against exactly that
// workspace shape while building this test. What real Go DOES allow, and
// what this reproduces, is a longer chain where the package that closes the
// loop is reached only via ANOTHER package's in-package test file being
// folded in — which real Go never does for an ordinary dependency (it
// compiles that package plain, ignoring its tests entirely), but
// TypeCheckPackage does, unconditionally, every time it checks any package
// for any reason. That is what turns a plain diamond into a false cycle:
//
//	dep         — has dep_test.go, which imports slow
//	slow        — production imports mid
//	mid         — has mid_test.go (folded in only because TypeCheckPackage
//	              checks mid at all, not because slow's plain import needed
//	              it), which imports bridge
//	bridge      — production imports dep directly AND imports other,
//	              passing a *dep.Thing between them
//	other       — production imports dep too
//
// dep's own check (production + dep_test.go) reaches bridge through slow and
// mid's test-fold, and bridge's own import of dep finds dep still on the
// stack — the in-progress rescue fires for bridge. Before the consistency
// fix, other (which ALSO imports dep, from its own, separate, non-nested
// check) could receive dep's real, post-cycle view instead of that same
// rescue, so bridge.Field's type and other.Use's parameter type failed to
// match — a spurious "cannot use ... as ... value" error, exactly the
// failure first observed against a real, large workspace.
func TestParser_TypeCheckPackage_DiamondRescueIsConsistentAcrossImporters(t *testing.T) {
	tempDir := t.TempDir()

	writeTestFile(t, tempDir, "go.mod", "module test/diamond2\n\ngo 1.21\n")

	writeTestFile(t, tempDir, "dep/dep.go", `package dep

type Thing struct{}
`)
	writeTestFile(t, tempDir, "dep/dep_test.go", `package dep

import (
	"testing"

	"test/diamond2/slow"
)

func TestDep(t *testing.T) {
	slow.Ping()
}
`)
	writeTestFile(t, tempDir, "slow/slow.go", `package slow

import "test/diamond2/mid"

func Ping() { mid.Noop() }
`)
	writeTestFile(t, tempDir, "mid/mid.go", `package mid

func Noop() {}
`)
	writeTestFile(t, tempDir, "mid/mid_test.go", `package mid

import (
	"testing"

	"test/diamond2/bridge"
)

func TestMid(t *testing.T) {
	bridge.Run(&bridge.Bridge{})
}
`)
	writeTestFile(t, tempDir, "bridge/bridge.go", `package bridge

import (
	"test/diamond2/dep"
	"test/diamond2/other"
)

type Bridge struct {
	Field *dep.Thing
}

func Run(b *Bridge) {
	other.Use(b.Field)
}
`)
	writeTestFile(t, tempDir, "other/other.go", `package other

import "test/diamond2/dep"

func Use(t *dep.Thing) {}
`)
	// entry never participates in the diamond at all — it is checked LAST,
	// as an entirely separate top-level target, specifically so its own
	// direct import of dep happens only after dep's combined check has
	// fully returned (dep.TypesChecked is already true by then, so this
	// import takes the "not currently in progress" branch of Import,
	// exactly where the consistency check lives). If that branch ever
	// forgets to prefer an existing rescue, entry.Field ends up typed via
	// dep's real, post-cycle view while other.Use (resolved earlier,
	// nested inside bridge's own check while dep was still in progress)
	// keeps the rescue view — the two-views-of-dep mismatch.
	writeTestFile(t, tempDir, "entry/entry.go", `package entry

import (
	"test/diamond2/dep"
	"test/diamond2/other"
)

type Entry struct {
	Field *dep.Thing
}

func Run(e *Entry) {
	other.Use(e.Field)
}
`)

	parser, ws, err := parseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	pkgDep := packageByImportPath(t, ws, "test/diamond2/dep")

	// dep is the entry point deliberately: its own combined check is what
	// starts the chain that reaches bridge nested (via slow, then mid's
	// folded-in test file), which is the exact ordering that exposed the
	// inconsistency — other's own, separate, later resolution of dep used
	// to receive the real view while bridge had already received the
	// rescue.
	done := make(chan struct{})
	go func() {
		parser.EnsureTypeChecked(ws, pkgDep)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("EnsureTypeChecked did not terminate — cycle guard is broken")
	}

	if len(pkgDep.ExcludedTestFiles) > 0 {
		t.Errorf("expected dep's test files to be included, got excluded (cause: %v)", pkgDep.ExcludedTestFiles)
	}

	pkgBridge := packageByImportPath(t, ws, "test/diamond2/bridge")
	if pkgBridge.TypesErr != nil {
		t.Fatalf("expected bridge to type-check cleanly, got: %v (this is the two-views-of-dep "+
			"inconsistency if it mentions dep.Thing / other.Use)", pkgBridge.TypesErr)
	}

	pkgEntry := packageByImportPath(t, ws, "test/diamond2/entry")
	// Checked as a separate, later EnsureTypeChecked call, deliberately: by
	// now dep.TypesChecked is already true (dep's own check, above, has
	// fully returned), so entry's own direct import of dep takes the
	// "already checked" branch of Import rather than the in-progress one —
	// exactly the branch that must still prefer an existing rescue over
	// dep's real view for this test to mean anything.
	parser.EnsureTypeChecked(ws, pkgEntry)
	if pkgEntry.TypesErr != nil {
		t.Fatalf("expected entry to type-check cleanly, got: %v (this is the two-views-of-dep "+
			"inconsistency if it mentions dep.Thing / other.Use)", pkgEntry.TypesErr)
	}

	// And the primary feature this rescue exists to preserve: dep's own
	// test file call to slow.Ping must still be findable via CallSitesOf,
	// exactly like TestSymbolResolver_CallSitesOf_TestOnlyThirdPartyImport
	// checks for the unresolved-third-party-import trigger.
	pkgSlow := packageByImportPath(t, ws, "test/diamond2/slow")
	parser.EnsureTypeChecked(ws, pkgSlow)

	seed := funcDefNamed(t, pkgSlow, "Ping")
	idx := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil))).BuildReferenceIndex()
	var sawTest bool
	for _, site := range idx.CallSitesOf(seed) {
		if strings.HasSuffix(site.File.Path, "dep_test.go") {
			sawTest = true
		}
	}
	if !sawTest {
		t.Error("expected a call site in dep_test.go (this is the bug: it used to go missing)")
	}
}

// TestParser_TypeCheckPackage_ConcurrentSharedDependency is a regression test
// for the in-progress marker's meaning. The marker exists to break cycles, and
// Import breaks a cycle by handing go/types an empty placeholder package. That
// is only correct when the marked package is on the *same* call stack; if the
// marker could also be set by another goroutine checking the same dependency,
// Import would hand out a placeholder for a perfectly good package, the
// dependent would fail to resolve anything from it, and — because results are
// memoized — that degraded type info would be cached permanently.
//
// MCP handlers hold only a read lock on the workspace state, so concurrent
// requests really do reach the importer at once. Here several goroutines
// type-check different packages that all import one shared, deliberately slow
// dependency. Every dependent must end up with a clean type-check; a leaked
// placeholder shows up as a non-nil TypesErr on whichever dependent lost the
// race.

// TestParser_TypeCheckPackage_ExternalTestPackage is a regression test for
// checkExternalTestPackage: a _test.go file declaring `package foo_test`
// (a different package name than foo's own, despite living in foo's
// directory) is a SEPARATE compilation unit that TypeCheckPackage's
// withTests fold cannot include — go/types.Checker requires every file
// passed to one Check() call to declare the same package name. Before
// checkExternalTestPackage existed, such a file was parsed (present in
// pkg.TestFiles) but never type-checked at all: no Defs/Uses entries, so
// CallSitesOf/NonCallUsesOf were blind to every identifier in it, silently,
// as a permanent gap that nothing reported.
func TestParser_TypeCheckPackage_ExternalTestPackage(t *testing.T) {
	tempDir := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(tempDir, rel)
		err := os.MkdirAll(filepath.Dir(full), 0755)
		if err != nil {
			t.Fatalf("failed to create dir for %s: %v", rel, err)
		}
		err = os.WriteFile(full, []byte(content), 0644)
		if err != nil {
			t.Fatalf("failed to write %s: %v", rel, err)
		}
	}

	writeFile("go.mod", "module test/externaltest\n\ngo 1.21\n")
	writeFile("foo/foo.go", `package foo

func Process(name string) string { return name }
`)
	// package foo_test, not foo — an external test package. It imports foo
	// the same way any other, unrelated package would: by its import path.
	writeFile("foo/foo_test.go", `package foo_test

import (
	"testing"

	"test/externaltest/foo"
)

func TestProcess(t *testing.T) {
	foo.Process("x")
}
`)

	parser, ws, err := parseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	fsPath, ok := ws.ImportToPath["test/externaltest/foo"]
	if !ok {
		t.Fatal("expected test/externaltest/foo to be in ImportToPath")
	}
	pkgFoo, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatal("expected package foo to be parsed")
	}

	parser.EnsureTypeChecked(ws, pkgFoo)
	if pkgFoo.TypesErr != nil {
		t.Fatalf("expected foo to type-check cleanly, got: %v", pkgFoo.TypesErr)
	}

	var seed *gotypes.Func
	for ident, obj := range pkgFoo.TypesInfo.Defs {
		if ident.Name == "Process" {
			seed, _ = obj.(*gotypes.Func)
		}
	}
	if seed == nil {
		t.Fatal("expected Process to resolve to a go/types object")
	}

	idx := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil))).BuildReferenceIndex()
	var sawExternalTest bool
	for _, site := range idx.CallSitesOf(seed) {
		if strings.HasSuffix(site.File.Path, "foo_test.go") {
			sawExternalTest = true
		}
	}
	if !sawExternalTest {
		t.Error("expected a call site in the external test package's foo_test.go " +
			"(this is the bug: external test packages were never type-checked at all)")
	}
}

func TestParser_TypeCheckPackage_ConcurrentSharedDependency(t *testing.T) {
	tempDir := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(tempDir, rel)
		err := os.MkdirAll(filepath.Dir(full), 0755)
		if err != nil {
			t.Fatalf("failed to create dir for %s: %v", rel, err)
		}
		err = os.WriteFile(full, []byte(content), 0644)
		if err != nil {
			t.Fatalf("failed to write %s: %v", rel, err)
		}
	}

	writeFile("go.mod", "module test/conc\n\ngo 1.21\n")

	// Make the shared dependency big enough that checking it takes long enough
	// for the other goroutines to pile up behind it.
	var dep strings.Builder
	dep.WriteString("package dep\n\ntype T int\n")
	for i := range 400 {
		fmt.Fprintf(&dep, "\nfunc F%d(a, b T) T { x := a + b; y := x * 2; return y - a }\n", i)
	}
	writeFile("dep/dep.go", dep.String())

	const dependents = 8
	for i := range dependents {
		writeFile(fmt.Sprintf("user%d/user%d.go", i, i), fmt.Sprintf(`package user%d

import "test/conc/dep"

func Use() dep.T {
	var v dep.T
	return dep.F0(v, v)
}
`, i))
	}

	parser, ws, err := parseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	pkgs := make([]*types.Package, 0, dependents)
	for i := range dependents {
		importPath := fmt.Sprintf("test/conc/user%d", i)
		fsPath, ok := ws.ImportToPath[importPath]
		if !ok {
			t.Fatalf("expected %s to be in ImportToPath", importPath)
		}
		pkg, ok := ws.Packages[fsPath]
		if !ok {
			t.Fatalf("expected package %s to be parsed", importPath)
		}
		pkgs = append(pkgs, pkg)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, pkg := range pkgs {
		wg.Add(1)
		go func(pkg *types.Package) {
			defer wg.Done()
			<-start // release all goroutines together to maximise overlap
			parser.EnsureTypeChecked(ws, pkg)
		}(pkg)
	}
	close(start)
	wg.Wait()

	for _, pkg := range pkgs {
		if !pkg.TypesChecked {
			t.Errorf("expected %s to be marked TypesChecked", pkg.ImportPath)
			continue
		}
		if pkg.TypesErr == nil {
			continue
		}
		t.Errorf("package %s failed to type-check against the shared dependency: %v\n"+
			"this means Import handed it a placeholder for test/conc/dep instead of the real package",
			pkg.ImportPath, pkg.TypesErr)
	}
}

// TestParser_WorkspaceImporter_ExternalModuleFallback is a regression test for
// the workspaceImporter.Import fallback: replacing importer.Default() (GOPATH
// only, cannot see the module cache) with a go/packages-backed loader. It
// loads this very repository as the workspace — a real module with real
// third-party dependencies already in the local module cache — and
// type-checks pkg/analysis itself, which imports golang.org/x/tools/go/packages.
// Before the fix this package's TypesErr would be non-nil (the import
// unresolvable) and any type-aware logic guarded on TypeOf() for its symbols
// would silently degrade; this asserts both the clean type-check and that a
// symbol from the external package actually resolves.
func TestParser_WorkspaceImporter_ExternalModuleFallback(t *testing.T) {
	// Skip rather than fail if the module cache doesn't already have
	// golang.org/x/tools: GOPROXY=off (set inside loadExternal) makes a cold
	// cache fail fast instead of hanging on a network fetch, but a cold cache
	// is a legitimate reason to skip this test, not to fail it — the test
	// must never become flaky in a clean environment with no prior `go build`.
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "golang.org/x/tools")
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("golang.org/x/tools not present in the local module cache; skipping")
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("failed to resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Skipf("repo root %s has no go.mod; skipping", repoRoot)
	}

	parser, ws, err := parseWorkspace(repoRoot)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	const importPath = "github.com/mamaar/gorefactor/pkg/analysis"
	fsPath, ok := ws.ImportToPath[importPath]
	if !ok {
		t.Fatalf("expected %s to be in ImportToPath", importPath)
	}
	pkg, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatalf("expected %s to be parsed", importPath)
	}

	parser.EnsureTypeChecked(ws, pkg)

	if pkg.TypesErr != nil {
		t.Fatalf("expected %s to type-check cleanly via the go/packages fallback, got: %v", importPath, pkg.TypesErr)
	}
	if pkg.TypesPkg == nil {
		t.Fatal("expected TypesPkg to be populated")
	}

	var extPkg *gotypes.Package
	for _, imp := range pkg.TypesPkg.Imports() {
		if imp.Path() == "golang.org/x/tools/go/packages" {
			extPkg = imp
			break
		}
	}
	if extPkg == nil {
		t.Fatal("expected golang.org/x/tools/go/packages among pkg/analysis's resolved imports")
	}
	obj := extPkg.Scope().Lookup("Load")
	if obj == nil {
		t.Error("expected golang.org/x/tools/go/packages.Load to resolve from the externally-loaded package")
	}
}

// TestSymbolResolver_CallSitesOf_TestOnlyThirdPartyImport is a regression
// test for a silent gap in workspaceImporter.loadExternal: with cfg.Tests
// true, packages.Load(cfg, "./...") returns TWO *packages.Package values for
// a workspace-local package that has tests — the production variant and the
// "p [p.test]" in-package-test variant — sharing one PkgPath but distinguished
// only by ID. loadExternal's cycle-avoidance walk used to dedupe on PkgPath,
// so whichever of the two variants it visited first marked the PkgPath seen
// and the OTHER variant's extra import edges (anything only a _test.go file
// imports) were never walked — so a third-party module imported EXCLUSIVELY
// from _test.go files anywhere in the workspace never reached imp.ext.
//
// The consequence reached far past loadExternal: Import() then failed to
// resolve that module, which failed the package's "with tests" type-check
// attempt, and (*GoParser).TypeCheckPackage's fallback silently retried
// WITHOUT any of that package's test files — dropping every _test.go
// identifier from TypesInfo, in every package with such an import, not just
// the one that has it. That is how a call site in an entirely unrelated
// _test.go file (in the same package) went missing from CallSitesOf with no
// error anywhere: this test's own case, ss_arcgis_tokens_test.go calling
// GetTokenForService while a SIBLING test file in the same package imported
// github.com/DATA-DOG/go-sqlmock.
//
// This loads a synthetic module (not this repo) whose only package, foo, has
// a production call site (bar.go) and a _test.go call site guarded by a
// real third-party import (golang.org/x/tools/go/ast/inspector, chosen
// because it is already a direct dependency of this very tool and so is
// already present in the local module cache — see the skip guard below,
// mirrored from TestParser_WorkspaceImporter_ExternalModuleFallback).
func TestSymbolResolver_CallSitesOf_TestOnlyThirdPartyImport(t *testing.T) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}} {{.Version}} {{.Sum}} {{.GoModSum}}", "golang.org/x/tools")
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	out, err := cmd.Output()
	fields := strings.Fields(string(out))
	if err != nil || len(fields) != 4 {
		t.Skip("golang.org/x/tools not present in the local module cache; skipping")
	}
	version, sum, goModSum := fields[1], fields[2], fields[3]

	tempDir := t.TempDir()

	writeTestFile(t, tempDir, "go.mod", "module example.com/testws\n\ngo 1.26.0\n\nrequire golang.org/x/tools "+version+"\n")
	writeTestFile(t, tempDir, "go.sum",
		"golang.org/x/tools "+version+" "+sum+"\n"+
			"golang.org/x/tools "+version+"/go.mod "+goModSum+"\n")
	writeTestFile(t, tempDir, "foo/foo.go", `package foo

func Process(name string) string { return name }
`)
	// A normal production call site, so this test also confirms the fix does
	// not somehow cost the package its non-test call sites.
	writeTestFile(t, tempDir, "foo/bar.go", `package foo

func Run() string { return Process("prod") }
`)
	// The test-only third-party import that triggers the bug: nothing in
	// foo's production files imports golang.org/x/tools, only this file does.
	writeTestFile(t, tempDir, "foo/foo_test.go", `package foo

import (
	"testing"

	"golang.org/x/tools/go/ast/inspector"
)

func TestProcess(t *testing.T) {
	_ = inspector.New(nil)
	Process("test")
}
`)

	parser, ws, err := parseWorkspace(tempDir)
	if err != nil {
		t.Fatalf("failed to parse workspace: %v", err)
	}

	pkg := packageByImportPath(t, ws, "example.com/testws/foo")

	parser.EnsureTypeChecked(ws, pkg)
	if len(pkg.ExcludedTestFiles) > 0 {
		t.Fatalf("expected foo's test files to type-check together with the package, got excluded (cause: %v)",
			pkg.ExcludedTestFiles)
	}
	if pkg.TypesErr != nil {
		t.Fatalf("expected foo to type-check cleanly, got: %v", pkg.TypesErr)
	}

	seed := funcDefNamed(t, pkg, "Process")

	idx := NewSymbolResolver(ws, slog.New(slog.NewTextHandler(io.Discard, nil))).BuildReferenceIndex()
	if !idx.HasTypeInfo() {
		t.Fatal("expected the reference index to have type info")
	}

	sites := idx.CallSitesOf(seed)
	var sawProd, sawTest bool
	for _, site := range sites {
		switch {
		case strings.HasSuffix(site.File.Path, "bar.go"):
			sawProd = true
		case strings.HasSuffix(site.File.Path, "foo_test.go"):
			sawTest = true
		}
	}
	if !sawProd {
		t.Errorf("expected a call site in bar.go, got sites: %+v", sites)
	}
	if !sawTest {
		t.Errorf("expected a call site in foo_test.go (this is the bug: it used to go missing), got sites: %+v", sites)
	}
}

// TestParser_WorkspaceImporter_HasExternalImports covers the check that
// decides whether loading the module graph via go/packages is worth doing.
// The interesting case is the third one: pkg.Imports is only accumulated from
// non-test files, so a workspace whose sole third-party dependency sits in a
// _test.go looks dependency-free unless test files are scanned too. Getting
// that wrong silently costs those test files their type information, because
// TypeCheckPackage folds in-package test files into the same check.
func TestParser_WorkspaceImporter_HasExternalImports(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{
			name: "stdlib and local imports only",
			files: map[string]string{
				"go.mod": "module test/ext\n\ngo 1.21\n",
				"a/a.go": "package a\n\nimport \"fmt\"\n\nfunc A() { fmt.Println(\"x\") }\n",
				"b/b.go": "package b\n\nimport \"test/ext/a\"\n\nfunc B() { a.A() }\n",
				"c/c.go": "package c\n\nimport \"strings\"\n\nfunc C() string { return strings.TrimSpace(\" x \") }\n",
			},
			want: false,
		},
		{
			name: "third-party import in a regular file",
			files: map[string]string{
				"go.mod": "module test/ext\n\ngo 1.21\n",
				"a/a.go": "package a\n\nimport \"golang.org/x/tools/go/packages\"\n\nvar _ = packages.Load\n",
			},
			want: true,
		},
		{
			name: "third-party import only in a test file",
			files: map[string]string{
				"go.mod":      "module test/ext\n\ngo 1.21\n",
				"a/a.go":      "package a\n\nfunc A() int { return 1 }\n",
				"a/a_test.go": "package a\n\nimport (\n\t\"testing\"\n\n\t\"golang.org/x/tools/go/packages\"\n)\n\nvar _ = packages.Load\n\nfunc TestA(t *testing.T) { _ = A() }\n",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			for rel, content := range tt.files {
				full := filepath.Join(tempDir, rel)
				err := os.MkdirAll(filepath.Dir(full), 0755)
				if err != nil {
					t.Fatalf("failed to create dir for %s: %v", rel, err)
				}
				err = os.WriteFile(full, []byte(content), 0644)
				if err != nil {
					t.Fatalf("failed to write %s: %v", rel, err)
				}
			}

			parser, _, err := parseWorkspace(tempDir)
			if err != nil {
				t.Fatalf("failed to parse workspace: %v", err)
			}

			got := parser.importer.hasExternalImports()
			if got != tt.want {
				t.Errorf("hasExternalImports() = %v, want %v", got, tt.want)
			}
		})
	}
}

const (
	fts5TestSrc   = "//go:build fts5\n\npackage p\n\nvar registry = []string{\"fts5\"}\n"
	nofts5TestSrc = "//go:build !fts5\n\npackage p\n\nvar registry = []string{}\n"
)

func writeWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module example.com/w\n\ngo 1.22\n"
	for name, content := range files {
		path := filepath.Join(root, name)
		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(path, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	err := os.MkdirAll(filepath.Dir(full), 0o755)
	if err != nil {
		t.Fatalf("failed to create dir for %s: %v", rel, err)
	}
	err = os.WriteFile(full, []byte(content), 0o644)
	if err != nil {
		t.Fatalf("failed to write %s: %v", rel, err)
	}
}

// packageByImportPath returns the parsed workspace package for importPath.
func packageByImportPath(t *testing.T, ws *types.Workspace, importPath string) *types.Package {
	t.Helper()
	fsPath, ok := ws.ImportToPath[importPath]
	if !ok {
		t.Fatalf("expected %s to be in ImportToPath", importPath)
	}
	pkg, ok := ws.Packages[fsPath]
	if !ok {
		t.Fatalf("expected package %s to be parsed", importPath)
	}
	return pkg
}

// funcDefNamed returns the go/types function declared as name in pkg.
func funcDefNamed(t *testing.T, pkg *types.Package, name string) *gotypes.Func {
	t.Helper()
	for ident, obj := range pkg.TypesInfo.Defs {
		if ident.Name != name {
			continue
		}
		fn, ok := obj.(*gotypes.Func)
		if ok {
			return fn
		}
	}
	t.Fatalf("expected %s to resolve to a go/types object", name)
	return nil
}

func loadWithConfig(t *testing.T, root string, cfg BuildConfig) (*GoParser, *types.Workspace) {
	t.Helper()
	set, err := LoadWorkspaceSet(root, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return set.Primary().Parser, set.Primary().Workspace
}

// parseWorkspace loads root under the host configuration and returns the
// primary view's parser and workspace.
func parseWorkspace(root string) (*GoParser, *types.Workspace, error) {
	set, err := LoadWorkspaceSet(root, DefaultBuildConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return nil, nil, fmt.Errorf("parse workspace: %w", err)
	}
	return set.Primary().Parser, set.Primary().Workspace, nil
}

func constrainedWorkspace(t *testing.T) string {
	return writeWorkspace(t, map[string]string{
		"p/p.go":             "package p\n",
		"p/x_fts5_test.go":   fts5TestSrc,
		"p/x_nofts5_test.go": nofts5TestSrc,
		"p/y_windows.go":     "package p\n\nfunc OnlyWindows() {}\n",
	})
}

func fileNames(m map[string]*types.File) []string {
	var names []string
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func TestParseWorkspace_ExcludesConstrainedFilesAndTypeChecks(t *testing.T) {
	root := constrainedWorkspace(t)
	p, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false))
	pkg := ws.Packages[filepath.Join(root, "p")]
	if pkg == nil {
		t.Fatal("package p not loaded")
	}
	got := fileNames(pkg.TestFiles)
	if !slices.Equal(got, []string{"x_nofts5_test.go"}) {
		t.Errorf("TestFiles = %v", got)
	}
	got = fileNames(pkg.Files)
	if !slices.Equal(got, []string{"p.go"}) {
		t.Errorf("Files = %v", got)
	}
	p.EnsureTypeChecked(ws, pkg)
	if pkg.TypesErr != nil {
		t.Errorf("TypesErr = %v", pkg.TypesErr)
	}
	if len(pkg.ExcludedTestFiles) > 0 {
		t.Errorf("test files excluded from type-check: %v", pkg.ExcludedTestFiles)
	}
}

func TestParseWorkspace_TagSelectsConstrainedTestFile(t *testing.T) {
	root := constrainedWorkspace(t)
	_, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false, "fts5"))
	pkg := ws.Packages[filepath.Join(root, "p")]
	got := fileNames(pkg.TestFiles)
	if !slices.Equal(got, []string{"x_fts5_test.go"}) {
		t.Errorf("TestFiles = %v", got)
	}
}

func TestParseWorkspace_GOOSSelectsSuffixedFile(t *testing.T) {
	root := constrainedWorkspace(t)
	_, ws := loadWithConfig(t, root, NewBuildConfig("windows", "amd64", false))
	pkg := ws.Packages[filepath.Join(root, "p")]
	_, ok := pkg.Files["y_windows.go"]
	if !ok {
		t.Errorf("Files = %v, want y_windows.go", fileNames(pkg.Files))
	}
}

func TestParseWorkspace_LegacyPlusBuildIgnoreExcluded(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":   "package p\n",
		"p/gen.go": "// +build ignore\n\npackage main\n",
	})
	_, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false))
	pkg := ws.Packages[filepath.Join(root, "p")]
	got := fileNames(pkg.Files)
	if !slices.Equal(got, []string{"p.go"}) {
		t.Errorf("Files = %v", got)
	}
}

func TestParseWorkspace_ImportCRequiresCgo(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go": "package p\n",
		"p/c.go": "package p\n\n// int x;\nimport \"C\"\n",
	})
	_, off := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false))
	got := fileNames(off.Packages[filepath.Join(root, "p")].Files)
	if !slices.Equal(got, []string{"p.go"}) {
		t.Errorf("cgo off: Files = %v", got)
	}
	_, on := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", true))
	got = fileNames(on.Packages[filepath.Join(root, "p")].Files)
	if !slices.Equal(got, []string{"c.go", "p.go"}) {
		t.Errorf("cgo on: Files = %v", got)
	}
}

func TestParseWorkspace_UnparsableFileDoesNotDropPackage(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":   "package p\n",
		"p/bad.go": "package p\n\nfunc (\n",
	})
	p, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false))
	pkg := ws.Packages[filepath.Join(root, "p")]
	if pkg == nil {
		t.Fatal("package p dropped")
	}
	got := fileNames(pkg.Files)
	if !slices.Equal(got, []string{"p.go"}) {
		t.Errorf("Files = %v", got)
	}
	bad := p.sources.File(filepath.Join(root, "p", "bad.go"))
	if bad == nil || bad.Err == nil {
		t.Errorf("bad.go record = %+v, want non-nil Err", bad)
	}
}

func TestParseWorkspace_FullyConstrainedDirectoryIsNotAPackage(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":           "package p\n",
		"win/w_windows.go": "package win\n",
	})
	p, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false))
	_, ok := ws.Packages[filepath.Join(root, "win")]
	if ok {
		t.Error("win loaded as a package")
	}
	_, ok = ws.Packages[filepath.Join(root, "p")]
	if !ok {
		t.Error("p not loaded")
	}
	if p.sources.File(filepath.Join(root, "win", "w_windows.go")) == nil {
		t.Error("constrained file missing from source tree")
	}
}

func TestParsePackage_AllUnparsableYieldsNoPackage(t *testing.T) {
	root := writeWorkspace(t, map[string]string{"p/bad.go": "package p\n\nfunc (\n"})
	p := NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	pkg, err := p.ParsePackage(filepath.Join(root, "p"))
	if pkg != nil || !errors.Is(err, ErrNoPackage) {
		t.Errorf("ParsePackage = %v, %v; want nil, ErrNoPackage", pkg, err)
	}
}

const cgoSrc = `package main

/*
#include <stdio.h>
*/
import "C"

func viaC() {
	C.puts(C.CString("x"))
}

func plain() {}

func main() {
	plain()
}
`

func TestTypeCheck_CgoFilePackageIsTyped(t *testing.T) {
	root := writeWorkspace(t, map[string]string{"main.go": cgoSrc})
	p, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", true))
	pkg := ws.Packages[root]
	if pkg == nil {
		t.Fatal("root package not loaded")
	}
	p.EnsureTypeChecked(ws, pkg)
	if pkg.TypesErr != nil {
		t.Fatalf("TypesErr = %v", pkg.TypesErr)
	}
	if pkg.TypesInfo == nil {
		t.Fatal("TypesInfo is nil")
	}
	var plainFunc, putsUse bool
	for id, obj := range pkg.TypesInfo.Uses {
		_, ok := obj.(*gotypes.Func)
		if ok && id.Name == "plain" {
			plainFunc = true
		}
		if id.Name == "puts" || id.Name == "CString" {
			putsUse = true
		}
	}
	if !plainFunc {
		t.Error("no use of plain resolved to *types.Func")
	}
	if putsUse {
		t.Error("C.puts / C.CString selectors have entries in Uses")
	}
}

func TestTypeCheck_CgoPackageKeepsInPackageTestFiles(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"main.go":      cgoSrc,
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestPlain(t *testing.T) { plain() }\n",
	})
	p, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", true))
	pkg := ws.Packages[root]
	p.EnsureTypeChecked(ws, pkg)
	if len(pkg.ExcludedTestFiles) > 0 {
		t.Errorf("test files excluded from type-check: %v", pkg.ExcludedTestFiles)
	}
}

const (
	brokenImportTestSrc = "package p\n\nimport _ \"example.com/missing/dep\"\n"
	callsFTestSrc       = "package p\n\nfunc useF() { F() }\n"
)

// checkedDir loads root under linux/amd64 and type-checks the package in rel.
func checkedDir(t *testing.T, root, rel string) (*GoParser, *types.Workspace, *types.Package) {
	t.Helper()
	p, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false))
	pkg := ws.Packages[filepath.Join(root, rel)]
	if pkg == nil {
		t.Fatalf("package %s not loaded", rel)
	}
	p.EnsureTypeChecked(ws, pkg)
	return p, ws, pkg
}

func excludedNames(pkg *types.Package) []string {
	return slices.Sorted(maps.Keys(pkg.ExcludedTestFiles))
}

// usesIn reports whether TypesInfo.Uses holds an identifier named name in file.
func usesIn(pkg *types.Package, file, name string) bool {
	f := pkg.TestFiles[file]
	for id := range pkg.TypesInfo.Uses {
		if id.Name == name && f.AST.FileStart <= id.Pos() && id.Pos() < f.AST.FileEnd {
			return true
		}
	}
	return false
}

func assertErrorIn(t *testing.T, ws *types.Workspace, pkg *types.Package, file string) {
	t.Helper()
	var terr gotypes.Error
	if !errors.As(pkg.ExcludedTestFiles[file], &terr) {
		t.Fatalf("%s: recorded error %v is not a types.Error", file, pkg.ExcludedTestFiles[file])
	}
	got := filepath.Base(ws.FileSet.Position(terr.Pos).Filename)
	if got != file {
		t.Errorf("%s: recorded error is located in %s: %v", file, got, terr)
	}
}

func TestTypeCheck_OnlyTestFileWithErrorIsExcluded(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":         "package p\n\nfunc F() {}\n",
		"p/good_test.go": callsFTestSrc,
		"p/bad_test.go":  brokenImportTestSrc,
	})
	_, ws, pkg := checkedDir(t, root, "p")

	got := excludedNames(pkg)
	if !slices.Equal(got, []string{"bad_test.go"}) {
		t.Fatalf("ExcludedTestFiles = %v, want only bad_test.go", got)
	}
	assertErrorIn(t, ws, pkg, "bad_test.go")
	if !usesIn(pkg, "good_test.go", "F") {
		t.Error("good_test.go's call of F is missing from TypesInfo.Uses")
	}
	if pkg.TypesErr != nil {
		t.Errorf("TypesErr = %v, want nil", pkg.TypesErr)
	}
	if pkg.UnattributedTestErr != nil {
		t.Errorf("UnattributedTestErr = %v, want nil", pkg.UnattributedTestErr)
	}
}

func TestTypeCheck_TestOnlyPackageIsCheckedAndExcludesBrokenTestFile(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":                   "package p\n\nfunc F() {}\n",
		"integration/good_test.go": "package integration\n\nimport \"example.com/w/p\"\n\nfunc useF() { p.F() }\n",
		"integration/bad_test.go":  "package integration\n\nimport _ \"example.com/missing/dep\"\n",
	})
	_, _, pkg := checkedDir(t, root, "integration")

	got := excludedNames(pkg)
	if !slices.Equal(got, []string{"bad_test.go"}) {
		t.Fatalf("ExcludedTestFiles = %v, want only bad_test.go", got)
	}
	if pkg.TypesPkg == nil || !usesIn(pkg, "good_test.go", "F") {
		t.Error("good_test.go's call of p.F is missing from the package's type information")
	}
}

func TestTypeCheck_ExternalTestOnlyPackageIsChecked(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":                  "package p\n\nfunc F() {}\n",
		"integration/ext_test.go": "package integration_test\n\nimport \"example.com/w/p\"\n\nfunc useF() { p.F() }\n",
	})
	_, _, pkg := checkedDir(t, root, "integration")

	if pkg.TypesPkg == nil || !usesIn(pkg, "ext_test.go", "F") {
		t.Error("ext_test.go's call of p.F is missing from the package's type information")
	}
}

func TestTypeCheck_TestFileDependingOnExcludedFileIsExcludedWithItsOwnError(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":           "package p\n\nfunc F() {}\n",
		"p/helper_test.go": "package p\n\nimport _ \"example.com/missing/dep\"\n\nfunc helper() {}\n",
		"p/user_test.go":   "package p\n\nfunc useHelper() { helper() }\n",
		"p/good_test.go":   callsFTestSrc,
	})
	_, ws, pkg := checkedDir(t, root, "p")

	got := excludedNames(pkg)
	if !slices.Equal(got, []string{"helper_test.go", "user_test.go"}) {
		t.Fatalf("ExcludedTestFiles = %v, want helper_test.go and user_test.go", got)
	}
	assertErrorIn(t, ws, pkg, "helper_test.go")
	assertErrorIn(t, ws, pkg, "user_test.go")
	if !usesIn(pkg, "good_test.go", "F") {
		t.Error("good_test.go's call of F is missing from TypesInfo.Uses")
	}
	if pkg.UnattributedTestErr != nil {
		t.Errorf("UnattributedTestErr = %v, want nil", pkg.UnattributedTestErr)
	}
}

func TestTypeCheck_ProductionFileErrorExcludesNoTestFile(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":         "package p\n\nfunc F() {}\n\nvar broken int = \"s\"\n",
		"p/good_test.go": callsFTestSrc,
	})
	_, _, pkg := checkedDir(t, root, "p")

	if len(pkg.ExcludedTestFiles) != 0 {
		t.Errorf("ExcludedTestFiles = %v, want none", excludedNames(pkg))
	}
	if pkg.TypesErr == nil {
		t.Error("TypesErr = nil, want the production file's error")
	}
	if !usesIn(pkg, "good_test.go", "F") {
		t.Error("good_test.go's call of F is missing from TypesInfo.Uses")
	}
}

func TestTypeCheck_ErrorChainLongerThanPassLimitDropsAllTestFilesUnattributed(t *testing.T) {
	files := map[string]string{
		"p/p.go":         "package p\n\nfunc F() {}\n",
		"p/t0_test.go":   "package p\n\nimport _ \"example.com/missing/dep\"\n\nfunc h0() {}\n",
		"p/good_test.go": callsFTestSrc,
	}
	for i := 1; i <= maxTestExclusionPasses+1; i++ {
		files[fmt.Sprintf("p/t%d_test.go", i)] = fmt.Sprintf("package p\n\nfunc h%d() { h%d() }\n", i, i-1)
	}
	_, ws, pkg := checkedDir(t, writeWorkspace(t, files), "p")

	if pkg.UnattributedTestErr == nil {
		t.Fatal("UnattributedTestErr = nil, want the error left after the last pass")
	}
	got, want := excludedNames(pkg), fileNames(pkg.TestFiles)
	if !slices.Equal(got, want) {
		t.Errorf("ExcludedTestFiles = %v, want every test file %v", got, want)
	}
	assertErrorIn(t, ws, pkg, "t0_test.go")
	if got := pkg.ExcludedTestFiles["good_test.go"]; got != pkg.UnattributedTestErr {
		t.Errorf("good_test.go recorded %v, want the unattributed error", got)
	}
	if usesIn(pkg, "good_test.go", "F") {
		t.Error("good_test.go is in TypesInfo.Uses although all test files were dropped")
	}
	if pkg.TypesErr != nil {
		t.Errorf("TypesErr = %v, want nil for the production files", pkg.TypesErr)
	}
}

func TestTypeCheck_ExternalTestFileWithErrorIsExcludedAndTheRestMerged(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":             "package p\n\nfunc F() {}\n",
		"p/good_ext_test.go": "package p_test\n\nimport \"example.com/w/p\"\n\nfunc useF() { p.F() }\n",
		"p/bad_ext_test.go":  "package p_test\n\nimport _ \"example.com/missing/dep\"\n",
	})
	_, ws, pkg := checkedDir(t, root, "p")

	got := excludedNames(pkg)
	if !slices.Equal(got, []string{"bad_ext_test.go"}) {
		t.Fatalf("ExcludedTestFiles = %v, want only bad_ext_test.go", got)
	}
	assertErrorIn(t, ws, pkg, "bad_ext_test.go")
	if !usesIn(pkg, "good_ext_test.go", "F") {
		t.Error("good_ext_test.go's call of p.F is missing from TypesInfo.Uses")
	}
	if pkg.UnattributedTestErr != nil {
		t.Errorf("UnattributedTestErr = %v, want nil", pkg.UnattributedTestErr)
	}
}

func TestTypeCheck_HealthyPackageIsCheckedInOnePass(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":         "package p\n\nfunc F() {}\n",
		"p/good_test.go": callsFTestSrc,
	})
	p, ws := loadWithConfig(t, root, NewBuildConfig("linux", "amd64", false))
	pkg := ws.Packages[filepath.Join(root, "p")]
	passes := 0
	p.checkPassHook = func(*types.Package) { passes++ }
	p.EnsureTypeChecked(ws, pkg)

	if passes != 1 {
		t.Errorf("check passes = %d, want 1", passes)
	}
	if pkg.ExcludedTestFiles != nil || pkg.UnattributedTestErr != nil {
		t.Errorf("exclusion record = %v / %v, want none", pkg.ExcludedTestFiles, pkg.UnattributedTestErr)
	}
}

func TestTypeCheck_FixedTestFileIsNoLongerExcludedAfterInvalidation(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":         "package p\n\nfunc F() {}\n",
		"p/good_test.go": callsFTestSrc,
		"p/bad_test.go":  brokenImportTestSrc,
	})
	set, err := LoadWorkspaceSet(root, NewBuildConfig("linux", "amd64", false), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	p, ws := set.Primary().Parser, set.Primary().Workspace
	pkg := ws.Packages[filepath.Join(root, "p")]
	p.EnsureTypeChecked(ws, pkg)
	if len(pkg.ExcludedTestFiles) != 1 {
		t.Fatalf("ExcludedTestFiles = %v, want bad_test.go", excludedNames(pkg))
	}

	p.InvalidateTypeCheck(pkg)
	if pkg.ExcludedTestFiles != nil || pkg.UnattributedTestErr != nil {
		t.Fatalf("exclusion record survived invalidation: %v / %v", pkg.ExcludedTestFiles, pkg.UnattributedTestErr)
	}

	bad := filepath.Join(root, "p", "bad_test.go")
	if err := os.WriteFile(bad, []byte("package p\n\nfunc alsoUseF() { F() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := set.SyncSource(bad); err != nil {
		t.Fatal(err)
	}
	p.ProjectFile(pkg, bad)
	p.InvalidateTypeCheck(pkg)
	p.EnsureTypeChecked(ws, pkg)

	if len(pkg.ExcludedTestFiles) != 0 {
		t.Errorf("ExcludedTestFiles = %v, want none", excludedNames(pkg))
	}
	if !usesIn(pkg, "bad_test.go", "F") {
		t.Error("the fixed bad_test.go's call of F is missing from TypesInfo.Uses")
	}
}

// assertCycleReported checks that pkg was checked despite a production import
// cycle and reports it, naming every import path in want.
func assertCycleReported(t *testing.T, pkg *types.Package, fn string, want ...string) {
	t.Helper()
	if !pkg.TypesChecked || pkg.TypesInfo == nil {
		t.Fatalf("%s: TypesChecked=%v TypesInfo nil=%v", pkg.ImportPath, pkg.TypesChecked, pkg.TypesInfo == nil)
	}
	defined := false
	for id := range pkg.TypesInfo.Defs {
		if id.Name == fn {
			defined = true
		}
	}
	if !defined {
		t.Errorf("%s: Defs has no entry for %s", pkg.ImportPath, fn)
	}
	var msg string
	for _, err := range pkg.ProductionErrs {
		msg += err.Error() + "\n"
	}
	if !strings.Contains(msg, "import cycle not allowed") {
		t.Fatalf("%s: ProductionErrs = %q, want an import cycle error", pkg.ImportPath, msg)
	}
	for _, path := range want {
		if !strings.Contains(msg, path) {
			t.Errorf("%s: ProductionErrs = %q, want %s named", pkg.ImportPath, msg, path)
		}
	}
}

func TestParser_TypeCheckPackage_ProductionCycleIsReportedNotRecursed(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/w/b\"\n\nfunc FA() { b.FB() }\n",
		"b/b.go": "package b\n\nimport \"example.com/w/a\"\n\nfunc FB() { a.FA() }\n",
	})
	p, ws := loadWithConfig(t, root, DefaultBuildConfig())
	pkgA, pkgB := ws.Packages[filepath.Join(root, "a")], ws.Packages[filepath.Join(root, "b")]

	p.TypeCheckPackage(ws, pkgA)
	p.TypeCheckPackage(ws, pkgB)

	assertCycleReported(t, pkgA, "FA", "example.com/w/a", "example.com/w/b")
	assertCycleReported(t, pkgB, "FB", "example.com/w/a", "example.com/w/b")
}

func TestParser_TypeCheckPackage_ProductionSelfImportIsReportedNotRecursed(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/w/a\"\n\nfunc FA() { a.FA() }\n",
	})
	p, ws := loadWithConfig(t, root, DefaultBuildConfig())
	pkgA := ws.Packages[filepath.Join(root, "a")]

	p.TypeCheckPackage(ws, pkgA)

	assertCycleReported(t, pkgA, "FA", "example.com/w/a -> example.com/w/a")
}

func TestParser_TypeCheckPackage_TestFileSelfImportTerminates(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"in-package test file": {
			"a/a.go":      "package a\n\nfunc FA() {}\n",
			"a/a_test.go": "package a\n\nimport \"example.com/w/a\"\n\nfunc TestA() { a.FA() }\n",
		},
		"external test package": {
			"a/a.go":      "package a\n\nfunc FA() {}\n",
			"a/a_test.go": "package a_test\n\nimport \"example.com/w/a\"\n\nfunc TestA() { a.FA() }\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := writeWorkspace(t, files)
			p, ws := loadWithConfig(t, root, DefaultBuildConfig())
			pkgA := ws.Packages[filepath.Join(root, "a")]

			p.TypeCheckPackage(ws, pkgA)

			if !pkgA.TypesChecked {
				t.Fatal("package not checked")
			}
		})
	}
}

func TestParser_TypeCheckPackage_ThreePackageProductionCycleNamesEveryMember(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/w/b\"\n\nfunc FA() { b.FB() }\n",
		"b/b.go": "package b\n\nimport \"example.com/w/c\"\n\nfunc FB() { c.FC() }\n",
		"c/c.go": "package c\n\nimport \"example.com/w/a\"\n\nfunc FC() { a.FA() }\n",
	})
	p, ws := loadWithConfig(t, root, DefaultBuildConfig())
	pkgA, pkgB, pkgC := ws.Packages[filepath.Join(root, "a")], ws.Packages[filepath.Join(root, "b")], ws.Packages[filepath.Join(root, "c")]

	p.TypeCheckPackage(ws, pkgA)
	p.TypeCheckPackage(ws, pkgB)
	p.TypeCheckPackage(ws, pkgC)

	all := []string{"example.com/w/a", "example.com/w/b", "example.com/w/c"}
	assertCycleReported(t, pkgA, "FA", all...)
	assertCycleReported(t, pkgB, "FB", all...)
	assertCycleReported(t, pkgC, "FC", all...)
}

func TestParser_InvalidateTypeCheck_CycleIntroducedAfterLoadIsReported(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"a/a.go": "package a\n\nfunc FA() {}\n",
		"b/b.go": "package b\n\nimport \"example.com/w/a\"\n\nfunc FB() { a.FA() }\n",
	})
	set, err := LoadWorkspaceSet(root, DefaultBuildConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	p, ws := set.Primary().Parser, set.Primary().Workspace
	pkgA, pkgB := ws.Packages[filepath.Join(root, "a")], ws.Packages[filepath.Join(root, "b")]
	p.EnsureTypeChecked(ws, pkgB)

	file := filepath.Join(root, "a", "a.go")
	if err := os.WriteFile(file, []byte("package a\n\nimport \"example.com/w/b\"\n\nfunc FA() { b.FB() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := set.SyncSource(file); err != nil {
		t.Fatal(err)
	}
	p.ProjectFile(pkgA, file)
	p.InvalidateTypeCheck(pkgA)
	p.InvalidateTypeCheck(pkgB)
	p.TypeCheckPackage(ws, pkgA)
	p.TypeCheckPackage(ws, pkgB)

	assertCycleReported(t, pkgA, "FA", "example.com/w/a", "example.com/w/b")
	assertCycleReported(t, pkgB, "FB", "example.com/w/a", "example.com/w/b")
}

func TestParseWorkspace_SkipsTestdataAndUnderscoreDirs(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"a/a.go":            "package a\n",
		"a/testdata/x/x.go": "package x\n",
		"_skip/y/y.go":      "package y\n",
		"b/_gen/z.go":       "package z\n",
		"testdata/top/t.go": "package top\n",
	})
	_, ws, err := parseWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for path := range ws.Packages {
		rel, _ := filepath.Rel(root, path)
		got = append(got, rel)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"a"}) {
		t.Errorf("packages = %v, want [a]", got)
	}
}

func TestInSkippedDir_RootIsExempt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "testdata", "_root")
	if InSkippedDir(root, root) {
		t.Error("root itself must not be skipped")
	}
	if !InSkippedDir(root, filepath.Join(root, "a", "testdata")) {
		t.Error("testdata below root must be skipped")
	}
	if InSkippedDir(root, filepath.Join(root, "a", "b")) {
		t.Error("plain dir must not be skipped")
	}
}

func TestSyncSource_UntrackedAndRemovedPathsYieldSentinels(t *testing.T) {
	root := writeWorkspace(t, map[string]string{
		"p/p.go":        "package p\n",
		"testdata/t.go": "package t\n",
		"p/notes.txt":   "x",
		"p/gone.go":     "package p\n",
	})
	set, err := LoadWorkspaceSet(root, DefaultBuildConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "p", "gone.go")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]error{
		filepath.Join(root, "p", "notes.txt"):   ErrSkippedSource,
		filepath.Join(root, "testdata", "t.go"): ErrSkippedSource,
		filepath.Join(root, "p", "gone.go"):     ErrSourceRemoved,
	} {
		src, err := set.SyncSource(path)
		if src != nil || !errors.Is(err, want) {
			t.Errorf("SyncSource(%s) = %v, %v; want nil, %v", filepath.Base(path), src, err, want)
		}
	}
}
