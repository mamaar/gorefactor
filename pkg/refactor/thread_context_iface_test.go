package refactor

import (
	gotypes "go/types"
	"slices"
	"strings"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// methodLabel renders a func object as "MemStore.Save" / "Store.Save" / "use".
// gainingNames is not usable for interface tests: every implementation and the
// interface method itself share the bare name "Save", so an assertion built on
// it could not tell the three apart — which is precisely what these tests exist
// to check.
func methodLabel(f *gotypes.Func) string {
	sig, ok := f.Type().(*gotypes.Signature)
	if !ok || sig.Recv() == nil {
		return f.Name()
	}

	t := sig.Recv().Type()
	if p, ok := t.(*gotypes.Pointer); ok {
		t = p.Elem()
	}

	if n, ok := t.(*gotypes.Named); ok {
		return n.Obj().Name() + "." + f.Name()
	}

	return f.Name()
}

func gainingLabels(plan *threadPlan) []string {
	var out []string
	for f := range plan.Funcs {
		out = append(out, methodLabel(f))
	}

	slices.Sort(out)

	return out
}

// storeFiles is the canonical happy-path workspace: one workspace interface,
// two workspace implementations, a caller of a concrete impl and a caller that
// goes THROUGH the interface.
const storeFiles = `package store

type Store interface {
	Save(x int)
}

type MemStore struct{}

func (m *MemStore) Save(x int) {}

type FileStore struct{}

func (f *FileStore) Save(x int) {}

func useMem(m *MemStore) { m.Save(1) }

func useIface(s Store) { s.Save(2) }
`

func TestThreadContext_ExpandsWorkspaceInterface(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"store/store.go": storeFiles,
	}, "store/store.go", "MemStore.Save")

	// Seeding one implementation must pull in the interface method AND the
	// sibling implementation: they are a unit, and rewriting any one alone stops
	// the type satisfying the interface.
	assertNames(t, gainingLabels(plan), []string{
		"FileStore.Save", "MemStore.Save", "Store.Save", "useIface", "useMem",
	}, "Funcs")

	got := argIn(t, plan, "useMem")
	if got != "ctx" {
		t.Errorf("useMem must pass its injected ctx, got %q", got)
	}
	// The call through the interface is only reachable via the interface method
	// object; it is the whole reason expandInterfaces returns it.
	got = argIn(t, plan, "useIface")
	if got != "ctx" {
		t.Errorf("useIface must pass its injected ctx, got %q", got)
	}
}

// TestThreadContext_InterfaceMethodDeclIsRepresentable pins the representation
// the rendering half depends on: an interface method resolves to a funcDecl with
// a nil FuncDecl but a usable FuncType (whose Params.Opening is the insertion
// point) and a real owning File.
func TestThreadContext_InterfaceMethodDeclIsRepresentable(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"store/store.go": storeFiles,
	}, "store/store.go", "MemStore.Save")

	var ifaceMethod *funcDecl

	for f, fd := range plan.Decls {
		if methodLabel(f) == "Store.Save" {
			ifaceMethod = fd
		}
	}

	if ifaceMethod == nil {
		t.Fatalf("Store.Save has no entry in plan.Decls")
	}

	if !ifaceMethod.IsInterfaceMethod() {
		t.Fatalf("Store.Save must be recorded as an interface method")
	}

	if ifaceMethod.Decl != nil {
		t.Errorf("an interface method has no *ast.FuncDecl; got %v", ifaceMethod.Decl)
	}

	if ifaceMethod.FuncType() == nil || ifaceMethod.FuncType().Params == nil {
		t.Fatalf("render needs a FuncType with Params to insert into")
	}

	if !ifaceMethod.FuncType().Params.Opening.IsValid() {
		t.Errorf("Params.Opening must be a valid insertion position")
	}

	if ifaceMethod.File == nil || !strings.HasSuffix(ifaceMethod.File.Path, "store.go") {
		t.Errorf("render needs the owning file, got %v", ifaceMethod.File)
	}
	// No body means no locals to collide with, so the plain name is right — and
	// uniqueCtxName must not choke on the nil *ast.FuncDecl.
	for f, name := range plan.Funcs {
		if methodLabel(f) == "Store.Save" && name != "ctx" {
			t.Errorf("interface method should take the plain name ctx, got %q", name)
		}
	}
}

