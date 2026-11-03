package analysis

import (
	"io"
	"log/slog"
	"testing"
)

const (
	reasonTests      = "Only referenced from tests"
	reasonUnusedCode = "Only referenced from unused code"
	reasonReceiver   = "Receiver type is unused"
)

// typedUnusedFacts runs FindUnusedSymbols on a type-checked workspace of
// files (path -> source, relative to module "example.com/w").
func typedUnusedFacts(t *testing.T, files map[string]string, includeExported bool) map[string]unusedFact {
	t.Helper()
	set, _ := loadSet(t, files)

	v := set.Primary()
	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	}

	idx, err := v.ReferenceIndex()
	if err != nil {
		t.Fatal(err)
	}

	a := NewUnusedAnalyzer(v.Workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.SetIncludeExported(includeExported)
	a.SetIndex(idx)

	unused, err := a.FindUnusedSymbols()
	if err != nil {
		t.Fatal(err)
	}

	return unusedFacts(unused)
}

// untypedUnusedFacts runs FindUnusedSymbols on parsed, unchecked sources.
func untypedUnusedFacts(t *testing.T, files map[string]string) map[string]unusedFact {
	t.Helper()
	ws := parsedGraphWorkspace(t, files)
	a := NewUnusedAnalyzer(ws, slog.New(slog.NewTextHandler(io.Discard, nil)))

	unused, err := a.FindUnusedSymbols()
	if err != nil {
		t.Fatal(err)
	}

	return unusedFacts(unused)
}

func assertReported(t *testing.T, facts map[string]unusedFact, key, reason string) {
	t.Helper()

	got, ok := facts[key]
	if !ok {
		t.Errorf("%s not reported; got %v", key, facts)
		return
	}

	if reason != "" && got.Reason != reason {
		t.Errorf("%s reason = %q, want %q", key, got.Reason, reason)
	}
}

func assertNotReported(t *testing.T, facts map[string]unusedFact, keys ...string) {
	t.Helper()

	for _, key := range keys {
		f, ok := facts[key]
		if ok {
			t.Errorf("%s reported (%q), want it live", key, f.Reason)
		}
	}
}

func assertSafe(t *testing.T, facts map[string]unusedFact, key string, want bool) {
	t.Helper()

	f, ok := facts[key]
	if !ok {
		t.Errorf("%s not reported; got %v", key, facts)
		return
	}

	if f.SafeToDelete != want {
		t.Errorf("%s SafeToDelete = %v, want %v", key, f.SafeToDelete, want)
	}
}

const testImport = "package p\n\nimport \"testing\"\n\n"

func TestFindUnusedSymbols_FunctionCalledOnlyFromTestFileIsReportedAsOnlyReferencedFromTests(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go":      "package p\n\nfunc helper() {}\n",
		"p/a_test.go": testImport + "func TestX(t *testing.T) { helper() }\n",
	}, false)
	assertReported(t, facts, "a.go:helper", reasonTests)
}

func TestFindUnusedSymbols_ReferenceFromExternalTestPackageDoesNotCount(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc Helper() {}\n",
		"p/a_test.go": "package p_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/w/p\"\n)\n\n" +
			"func TestX(t *testing.T) { p.Helper() }\n",
	}, true)
	assertReported(t, facts, "a.go:Helper", reasonTests)
}

func TestFindUnusedSymbols_TestHelperCalledFromTestIsNotReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go":      "package p\n\nfunc keep() {}\n\nfunc main() { keep() }\n",
		"p/a_test.go": testImport + "func helper() {}\n\nfunc TestX(t *testing.T) { helper() }\n",
	}, false)
	assertNotReported(t, facts, "a_test.go:helper")
}

func TestFindUnusedSymbols_VariableInTestFileReferencedByTestIsNotReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go":      "package p\n\nfunc keep() {}\n\nfunc main() { keep() }\n",
		"p/a_test.go": testImport + "var fixture = 1\n\nfunc TestX(t *testing.T) { _ = fixture }\n",
	}, false)
	assertNotReported(t, facts, "a_test.go:fixture")
}

