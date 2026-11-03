package tests_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type variadicJSON struct {
	Function  string   `json:"function"`
	Signature string   `json:"signature"`
	File      string   `json:"file"`
	Line      int      `json:"line"`
	Column    int      `json:"column"`
	CallSites int      `json:"call_sites"`
	ZeroArgs  int      `json:"zero_args"`
	OneArg    int      `json:"one_arg"`
	ManyArgs  int      `json:"many_args"`
	Spread    int      `json:"spread"`
	ValueUses int      `json:"value_uses"`
	Configs   []string `json:"configs"`
}

type variadicsJSON struct {
	Variadics    []variadicJSON `json:"variadics"`
	TotalCount   int            `json:"total_count"`
	BuildConfigs []string       `json:"build_configs"`
	Uncovered    []struct {
		File   string `json:"file"`
		Reason string `json:"reason"`
	} `json:"uncovered_files"`
	TypeErrors []struct {
		File  string `json:"file"`
		Error string `json:"error"`
	} `json:"type_error_files"`
}

const variadicsModule = "module example.com/vari\n\ngo 1.21\n"

// callVariadics runs detect_variadics over files, with args when non-nil.
func callVariadics(t *testing.T, args map[string]any, files map[string]string) variadicsJSON {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	files["go.mod"] = variadicsModule
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "detect_variadics", Arguments: args})
	if err != nil {
		t.Fatalf("detect_variadics: %v", err)
	}
	if res.IsError {
		t.Fatalf("detect_variadics: %s", errText(res))
	}
	var out variadicsJSON
	if err := json.Unmarshal([]byte(errText(res)), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}
	if out.TotalCount != len(out.Variadics) {
		t.Errorf("total_count %d, want %d", out.TotalCount, len(out.Variadics))
	}
	return out
}

// findVariadic returns the single finding named function declared in base.
func findVariadic(t *testing.T, out variadicsJSON, base, function string) variadicJSON {
	t.Helper()
	for _, v := range out.Variadics {
		if v.Function == function && filepath.Base(v.File) == base {
			return v
		}
	}
	t.Fatalf("no finding %s in %s: %+v", function, base, out.Variadics)
	return variadicJSON{}
}

// checkShape compares the call-site counts of v, ignoring where it is declared.
func checkShape(t *testing.T, got variadicJSON, want variadicJSON) {
	t.Helper()
	g := [6]int{got.CallSites, got.ZeroArgs, got.OneArg, got.ManyArgs, got.Spread, got.ValueUses}
	w := [6]int{want.CallSites, want.ZeroArgs, want.OneArg, want.ManyArgs, want.Spread, want.ValueUses}
	if g != w {
		t.Errorf("%s [call_sites zero one many spread value_uses] = %v, want %v", got.Function, g, w)
	}
}

func variadicNames(out variadicsJSON) []string {
	names := []string{}
	for _, v := range out.Variadics {
		names = append(names, v.Function)
	}
	slices.Sort(names)
	return names
}

func TestVariadics_BucketsCallsByVariadicArgumentCount(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{"a/a.go": `package a

func Fixed(a int, b string) {}

func F(prefix string, xs ...int) {}

func pair() (string, int, int) { return "", 1, 2 }

func use(xs []int) {
	Fixed(1, "")
	F("none")
	F("one", 1)
	F("two", 1, 2)
	F("three", 1, 2, 3)
	F("spread", xs...)
	F(pair())
}
`})

	got := variadicNames(out)
	if !slices.Equal(got, []string{"F"}) {
		t.Fatalf("listed %v, want only F", got)
	}
	f := findVariadic(t, out, "a.go", "F")
	checkShape(t, f, variadicJSON{CallSites: 6, ZeroArgs: 1, OneArg: 1, ManyArgs: 3, Spread: 1})
	if f.Signature != "func F(prefix string, xs ...int)" {
		t.Errorf("signature = %q", f.Signature)
	}
	if f.File != "a/a.go" || f.Line != 5 || f.Column != 6 {
		t.Errorf("position = %s:%d:%d", f.File, f.Line, f.Column)
	}
}