func TestThreadContext_SeedIsInterfaceMethod(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"store/store.go": storeFiles,
	}, "store/store.go", "Store.Save")

	assertNames(t, gainingLabels(plan), []string{
		"FileStore.Save", "MemStore.Save", "Store.Save", "useIface", "useMem",
	}, "Funcs")
}

// TestThreadContext_InterfaceAcrossPackages proves the expansion is not
// accidentally scoped to the seed's own package.
func TestThreadContext_InterfaceAcrossPackages(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"store/store.go": `package store

type Store interface {
	Save(x int)
}
`,
		"mem/mem.go": `package mem

type MemStore struct{}

func (m *MemStore) Save(x int) {}
`,
		"app/app.go": `package app

import (
	"example.com/m/mem"
	"example.com/m/store"
)

func run() {
	var s store.Store = &mem.MemStore{}
	s.Save(1)
}
`,
	}, "mem/mem.go", "MemStore.Save")

	assertNames(t, gainingLabels(plan), []string{
		"MemStore.Save", "Store.Save", "run",
	}, "Funcs")
}

func TestThreadContext_AbortsOnExternalInterface_HTTPHandler(t *testing.T) {
	err := threadErrFor(t, map[string]string{
		"srv/srv.go": `package srv

import "net/http"

type H struct{}

func (h *H) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

var _ http.Handler = &H{}
`,
	}, "srv/srv.go", "H.ServeHTTP", true)

	if !strings.Contains(err.Error(), "net/http.Handler") {
		t.Fatalf("error must name the external interface by package path, got: %v", err)
	}

	if !strings.Contains(err.Error(), "ServeHTTP") {
		t.Fatalf("error must name the offending method, got: %v", err)
	}

	if !strings.Contains(err.Error(), "the seed itself") {
		t.Fatalf("a seed-level abort must say so rather than blaming a reached function, got: %v", err)
	}

	if !strings.Contains(err.Error(), "No changes were made") {
		t.Fatalf("error must state that nothing was mutated, got: %v", err)
	}
}

// TestThreadContext_AbortLeavesWorkspaceUntouched asserts the abort happens
// before any mutation: no plan survives, so nothing downstream can render.
func TestThreadContext_AbortLeavesWorkspaceUntouched(t *testing.T) {
	files := map[string]string{
		"srv/srv.go": `package srv

import "net/http"

type H struct{}

func (h *H) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

var _ http.Handler = &H{}
`,
	}
	ws := buildScopeWorkspace(t, files)
	before := string(ws.Packages[findPkgPath(t, ws, "srv")].Files[srvFileKey(t, ws)].OriginalContent)

	op := &ThreadContextOperation{FunctionName: "H.ServeHTTP", SourceFile: "srv/srv.go"}

	plan, err := op.Execute(ws)
	if err == nil {
		t.Fatalf("expected abort")
	}

	if plan != nil {
		t.Errorf("an aborting Execute must return no plan, got %v", plan)
	}

	if op.plan != nil {
		t.Errorf("an aborting Execute must leave no plan-of-record, got %v", op.plan)
	}

	after := string(ws.Packages[findPkgPath(t, ws, "srv")].Files[srvFileKey(t, ws)].OriginalContent)
	if before != after {
		t.Errorf("the abort mutated file content")
	}
}

func findPkgPath(t *testing.T, ws *pkgtypes.Workspace, name string) string {
	t.Helper()

	for path, pkg := range ws.Packages {
		if pkg.Name == name {
			return path
		}
	}

	t.Fatalf("package %q not found", name)

	return ""
}