func TestFindUnusedSymbols_FunctionCalledOnlyByUnusedFunctionIsReportedAsOnlyReferencedFromUnusedCode(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc callee() {}\n\nfunc caller() { callee() }\n",
	}, false)
	assertReported(t, facts, "a.go:caller", "")
	assertReported(t, facts, "a.go:callee", reasonUnusedCode)
}

func TestFindUnusedSymbols_ChainBehindTestOnlyFunctionIsReportedToTheEnd(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go":      "package p\n\nfunc a() { b() }\n\nfunc b() { c() }\n\nfunc c() {}\n",
		"p/a_test.go": testImport + "func TestX(t *testing.T) { a() }\n",
	}, false)
	assertReported(t, facts, "a.go:a", reasonTests)
	assertReported(t, facts, "a.go:b", reasonUnusedCode)
	assertReported(t, facts, "a.go:c", reasonUnusedCode)
}

func TestFindUnusedSymbols_RecursiveFunctionWithoutOtherCallersIsReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc r(n int) int {\n\tif n == 0 {\n\t\treturn 0\n\t}\n\treturn r(n - 1)\n}\n",
	}, false)
	assertReported(t, facts, "a.go:r", "")
}

func TestFindUnusedSymbols_MutuallyRecursiveFunctionsWithoutOtherCallersAreBothReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc a(n int) { if n > 0 { b(n - 1) } }\n\nfunc b(n int) { if n > 0 { a(n - 1) } }\n",
	}, false)
	assertReported(t, facts, "a.go:a", reasonUnusedCode)
	assertReported(t, facts, "a.go:b", reasonUnusedCode)
}

func TestFindUnusedSymbols_PackageVarInitialiserOfUnusedVarDoesNotKeepItsCalleeAlive(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc callee() int { return 1 }\n\nvar v = callee()\n",
	}, false)
	assertReported(t, facts, "a.go:v", "")
	assertReported(t, facts, "a.go:callee", reasonUnusedCode)
}

func TestFindUnusedSymbols_TypeNamedOnlyByItsOwnMethodReceiversIsReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\ntype t struct{}\n\nfunc (t) m() {}\n",
	}, false)
	assertReported(t, facts, "a.go:t", "")
	assertReported(t, facts, "a.go:m", reasonReceiver)
}

const ifaceSource = "package p\n\ntype I interface{ run() }\n\ntype t struct{}\n\n"

func TestFindUnusedSymbols_InterfaceNamedMethodOfUnusedTypeIsReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": ifaceSource + "func (t) run() {}\n\nfunc main() { var _ I }\n",
	}, false)
	assertReported(t, facts, "a.go:t", "")
	assertReported(t, facts, "a.go:run", reasonReceiver)
}

func TestFindUnusedSymbols_InterfaceNamedMethodOfLiveTypeKeepsItsCalleesAlive(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": ifaceSource + "func (t) run() { helper() }\n\nfunc helper() {}\n\nfunc main() { var _ I = t{} }\n",
	}, false)
	assertNotReported(t, facts, "a.go:t", "a.go:run", "a.go:helper")
}

func TestFindUnusedSymbols_InterfaceUsedOnlyInSignaturesOfUnusedFunctionsIsReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\ntype iface interface{ M() }\n\nfunc f(i iface) { i.M() }\n",
	}, false)
	assertReported(t, facts, "a.go:f", "")
	assertReported(t, facts, "a.go:iface", reasonUnusedCode)
}

func TestFindUnusedSymbols_TypeUsedOnlyAsFieldTypeOfUnusedStructIsReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\ntype dep struct{}\n\ntype s struct{ d dep }\n",
	}, false)
	assertReported(t, facts, "a.go:s", "")
	assertReported(t, facts, "a.go:dep", reasonUnusedCode)
}

func TestFindUnusedSymbols_UnreportedExportedFunctionKeepsItsCalleesAlive(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc Exp() { callee() }\n\nfunc callee() {}\n",
	}, false)
	assertNotReported(t, facts, "a.go:callee", "a.go:Exp")
}

func TestFindUnusedSymbols_ExportedFunctionCalledOnlyByUnusedCodeIsReportedWhenExportedIncluded(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc Exp() {}\n\nfunc caller() { Exp() }\n",
	}, true)
	assertReported(t, facts, "a.go:Exp", reasonUnusedCode)
}