func TestVariadics_MethodExpressionReceiverIsNotAVariadicArgument(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{"a/a.go": `package a

type T struct{}

func (T) M(xs ...int) {}

func use(t T) {
	T.M(t)
	T.M(t, 1)
	T.M(t, 1, 2)
	t.M(1)
}
`})

	checkShape(t, findVariadic(t, out, "a.go", "T.M"), variadicJSON{CallSites: 4, ZeroArgs: 1, OneArg: 2, ManyArgs: 1})
}

func TestVariadics_GenericCallsCountAgainstTheOrigin(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{"a/a.go": `package a

func G[T any](xs ...T) {}

type Box[T any] struct{}

func (Box[T]) Put(xs ...T) {}

func use(b Box[int]) {
	G[int]()
	G[int](1, 2)
	G("inferred")
	b.Put(1)
}
`})

	checkShape(t, findVariadic(t, out, "a.go", "G"), variadicJSON{CallSites: 3, ZeroArgs: 1, OneArg: 1, ManyArgs: 1})
	checkShape(t, findVariadic(t, out, "a.go", "Box.Put"), variadicJSON{CallSites: 1, OneArg: 1})
}

func TestVariadics_FuncValueUsesAreSeparateFromCalls(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{"a/a.go": `package a

func F(xs ...int) {}

var hook = F

func apply(f func(...int)) { f(1) }

func use() {
	apply(F)
	F()
}
`})

	checkShape(t, findVariadic(t, out, "a.go", "F"), variadicJSON{CallSites: 1, ZeroArgs: 1, ValueUses: 2})
}

func TestVariadics_InterfaceCallsCountForTheInterfaceMethod(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{"a/a.go": `package a

type Doer interface {
	Do(xs ...int)
}

type Impl struct{}

func (Impl) Do(xs ...int) {}

func use(d Doer, i Impl) {
	d.Do()
	d.Do(1, 2)
	i.Do(1)
}
`})

	checkShape(t, findVariadic(t, out, "a.go", "Doer.Do"), variadicJSON{CallSites: 2, ZeroArgs: 1, ManyArgs: 1})
	checkShape(t, findVariadic(t, out, "a.go", "Impl.Do"), variadicJSON{CallSites: 1, OneArg: 1})
}

func TestVariadics_CallsFromOtherPackagesAndTestFilesCount(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"lib/lib.go": `package lib

func F(xs ...int) {}
`,
		"lib/lib_test.go": `package lib

import "testing"

func helper(t *testing.T, names ...string) {}

func TestIn(t *testing.T) {
	F(1)
	helper(t)
}
`,
		"lib/ext_test.go": `package lib_test

import (
	"testing"

	"example.com/vari/lib"
)

func TestExt(t *testing.T) {
	lib.F(1, 2)
}
`,
		"app/app.go": `package app

import "example.com/vari/lib"

func Run() { lib.F() }
`,
	})

	checkShape(t, findVariadic(t, out, "lib.go", "F"), variadicJSON{CallSites: 3, ZeroArgs: 1, OneArg: 1, ManyArgs: 1})
	checkShape(t, findVariadic(t, out, "lib_test.go", "helper"), variadicJSON{CallSites: 1, ZeroArgs: 1})
}

func TestVariadics_GeneratedDeclarationsAreSkippedButTheirCallsCount(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"a/a.go": `package a

func F(xs ...int) {}
`,
		"a/a_gen.go": genHeader + `package a

func Gen(xs ...int) {}

func use() { F(1) }
`,
	})

	got := variadicNames(out)
	if !slices.Equal(got, []string{"F"}) {
		t.Fatalf("listed %v, want only F", got)
	}
	checkShape(t, findVariadic(t, out, "a.go", "F"), variadicJSON{CallSites: 1, OneArg: 1})
}