func srvFileKey(t *testing.T, ws *pkgtypes.Workspace) string {
	t.Helper()

	for _, pkg := range ws.Packages {
		if pkg.Name != "srv" {
			continue
		}

		for key := range pkg.Files {
			return key
		}
	}

	t.Fatalf("srv file not found")

	return ""
}

// TestThreadContext_AbortsOnExternalInterface_Transitive covers the case the
// seed-only check would miss: the offending method is pulled into the worklist
// by the climb, not named by the user.
func TestThreadContext_AbortsOnExternalInterface_Transitive(t *testing.T) {
	err := threadErrFor(t, map[string]string{
		"w/w.go": `package w

import "io"

type Sink struct{}

func (s *Sink) Write(p []byte) (int, error) {
	target()
	return len(p), nil
}

var _ io.Writer = &Sink{}

func target() {}
`,
	}, "w/w.go", "target", true)

	if !strings.Contains(err.Error(), "io.Writer") {
		t.Fatalf("error must name io.Writer, got: %v", err)
	}

	if !strings.Contains(err.Error(), "Write") {
		t.Fatalf("error must name the offending method, got: %v", err)
	}

	if strings.Contains(err.Error(), "the seed itself") {
		t.Fatalf("a transitively-reached offender must not be reported as the seed, got: %v", err)
	}
}

// TestThreadContext_AbortsOnExternalInterface_ValueReceiver is the case a naive
// Implements(T, iface) misses, and it is fussier to construct than it looks.
//
// Len has a VALUE receiver, so it is in the method set of both Items and *Items
// — but Less and Swap take POINTER receivers, so only *Items satisfies
// sort.Interface. Implements(Items, sort.Interface) is therefore FALSE while
// Implements(*Items, sort.Interface) is TRUE: without the pointer test the
// satisfaction that threading Len would break is invisible.
//
// sort.Interface (not io.Writer) is essential here. A value-receiver Write makes
// Implements(Items, io.Writer) true on its own, so io.Writer would fire the
// abort via the plain test and the pointer path would never be exercised — the
// test would pass while asserting nothing. sort.Interface is the only exported
// interface of the only imported package, and Items does not satisfy it by
// value, so the abort can ONLY come from the pointer test.
func TestThreadContext_AbortsOnExternalInterface_ValueReceiver(t *testing.T) {
	files := map[string]string{
		"s/s.go": `package s

import "sort"

type Items struct{ v []int }

func (it Items) Len() int { return len(it.v) }

func (it *Items) Less(i, j int) bool { return it.v[i] < it.v[j] }

func (it *Items) Swap(i, j int) { it.v[i], it.v[j] = it.v[j], it.v[i] }

var _ sort.Interface = &Items{}
`,
	}

	// Guard the guard: if either precondition broke, the test below would pass
	// for the wrong reason and silently stop covering the pointer path.
	ws := buildScopeWorkspace(t, files)
	pkg := findPackageByName(t, ws, "s")
	items := pkg.TypesPkg.Scope().Lookup("Items").Type()
	var sortIface *gotypes.Interface

	for _, imp := range pkg.TypesPkg.Imports() {
		if imp.Path() == "sort" {
			sortIface = imp.Scope().Lookup("Interface").Type().Underlying().(*gotypes.Interface)
		}
	}

	if sortIface == nil {
		t.Fatalf("could not resolve sort.Interface")
	}

	if gotypes.Implements(items, sortIface) {
		t.Fatalf("precondition broken: Items must NOT satisfy sort.Interface by value")
	}

	if !gotypes.Implements(gotypes.NewPointer(items), sortIface) {
		t.Fatalf("precondition broken: *Items must satisfy sort.Interface")
	}

	err := threadErrFor(t, files, "s/s.go", "Items.Len", true)
	if !strings.Contains(err.Error(), "sort.Interface") {
		t.Fatalf("a value-receiver method satisfying an external interface only via the pointer "+
			"method set must still abort, naming sort.Interface; got: %v", err)
	}

	if !strings.Contains(err.Error(), "Len") {
		t.Fatalf("error must name the offending method, got: %v", err)
	}
}