func TestFindUnusedSymbols_InitAndBlankVarInitialisersKeepTheirReferencesAlive(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\nfunc a() {}\n\nfunc b() int { return 1 }\n\nfunc init() { a() }\n\nvar _ = b()\n",
	}, false)
	assertNotReported(t, facts, "a.go:a", "a.go:b")
}

func TestFindUnusedSymbols_TestOnlyAndTransitiveFindingsAreNotSafeToDelete(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go":      "package p\n\nfunc a() { b() }\n\nfunc b() {}\n\nfunc lone() {}\n",
		"p/a_test.go": testImport + "func TestX(t *testing.T) { a() }\n",
	}, false)
	assertSafe(t, facts, "a.go:a", false)
	assertSafe(t, facts, "a.go:b", false)
	assertReported(t, facts, "a.go:lone", "")
	assertSafe(t, facts, "a.go:lone", true)
}

func TestFindUnusedSymbols_TypeWhoseOnlyReferencesAreItsOwnMethodsIsSafeToDelete(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go": "package p\n\ntype t struct{}\n\nfunc (x t) a() { x.b() }\n\nfunc (x t) b() {}\n",
	}, false)
	assertReported(t, facts, "a.go:t", "")
	assertSafe(t, facts, "a.go:t", true)
}

func TestFindUnusedSymbols_TypeWhoseMethodIsCalledFromATestIsNotSafeToDelete(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"p/a.go":      "package p\n\ntype thing struct{}\n\nfunc (x thing) m() {}\n",
		"p/a_test.go": testImport + "func TestX(t *testing.T) { var x thing; x.m() }\n",
	}, false)
	assertReported(t, facts, "a.go:thing", reasonTests)
	assertSafe(t, facts, "a.go:thing", false)
}

func TestFindUnusedSymbols_LiveMethodKeepsItsReceiverTypeAlive(t *testing.T) {
	facts := untypedUnusedFacts(t, map[string]string{
		"a.go": "package p\n\ntype T struct{}\n\nfunc (T) m() {}\n\nfunc main() {\n\tvar v interface{ m() }\n\tv.m()\n}\n",
	})
	assertNotReported(t, facts, "a.go:T", "a.go:m")
}

func TestFindUnusedSymbols_UntypedPackageFunctionCalledOnlyByUnusedFunctionIsReported(t *testing.T) {
	facts := untypedUnusedFacts(t, map[string]string{
		"a.go": "package p\n\nfunc callee() {}\n\nfunc caller() { callee() }\n",
	})
	assertReported(t, facts, "a.go:caller", "")
	assertReported(t, facts, "a.go:callee", reasonUnusedCode)
}

func TestFindUnusedSymbols_RequesterShapeBehindTestOnlyExportedFunctionIsReportedWhileLiveFunctionIsNot(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"common/ags.go": `package common

type AgsResponse interface{ AgsError() error }

type doAgsRequester struct{ c int }

func NewDoAgsRequester(c int) *doAgsRequester { return &doAgsRequester{c: c} }

func (d *doAgsRequester) Do(obj AgsResponse) error { return obj.AgsError() }

var DefaultDoAgsRequester = NewDoAgsRequester(1)

func DoAgsRequest(obj AgsResponse) error { return DefaultDoAgsRequester.Do(obj) }

func DoRequest() error { return nil }
`,
		"common/ags_test.go": "package common\n\nimport \"testing\"\n\n" + `type stub struct{}

func (stub) AgsError() error { return nil }

func TestDoAgsRequest(t *testing.T) { _ = DoAgsRequest(stub{}) }
`,
		"fetch/fetch.go": "package fetch\n\nimport \"example.com/w/common\"\n\nfunc Fetch() error { return common.DoRequest() }\n\nfunc init() { _ = Fetch() }\n",
	}, true)

	assertReported(t, facts, "ags.go:DoAgsRequest", reasonTests)

	for _, key := range []string{"DefaultDoAgsRequester", "NewDoAgsRequester", "doAgsRequester", "Do", "AgsResponse"} {
		assertReported(t, facts, "ags.go:"+key, reasonUnusedCode)
	}

	assertNotReported(t, facts, "ags.go:DoRequest")
}

