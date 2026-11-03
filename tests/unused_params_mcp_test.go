package tests_test

import (
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type unusedParamJSON struct {
	Function  string   `json:"function"`
	Param     string   `json:"param"`
	Index     int      `json:"index"`
	Blank     bool     `json:"blank"`
	File      string   `json:"file"`
	Line      int      `json:"line"`
	CallSites int      `json:"call_sites"`
	Fixable   bool     `json:"fixable"`
	Propagate bool     `json:"propagate"`
	Reason    string   `json:"reason"`
	Configs   []string `json:"configs"`
}

type unusedParamsJSON struct {
	Params     []unusedParamJSON `json:"unused_params"`
	TotalCount int               `json:"total_count"`
}

// callUnusedParams runs detect_unused_params over files, with args when non-nil.
func callUnusedParams(t *testing.T, args map[string]any, files map[string]string) unusedParamsJSON {
	t.Helper()

	if args == nil {
		args = map[string]any{}
	}

	files["go.mod"] = "module example.com/up\n\ngo 1.21\n"
	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res, err := sess.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "detect_unused_params", Arguments: args})
	if err != nil {
		t.Fatalf("detect_unused_params: %v", err)
	}

	if res.IsError {
		t.Fatalf("detect_unused_params: %s", errText(res))
	}

	var out unusedParamsJSON
	if err := json.Unmarshal([]byte(errText(res)), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}

	if out.TotalCount != len(out.Params) {
		t.Errorf("total_count %d, want %d", out.TotalCount, len(out.Params))
	}

	return out
}

// findParam returns the finding for param of function; param "" is an unnamed one.
func findParam(t *testing.T, out unusedParamsJSON, function, param string) unusedParamJSON {
	t.Helper()

	for _, p := range out.Params {
		if p.Function == function && p.Param == param {
			return p
		}
	}

	t.Fatalf("no finding for %s(%s): %+v", function, param, out.Params)

	return unusedParamJSON{}
}

func wantFixable(t *testing.T, p unusedParamJSON) {
	t.Helper()

	if !p.Fixable || p.Reason != "" {
		t.Errorf("%s(%s): fixable=%v reason=%q, want fixable", p.Function, p.Param, p.Fixable, p.Reason)
	}
}

func wantRefused(t *testing.T, p unusedParamJSON, reason string) {
	t.Helper()

	if p.Fixable || !strings.Contains(p.Reason, reason) {
		t.Errorf("%s(%s): fixable=%v reason=%q, want a refusal containing %q", p.Function, p.Param, p.Fixable, p.Reason, reason)
	}
}

func TestUnusedParams_ListsOnlyParametersTheBodyNeverReads(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

func F(used int, unused string, alsoUnused bool) int { return used }

func Closure(x int, y int) func() int { return func() int { return y } }

type T struct{}

func (T) M(a int, b int) int { return a }

func use() {
	F(1, "", true)
	F(2, "", false)
	Closure(1, 2)
	T{}.M(1, 2)
}
`})

	if len(out.Params) != 4 {
		t.Fatalf("got %+v, want unused, alsoUnused, x and b", out.Params)
	}

	f := findParam(t, out, "F", "unused")
	wantFixable(t, f)

	if f.Index != 1 || f.CallSites != 2 || f.File != "a/a.go" || f.Line != 3 || f.Blank {
		t.Errorf("F(unused) = %+v", f)
	}

	wantFixable(t, findParam(t, out, "F", "alsoUnused"))
	wantFixable(t, findParam(t, out, "Closure", "x"))

	p := findParam(t, out, "Closure", "x")
	if p.Index != 0 {
		t.Errorf("Closure(x) index = %d", p.Index)
	}
}

func TestUnusedParams_ReportsBlankAndUnnamedParametersAsBlank(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

func Blank(_ int, x string) string { return x }

func Unnamed(string) {}

func use() {
	Blank(1, "")
	Unnamed("")
}
`})

	if len(out.Params) != 2 {
		t.Fatalf("got %+v, want the blank and the unnamed parameter", out.Params)
	}

	for _, f := range []string{"Blank", "Unnamed"} {
		p := findParam(t, out, f, map[string]string{"Blank": "_", "Unnamed": ""}[f])
		if !p.Blank || p.Index != 0 {
			t.Errorf("%s: %+v, want blank at index 0", f, p)
		}

		wantFixable(t, p)
	}
}

func TestUnusedParams_AmbiguousRemovalIsNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

func Twin(_ int, _ int) {}

func Mixed(int, int, string) {}

func use() {
	Twin(1, 2)
	Mixed(1, 2, "")
}
`})

	if len(out.Params) != 5 {
		t.Fatalf("got %+v, want every parameter listed", out.Params)
	}

	for _, p := range out.Params {
		if p.Function == "Mixed" && p.Index == 2 {
			wantFixable(t, p) // removing the string leaves one result
			continue
		}

		wantRefused(t, p, "ambiguous")
	}
}

func TestUnusedParams_SkipsInterfaceMethodsAndGeneratedDeclarations(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"a/a.go": `package a