// TestThreadContext_AbortsOnExternalInterface_Stringer is the case that the
// type-info scan alone cannot see, and the sole reason the survey also walks
// every direct import's exported interfaces.
//
// Nothing in this workspace ever mentions fmt.Stringer: `fmt.Println(t.String())`
// has the signature func(...any) (int, error), so walking the types the code
// actually mentions reaches `any` and `error` and stops. Stringer is reachable
// only by enumerating what the imported package DECLARES. Threading String()
// would silently change how every %v of this type prints.
func TestThreadContext_AbortsOnExternalInterface_Stringer(t *testing.T) {
	err := threadErrFor(t, map[string]string{
		"p/p.go": `package p

import "fmt"

type Temp struct{ c int }

func (t Temp) String() string { return "temp" }

func show(t Temp) { fmt.Println(t.String()) }
`,
	}, "p/p.go", "Temp.String", true)

	if !strings.Contains(err.Error(), "fmt.Stringer") {
		t.Fatalf("an interface satisfied but never mentioned must still abort, got: %v", err)
	}

	if !strings.Contains(err.Error(), "String") {
		t.Fatalf("error must name the offending method, got: %v", err)
	}
}

// TestThreadContext_NoAbortForWorkspaceOnlyInterface is the regression guard on
// the happy path: the external-interface check must not fire for an interface we
// own, even when the workspace imports packages full of interfaces.
func TestThreadContext_NoAbortForWorkspaceOnlyInterface(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"store/store.go": `package store

import "context"

type Store interface {
	Save(x int)
}

type MemStore struct{}

func (m *MemStore) Save(x int) {}

func run(ctx context.Context, s Store) { s.Save(1) }
`,
	}, "store/store.go", "MemStore.Save")

	assertNames(t, gainingLabels(plan), []string{"MemStore.Save", "Store.Save"}, "Funcs")

	got := argIn(t, plan, "run")
	if got != "ctx" {
		t.Errorf("run already has a ctx and must pass it, got %q", got)
	}
}

// TestThreadContext_PlainFunctionUnaffectedByExternalInterfaces guards against
// the check firing on a non-method whose NAME happens to match an external
// interface's method.
func TestThreadContext_PlainFunctionUnaffectedByExternalInterfaces(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"w/w.go": `package w

import "io"

var _ io.Writer = (*Sink)(nil)

type Sink struct{}

func (s *Sink) Write(p []byte) (int, error) { return len(p), nil }

func Read(p []byte) {}

func caller() { Read(nil) }
`,
	}, "w/w.go", "Read")

	// Read is a plain function, not a method: it satisfies nothing.
	assertNames(t, gainingLabels(plan), []string{"Read", "caller"}, "Funcs")
}

func TestThreadContext_MissingDeclarationSaysWhatWasLookedUpAndWhere(t *testing.T) {
	err := threadErrFor(t, map[string]string{
		"store/store.go": `package store

type MemStore struct{}

func (m *MemStore) Save(x int) {}

func use() {
	type saver interface {
		Save(x int)
	}
	var s saver = &MemStore{}
	s.Save(1)
}
`,
	}, "store/store.go", "MemStore.Save", true)
	if err == nil {
		t.Fatal("want an error for an interface declared inside a function")
	}

	for _, want := range []string{
		"saver.Save",
		"store/store.go:9",
		"package example.com/m/store",
		"defines no package-level function, method or interface method there",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q, got: %v", want, err)
		}
	}

	if strings.Contains(err.Error(), "may have failed to type-check") {
		t.Errorf("error must not guess at a failed type-check, got: %v", err)
	}
}

// IsInterfaceMethod reports whether this declaration is an interface method
// field rather than an ordinary function declaration.
func (fd *funcDecl) IsInterfaceMethod() bool { return fd.Decl == nil && fd.IfaceFuncType != nil }