func TestVariadics_CallsMergeAcrossBuildConfigurations(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"a/a.go": `package a

func F(xs ...int) {}

func use() {
	F()
	Sys(1)
}
`,
		"a/call_windows.go": `package a

func onWindows() { F(1, 2) }
`,
		"a/sys_linux.go": `package a

func Sys(xs ...int) {}
`,
		"a/sys_darwin.go": `package a

func Sys(xs ...int) {}
`,
		"a/sys_windows.go": `package a

func Sys(xs ...int) { Sys(xs...) }
`,
		"a/sys_other.go": `//go:build !linux && !darwin && !windows

package a

func Sys(xs ...int) {}
`,
	})

	if len(out.BuildConfigs) < 2 {
		t.Fatalf("build_configs = %v, want several", out.BuildConfigs)
	}
	checkShape(t, findVariadic(t, out, "a.go", "F"), variadicJSON{CallSites: 2, ZeroArgs: 1, ManyArgs: 1})

	want := variadicJSON{CallSites: 2, OneArg: 1, Spread: 1}
	for _, base := range []string{"sys_linux.go", "sys_darwin.go", "sys_windows.go"} {
		sys := findVariadic(t, out, base, "Sys")
		checkShape(t, sys, want)
		if len(sys.Configs) == 0 {
			t.Errorf("%s: configs is empty, want the configurations declaring it", base)
		}
	}
	f := findVariadic(t, out, "a.go", "F")
	if len(f.Configs) != 0 {
		t.Errorf("F: configs = %v, want none when every configuration declares it", f.Configs)
	}
}

func TestVariadics_PackageFilterLimitsDeclarationsNotCounts(t *testing.T) {
	files := func() map[string]string {
		return map[string]string{
			"lib/lib.go": `package lib

func F(xs ...int) {}
`,
			"app/app.go": `package app

import "example.com/vari/lib"

func Own(xs ...int) {}

func Run() { lib.F(1) }
`,
		}
	}

	for _, pkg := range []string{"example.com/vari/lib", "lib"} {
		out := callVariadics(t, map[string]any{"package": pkg}, files())
		got := variadicNames(out)
		if !slices.Equal(got, []string{"F"}) {
			t.Fatalf("package %q listed %v, want only F", pkg, got)
		}
		checkShape(t, findVariadic(t, out, "lib.go", "F"), variadicJSON{CallSites: 1, OneArg: 1})
	}

	out := callVariadics(t, map[string]any{"package": "nosuch"}, files())
	if len(out.Variadics) != 0 {
		t.Errorf("unknown package listed %v", variadicNames(out))
	}
}

func TestVariadics_EachLinkOfAMethodChainCounts(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{"a/a.go": `package a

type B struct{}

func (b B) With(xs ...int) B { return b }

func use(b B) {
	b.With().With(1).With(1, 2)
	b.With(1).
		With(2).
		With(3)
}
`})

	checkShape(t, findVariadic(t, out, "a.go", "B.With"), variadicJSON{CallSites: 6, ZeroArgs: 1, OneArg: 4, ManyArgs: 1})
}

func TestVariadics_ExternalTestFilesBucketLikeAnyOther(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"lib/lib.go": `package lib

type T struct{}

func (T) M(xs ...int) {}

func F(prefix string, xs ...int) {}
`,
		"lib/ext_test.go": `package lib_test

import (
	"testing"

	"example.com/vari/lib"
)

func triple() (string, int, int) { return "", 1, 2 }

func TestExt(t *testing.T) {
	lib.T.M(lib.T{}, 1)
	lib.F(triple())
}
`,
	})

	checkShape(t, findVariadic(t, out, "lib.go", "T.M"), variadicJSON{CallSites: 1, OneArg: 1})
	checkShape(t, findVariadic(t, out, "lib.go", "F"), variadicJSON{CallSites: 1, ManyArgs: 1})
}

func TestVariadics_SameNameInExternalTestPackageKeepsItsOwnCounts(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"lib/lib.go": `package lib

func helper(xs ...int) {}

func use() { helper() }
`,
		"lib/ext_test.go": `package lib_test

import "testing"

func helper(xs ...string) {}

func TestExt(t *testing.T) {
	helper("a", "b")
	helper("a", "b", "c")
}
`,
	})

	checkShape(t, findVariadic(t, out, "lib.go", "helper"), variadicJSON{CallSites: 1, ZeroArgs: 1})
	checkShape(t, findVariadic(t, out, "ext_test.go", "helper"), variadicJSON{CallSites: 2, ManyArgs: 2})
}