type Doer interface{ Do(x int) }

type Fn func(unused int)

var lit = func(unused int) {}
`,
		"a/a_gen.go": genHeader + `package a

func Gen(unused int) {}
`,
	})

	if len(out.Params) != 0 {
		t.Fatalf("got %+v, want none", out.Params)
	}
}

func TestUnusedParams_FuncValueUseIsNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

func Handler(unused int) {}

func Method(unused int) {}

type T struct{}

func (T) M(unused int) {}

func apply(f func(int)) { f(1) }

func use(t T) {
	apply(Handler)
	apply(t.M)
	Method(1)
}
`})

	wantRefused(t, findParam(t, out, "Handler", "unused"), "func value")
	wantRefused(t, findParam(t, out, "T.M", "unused"), "func value")
	wantFixable(t, findParam(t, out, "Method", "unused"))
}

func TestUnusedParams_CallSiteArgumentsWithCallsOrReceivesAreNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

func Call(unused int) {}

func Recv(unused int) {}

func Clean(unused int) {}

func Spread(unused int, rest ...int) {}

func Multi(a int, unused int) {}

func Variadic(xs ...int) {}

func two() (int, int) { return 1, 2 }

func next() int { return 1 }

func use(ch chan int, xs []int, n int32) {
	Call(next())
	Recv(<-ch)
	Clean(int(n))
	Spread(next(), xs...)
	Multi(two())
	Variadic(1)
}
`})

	wantRefused(t, findParam(t, out, "Call", "unused"), "side effects")
	wantRefused(t, findParam(t, out, "Recv", "unused"), "side effects")
	wantFixable(t, findParam(t, out, "Clean", "unused"))
	wantRefused(t, findParam(t, out, "Spread", "unused"), "side effects")
	wantRefused(t, findParam(t, out, "Multi", "unused"), "multi-value")
	wantRefused(t, findParam(t, out, "Variadic", "xs"), "variadic arguments")
}

func TestUnusedParams_MethodExpressionCallIsNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

type T struct{}

func (T) M(unused int) {}

func use() { T.M(T{}, 1) }
`})

	wantRefused(t, findParam(t, out, "T.M", "unused"), "method expression")
}

func TestUnusedParams_WorkspaceInterfaceNeedsPropagateWhenEveryImplementerLeavesItUnused(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

type Doer interface{ Do(x int) }

type One struct{}

func (One) Do(x int) {}

type Two struct{}

func (Two) Do(x int) {}

func use(d Doer) { d.Do(1) }

var _ Doer = One{}
var _ Doer = Two{}
`})

	for _, recv := range []string{"One", "Two"} {
		p := findParam(t, out, recv+".Do", "x")
		wantFixable(t, p)

		if !p.Propagate {
			t.Errorf("%s.Do: propagate = false, want true", recv)
		}
	}
}

func TestUnusedParams_ImplementerThatReadsTheParameterBlocksPropagate(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

type Doer interface{ Do(x int) }

type Idle struct{}

func (Idle) Do(x int) {}

type Busy struct{}

func (Busy) Do(x int) { _ = x }

var _ Doer = Idle{}
var _ Doer = Busy{}
`})

	p := findParam(t, out, "Idle.Do", "x")
	wantRefused(t, p, "Doer")
	wantRefused(t, p, "Busy.Do")
}

func TestUnusedParams_ExternalInterfaceNamesItAndIsNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

import (
	"io"
	"net/http"
)

type W struct{}

func (W) Write(p []byte) (int, error) { return 0, nil }

type H struct{}

func (H) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

var _ io.Writer = W{}
var _ http.Handler = H{}
`})

	wantRefused(t, findParam(t, out, "W.Write", "p"), "io.Writer")
	wantRefused(t, findParam(t, out, "H.ServeHTTP", "w"), "http.Handler")
}

func TestUnusedParams_PromotedMethodSatisfyingAnInterfaceIsNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

import "io"

type base struct{}

func (base) Write(p []byte) (int, error) { return 0, nil }

type Outer struct{ base }

var _ io.Writer = Outer{}
`})

	wantRefused(t, findParam(t, out, "base.Write", "p"), "io.Writer")
}

func TestUnusedParams_MergesDeclarationsAcrossBuildConfigurations(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"a/a.go": `package a

func use() {
	Sys(1)
	Shared(1)
}

func Shared(x int) {}
`,
		"a/sys_linux.go":  "package a\n\nfunc Sys(x int) {}\n",
		"a/sys_darwin.go": "package a\n\nfunc Sys(x int) {}\n",
		"a/sys_other.go":  "//go:build !linux && !darwin\n\npackage a\n\nfunc Sys(x int) int { return x }\n",
	})

	if len(out.Params) != 3 {
		t.Fatalf("got %+v, want Shared and both Sys variants that ignore x", out.Params)
	}

	shared := findParam(t, out, "Shared", "x")
	wantFixable(t, shared)

	if len(shared.Configs) != 0 {
		t.Errorf("Shared: configs = %v, want none when every configuration declares it", shared.Configs)
	}

	for _, p := range out.Params {
		if p.Function != "Sys" {
			continue
		}

		wantRefused(t, p, "another build configuration")

		if len(p.Configs) == 0 || p.CallSites != 1 {
			t.Errorf("Sys: configs=%v call_sites=%d, want the declaring configurations and the one call", p.Configs, p.CallSites)
		}
	}
}