func TestFindUnusedSymbols_TypeWhoseMethodIsCalledByUnusedCodeIsNotSafeToDelete(t *testing.T) {
	facts := untypedUnusedFacts(t, map[string]string{
		"a.go": "package p\n\ntype thing struct{}\n\nfunc (thing) m() {}\n\nfunc f() {\n\tvar v interface{ m() }\n\tv.m()\n}\n",
	})
	assertReported(t, facts, "a.go:thing", reasonUnusedCode)
	assertSafe(t, facts, "a.go:thing", false)
}

const embeddingSource = "package p\n\ntype base struct{}\n\nfunc (base) Close() error { return leaf() }\n\nfunc leaf() error { return nil }\n\n" +
	"type Impl struct{ base }\n\nfunc Run() any { return Impl{} }\n\nfunc main() { _ = Run() }\n"

func TestFindUnusedSymbols_TypeUsedOnlyByEmbeddingIsNotReported(t *testing.T) {
	files := map[string]string{"p/a.go": embeddingSource}
	for _, includeExported := range []bool{false, true} {
		facts := typedUnusedFacts(t, files, includeExported)
		assertNotReported(t, facts, "a.go:base", "a.go:Close", "a.go:leaf")
	}

	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": embeddingSource}), "a.go:base", "a.go:leaf")
}

func TestFindUnusedSymbols_PointerEmbeddedTypeIsNotReported(t *testing.T) {
	src := "package p\n\ntype base struct{}\n\ntype gen[X any] struct{}\n\ntype Impl struct {\n\t*base\n\tgen[int]\n}\n\nfunc main() { _ = Impl{} }\n"
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": src}, false), "a.go:base", "a.go:gen")
	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": src}), "a.go:base", "a.go:gen")
}

func TestFindUnusedSymbols_TypeEmbeddedFromAnotherPackageIsNotReported(t *testing.T) {
	facts := typedUnusedFacts(t, map[string]string{
		"dep/dep.go": "package dep\n\ntype Base struct{}\n\nfunc (Base) Close() error { return leaf() }\n\nfunc leaf() error { return nil }\n",
		"p/a.go":     "package p\n\nimport \"example.com/w/dep\"\n\ntype Impl struct{ dep.Base }\n\nfunc main() { var _ any = Impl{} }\n",
	}, true)
	assertNotReported(t, facts, "dep.go:Base", "dep.go:Close", "dep.go:leaf")
}

func TestFindUnusedSymbols_TypeEmbeddedInFunctionLocalStructIsNotReported(t *testing.T) {
	src := "package p\n\ntype base struct{}\n\nfunc main() {\n\ttype local struct{ base }\n\t_ = local{}\n}\n"
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": src}, false), "a.go:base")
	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": src}), "a.go:base")
}

func TestFindUnusedSymbols_TypeEmbeddedInStructNestedInOtherTypeIsNotReported(t *testing.T) {
	src := "package p\n\ntype base struct{}\n\ntype Outer struct {\n\tf []struct{ base }\n}\n\nfunc main() { _ = Outer{} }\n"
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": src}, false), "a.go:base")
	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": src}), "a.go:base")
}

func TestFindUnusedSymbols_InterfaceEmbeddedInLiveInterfaceIsNotReported(t *testing.T) {
	src := "package p\n\ntype inner interface{ m() }\n\ntype Outer interface{ inner }\n\nfunc main() { var _ Outer }\n"
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": src}, false), "a.go:inner")
	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": src}), "a.go:inner")
}

func TestFindUnusedSymbols_TypeEmbeddedOnlyInUnusedStructIsReported(t *testing.T) {
	src := "package p\n\ntype base struct{}\n\ntype impl struct{ base }\n"
	for _, facts := range []map[string]unusedFact{
		typedUnusedFacts(t, map[string]string{"p/a.go": src}, false),
		untypedUnusedFacts(t, map[string]string{"a.go": src}),
	} {
		assertReported(t, facts, "a.go:impl", "")
		assertReported(t, facts, "a.go:base", reasonUnusedCode)
	}
}

func TestFindUnusedSymbols_TypesReachedThroughEmbeddingCascadeAreNotReported(t *testing.T) {
	src := "package p\n\ntype (\n\tx struct{ y }\n\ty struct{ n z }\n\tz int\n)\n\nfunc main() { _ = x{} }\n"
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": src}, false), "a.go:y", "a.go:z")
	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": src}), "a.go:y", "a.go:z")
}