func TestVariadics_EmbeddingAttributesCallsToTheDeclaringMethod(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{"a/a.go": `package a

type Doer interface {
	Do(xs ...int)
}

type Wider interface {
	Doer
}

type Impl struct{}

func (Impl) Do(xs ...int) {}

type Outer struct{ Impl }

func use(w Wider, o Outer) {
	w.Do(1)
	o.Do()
}
`})

	checkShape(t, findVariadic(t, out, "a.go", "Doer.Do"), variadicJSON{CallSites: 1, OneArg: 1})
	checkShape(t, findVariadic(t, out, "a.go", "Impl.Do"), variadicJSON{CallSites: 1, ZeroArgs: 1})
}

func TestVariadics_CallsInFilesGeneratedForOneConfigurationCount(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"a/x_linux.go": `package a

func X(xs ...int) {}
`,
		"a/x_darwin.go": `package a

func X(xs ...int) {}
`,
		"a/x_windows.go": genHeader + `package a

func X(xs ...int) {}

func onWindows() { X(1, 2) }
`,
	})

	for _, base := range []string{"x_linux.go", "x_darwin.go"} {
		checkShape(t, findVariadic(t, out, base, "X"), variadicJSON{CallSites: 1, ManyArgs: 1})
	}
	for _, v := range out.Variadics {
		if filepath.Base(v.File) == "x_windows.go" {
			t.Errorf("listed generated declaration %+v", v)
		}
	}
}

func TestVariadics_PackageFilterFindsDirectoryOutsideThePrimaryConfiguration(t *testing.T) {
	for _, pkg := range []string{"win", "./win", "win/", "example.com/vari/win"} {
		out := callVariadics(t, map[string]any{"package": pkg}, map[string]string{
			"win/w_windows.go": `package win

func W(xs ...int) {}
`,
			"win/w_plan9.go": `package win

func P(xs ...int) {}
`,
			"a/a.go": `package a

func A(xs ...int) {}
`,
		})
		got := variadicNames(out)
		if !slices.Equal(got, []string{"P", "W"}) {
			t.Errorf("package %q listed %v, want P and W", pkg, got)
		}
	}
}

func TestVariadics_FilesWithTypeErrorsAreReported(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"a/a.go": `package a

type T struct{}

func (T) M(xs ...int) {}

func F(prefix string, xs ...int) {}

func tooFew() { F() }
`,
		"a/broken.go": `package a

func broken() {
	y := undefinedFunc()
	y.M(1)
}
`,
		"b/b.go": `package b

func Clean(xs ...int) {}
`,
	})

	checkShape(t, findVariadic(t, out, "a.go", "F"), variadicJSON{CallSites: 1, ZeroArgs: 1})

	files := []string{}
	for _, e := range out.TypeErrors {
		if e.Error == "" {
			t.Errorf("%s has no error text", e.File)
		}
		files = append(files, e.File)
	}
	slices.Sort(files)
	if !slices.Equal(files, []string{"a/a.go", "a/broken.go"}) {
		t.Errorf("type_error_files = %v, want a/a.go and a/broken.go", files)
	}
}

func TestVariadics_FindingsAreSortedByFileThenLine(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"b/b.go": `package b

func Z(xs ...int) {}

func A(xs ...int) {}
`,
		"a/a.go": `package a

func M(xs ...int) {}
`,
	})

	got := []string{out.Variadics[0].Function, out.Variadics[1].Function, out.Variadics[2].Function}
	if !slices.Equal(got, []string{"M", "Z", "A"}) {
		t.Errorf("order = %v", got)
	}
}

func TestVariadics_FilesThatDoNotTypeCheckAreReported(t *testing.T) {
	out := callVariadics(t, nil, map[string]string{
		"a/a.go": `package a

func F(xs ...int) {}
`,
		"a/broken_test.go": `package a

func broken() { F(1, undefinedName) }
`,
	})

	if len(out.Uncovered) != 1 || !strings.HasSuffix(out.Uncovered[0].File, "broken_test.go") {
		t.Errorf("uncovered_files = %+v, want only broken_test.go", out.Uncovered)
	}
}