func TestUnusedParams_CallsFromExternalTestPackagesCountAndTheirDeclarationsAreListed(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"lib/lib.go": `package lib

func F(unused int) {}
`,
		"lib/ext_test.go": `package lib_test

import (
	"testing"

	"example.com/up/lib"
)

func helper(unused string) {}

func TestExt(t *testing.T) {
	lib.F(next())
	helper("")
}

func next() int { return 1 }
`,
	})

	wantRefused(t, findParam(t, out, "F", "unused"), "side effects")

	f := findParam(t, out, "F", "unused")
	if f.CallSites != 1 {
		t.Errorf("F: call_sites = %d", f.CallSites)
	}

	h := findParam(t, out, "helper", "unused")
	wantFixable(t, h)

	if h.CallSites != 1 {
		t.Errorf("helper: call_sites = %d", h.CallSites)
	}
}

func TestUnusedParams_PackageFilterLimitsDeclarationsNotCalls(t *testing.T) {
	out := callUnusedParams(t, map[string]any{"package": "lib"}, map[string]string{
		"lib/lib.go": `package lib

func F(unused int) {}
`,
		"app/app.go": `package app

import "example.com/up/lib"

func Own(unused int) {}

func Run() { lib.F(1); lib.F(2) }
`,
	})

	if len(out.Params) != 1 || out.Params[0].Function != "F" || out.Params[0].CallSites != 2 {
		t.Fatalf("got %+v, want only F with 2 calls", out.Params)
	}
}

func TestUnusedParams_GenericDeclarationsAreNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"a/a.go": `package a

func G[T any](unused int, v T) T { return v }

type Box[T any] struct{ v T }

func (b Box[T]) Get(unused int) T { return b.v }

func use(b Box[int]) { G(1, 2); b.Get(1) }
`})

	wantRefused(t, findParam(t, out, "G", "unused"), "type parameters")
	wantRefused(t, findParam(t, out, "Box.Get", "unused"), "type parameters")
}

func TestUnusedParams_TestEntryPointsAreNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"a/a.go":      "package a\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestIt(t *testing.T) {}\n\nfunc Testimony(t *testing.T) {}\n",
	})

	wantRefused(t, findParam(t, out, "TestIt", "t"), "test entry point")
	wantFixable(t, findParam(t, out, "Testimony", "t"))
}

func TestUnusedParams_VariantsWithDifferentSignaturesAcrossConfigurationsAreNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"a/a.go":         "package a\n\nfunc use() { Sys(1, 2) }\n",
		"a/sys_linux.go": "package a\n\nfunc Sys(a int, b float64) {}\n",
		"a/sys_other.go": "//go:build !linux\n\npackage a\n\nfunc Sys(a int, b int) {}\n",
	})

	for _, p := range out.Params {
		if p.Function == "Sys" && p.Param == "b" {
			wantRefused(t, p, "which differs from")
			wantRefused(t, p, "make the declarations agree first")
		}
	}

	if findParam(t, out, "Sys", "b").Fixable {
		t.Errorf("Sys(b) fixable")
	}
}

func TestUnusedParams_FunctionWithoutABodyIsNotListed(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"a/a.go":  "package a\n\nfunc stub(x int)\n\nfunc Real(x int) {}\n",
		"a/asm.s": "",
	})

	for _, p := range out.Params {
		if p.Function == "stub" {
			t.Errorf("listed the bodiless stub: %+v", p)
		}
	}

	wantFixable(t, findParam(t, out, "Real", "x"))
}

func TestUnusedParams_ResultNamesDoNotSplitConfigurations(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"a/sys_linux.go":  "package a\n\nfunc Sys(x int) (n int) { return 0 }\n",
		"a/sys_darwin.go": "package a\n\nfunc Sys(x int) int { return 0 }\n",
	})

	if len(out.Params) != 2 {
		t.Fatalf("got %+v, want x under both configurations", out.Params)
	}

	for _, p := range out.Params {
		wantFixable(t, p)
	}
}

func TestUnusedParams_MismatchNamesTheCurrentFileSignatureFirst(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{
		"a/sys_linux.go":  "package a\n\nfunc Sys(x int) {}\n",
		"a/sys_darwin.go": "package a\n\nfunc Sys(y int) {}\n",
	})

	if len(out.Params) != 2 {
		t.Fatalf("got %+v, want x and y", out.Params)
	}

	for _, p := range out.Params {
		want := "in a/sys_linux.go has signature (x int), which differs from (y int)"
		if p.File == "a/sys_darwin.go" {
			want = "in a/sys_darwin.go has signature (y int), which differs from (x int)"
		}

		wantRefused(t, p, want)
	}
}