const aliasSource = "package p\n\ntype b struct{}\n\ntype a = b\n\nfunc (a) m() { leaf() }\n\nfunc leaf() {}\n\ntype I interface{ m() }\n\nfunc Run() I { return b{} }\n\nfunc main() { _ = Run() }\n"

func TestFindUnusedSymbols_MethodDeclaredOnAliasIsLiveWhenAliasedTypeIsLive(t *testing.T) {
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": aliasSource}, false), "a.go:a", "a.go:m", "a.go:leaf")
	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": aliasSource}), "a.go:a", "a.go:m", "a.go:leaf")
}

func TestFindUnusedSymbols_MethodDeclaredOnAliasChainIsLiveWhenAliasedTypeIsLive(t *testing.T) {
	src := "package p\n\ntype b struct{}\n\ntype a1 = b\n\ntype a2 = a1\n\nfunc (a2) m() { leaf() }\n\nfunc leaf() {}\n\n" +
		"type I interface{ m() }\n\nfunc main() { var _ I = b{} }\n"
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": src}, false), "a.go:a1", "a.go:a2", "a.go:m", "a.go:leaf")
}

func TestFindUnusedSymbols_AliasNamedOnlyByDeadMethodReceiverIsReported(t *testing.T) {
	src := "package p\n\ntype b struct{}\n\ntype a = b\n\nfunc (a) m() {}\n\nfunc main() { _ = b{} }\n"
	for _, facts := range []map[string]unusedFact{
		typedUnusedFacts(t, map[string]string{"p/a.go": src}, false),
		untypedUnusedFacts(t, map[string]string{"a.go": src}),
	} {
		assertReported(t, facts, "a.go:m", "")
		assertReported(t, facts, "a.go:a", "")
		assertNotReported(t, facts, "a.go:b")
	}
}

// assertMethodThroughInterfaceLiteralIsLive checks that a live type's methods
// foo and bar, named only by an interface written in use, keep leaf alive.
func assertMethodThroughInterfaceLiteralIsLive(t *testing.T, use string) {
	t.Helper()

	src := "package p\n\ntype T struct{}\n\nfunc (T) foo() { leaf() }\n\nfunc (T) bar() { leaf() }\n\nfunc leaf() {}\n\n" + use
	assertNotReported(t, typedUnusedFacts(t, map[string]string{"p/a.go": src}, false), "a.go:foo", "a.go:bar", "a.go:leaf")
	assertNotReported(t, untypedUnusedFacts(t, map[string]string{"a.go": src}), "a.go:foo", "a.go:bar", "a.go:leaf")
}

func TestFindUnusedSymbols_MethodCalledThroughAnonymousInterfaceParameterKeepsItsCalleesAlive(t *testing.T) {
	assertMethodThroughInterfaceLiteralIsLive(t, "func use(x interface{ foo(); bar() }) { x.foo(); x.bar() }\n\nfunc main() { use(T{}) }\n")
}

func TestFindUnusedSymbols_MethodCalledThroughFunctionLocalInterfaceKeepsItsCalleesAlive(t *testing.T) {
	assertMethodThroughInterfaceLiteralIsLive(t, "func main() {\n\ttype i interface{ foo(); bar() }\n\tvar x i = T{}\n\tx.foo()\n\tx.bar()\n}\n")
}

func TestFindUnusedSymbols_MethodCalledThroughInlineTypeConstraintKeepsItsCalleesAlive(t *testing.T) {
	assertMethodThroughInterfaceLiteralIsLive(t, "func use[X interface{ foo(); bar() }](x X) { x.foo(); x.bar() }\n\nfunc main() { use(T{}) }\n")
}

func TestFindUnusedSymbols_MethodCalledThroughAnonymousInterfaceFieldKeepsItsCalleesAlive(t *testing.T) {
	assertMethodThroughInterfaceLiteralIsLive(t, "type holder struct{ x interface{ foo(); bar() } }\n\nfunc main() {\n\th := holder{x: T{}}\n\th.x.foo()\n\th.x.bar()\n}\n")
}

func TestFindUnusedSymbols_MethodsNamedByStdInterfacesOfLiveTypesAreNotReported(t *testing.T) {
	src := `package p

import (
	"go/ast"
	"go/types"
)

type imp struct{}

func (imp) Import(path string) (*types.Package, error) { return nil, nil }

func (imp) Dead() {}

type vis struct{}

func (vis) Visit(n ast.Node) ast.Visitor { return nil }

type wrapErr struct{}

func (wrapErr) Error() string { return "" }

func (wrapErr) Unwrap() error { return nil }

func main() {
	_ = types.Config{Importer: imp{}}
	ast.Walk(vis{}, nil)
	_ = error(wrapErr{})
}
`
	facts := typedUnusedFacts(t, map[string]string{"p/a.go": src}, true)
	assertNotReported(t, facts, "a.go:Import", "a.go:Visit", "a.go:Error", "a.go:Unwrap")
	assertReported(t, facts, "a.go:Dead", "")
}

func TestFindUnusedSymbols_ExportedMethodNamedByAnInterfaceButNeverConvertedIsReported(t *testing.T) {
	src := "package p\n\ntype T struct{}\n\nfunc (T) String() string { return \"\" }\n\nfunc (T) Live() {}\n\nfunc main() { T{}.Live() }\n"
	facts := typedUnusedFacts(t, map[string]string{"p/a.go": src}, true)
	assertReported(t, facts, "a.go:String", "")
	assertNotReported(t, facts, "a.go:T", "a.go:Live")
}

func TestFindUnusedSymbols_MethodOfTypeConvertedToAnyOrBroadInterfaceIsKept(t *testing.T) {
	src := `package p

import "fmt"

type viaPrint struct{}

func (viaPrint) String() string { return "" }

type viaVariadic struct{}

func (viaVariadic) String() string { return "" }

type viaIface struct{}

func (viaIface) Foo() {}

func (viaIface) Dead() {}

type nested struct{ in inner }

type inner struct{}

func (inner) String() string { return "" }

type shape interface{ Foo() }

func main() {
	fmt.Println(viaPrint{})
	args := []any{viaVariadic{}}
	fmt.Println(args...)
	var s shape = viaIface{}
	fmt.Println(s)
	fmt.Println(nested{})
}
`
	facts := typedUnusedFacts(t, map[string]string{"p/a.go": src}, true)
	assertNotReported(t, facts, "a.go:String", "a.go:Foo")
	assertReported(t, facts, "a.go:Dead", "")
}

func TestFindUnusedSymbols_MethodSatisfyingInterfaceOnlyInTestCodeIsReported(t *testing.T) {
	files := map[string]string{
		"p/a.go":      "package p\n\ntype T struct{}\n\nfunc (T) Foo() {}\n\ntype I interface{ Foo() }\n\nfunc main() { _ = T{} }\n",
		"p/a_test.go": testImport + "func TestX(t *testing.T) { var _ I = T{} }\n",
	}
	assertReported(t, typedUnusedFacts(t, files, true), "a.go:Foo", "")
}

func TestFindUnusedSymbols_MethodOfAnInterfaceTheWorkspaceAssertsToIsKept(t *testing.T) {
	src := `package p

type base interface{ run() }

type gated interface {
	base
	refuse() bool
}

type planner struct{}

func (planner) run() {}

func (planner) refuse() bool { return false }

func (planner) dead() {}

func drive(b base) {
	b.run()
	if g, ok := b.(gated); ok {
		_ = g.refuse()
	}
}

func main() { drive(planner{}) }
`
	facts := typedUnusedFacts(t, map[string]string{"p/a.go": src}, false)
	assertNotReported(t, facts, "a.go:refuse", "a.go:run")
	assertReported(t, facts, "a.go:dead", "")
}

func TestFindUnusedSymbols_InterfaceMethodDeclaredInTestFileIsNotReported(t *testing.T) {
	files := map[string]string{
		"p/a.go":      "package p\n\ntype I interface{ Foo() }\n\nfunc Use(i I) { i.Foo() }\n\nfunc main() { Use(nil) }\n",
		"p/a_test.go": testImport + "type fake struct{}\n\nfunc (fake) Foo() {}\n\nfunc TestX(t *testing.T) { Use(fake{}) }\n",
	}
	assertNotReported(t, typedUnusedFacts(t, files, true), "a_test.go:Foo")
}
