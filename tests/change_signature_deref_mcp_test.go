package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const derefTarget = "package p\n\nfunc sum(b *[]int, k int) int {\n\ttotal := 0\n\tfor _, v := range *b {\n\t\ttotal += v + k\n\t}\n\treturn total + len(*b)\n}\n"

func derefArgs(extra map[string]any) map[string]any {
	args := map[string]any{
		"function_name": "sum",
		"source_file":   "p/f.go",
		"subcommand":    "deref_param",
		"params": []map[string]any{
			{"name": "b", "type": "[]int"},
			{"name": "k", "type": "int"},
		},
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestChangeSignature_DerefParamRewritesDeclBodyAndCallSites(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": derefTarget,
		"p/use.go": "package p\n\nfunc Use(ptr *[]int) int {\n\tx := []int{1}\n\ta := sum(&x, 1)\n\tb := sum(ptr, 2)\n\tc := sum(&[]int{3}, 3)\n\treturn a + b + c\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefArgs(nil))

	f := fileText(t, dir, "p/f.go")
	for _, want := range []string{"func sum(b []int, k int) int", "range b {", "len(b)"} {
		if !strings.Contains(f, want) {
			t.Errorf("f.go missing %q:\n%s", want, f)
		}
	}
	if strings.Contains(f, "*b") {
		t.Errorf("f.go still dereferences b:\n%s", f)
	}
	for _, want := range []string{"sum(x, 1)", "sum(*ptr, 2)", "sum([]int{3}, 3)"} {
		assertFileContains(t, dir, "p/use.go", want)
	}
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamParenthesizesNonPrimaryArgument(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   derefTarget,
		"p/use.go": "package p\n\nfunc pick(a, b *[]int, c bool) int {\n\treturn sum(map[bool]*[]int{true: a, false: b}[c], 1)\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefArgs(nil))

	assertFileContains(t, dir, "p/use.go", "sum(*map[bool]*[]int{true: a, false: b}[c], 1)")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamRefusesNonDerefUseOfParam(t *testing.T) {
	cases := map[string]string{
		"nil compare": "package p\n\nfunc sum(b *[]int, k int) int {\n\tif b == nil {\n\t\treturn 0\n\t}\n\treturn len(*b)\n}\n",
		"passed on":   "package p\n\nfunc other(b *[]int) int { return len(*b) }\n\nfunc sum(b *[]int, k int) int { return other(b) }\n",
		"assigned":    "package p\n\nfunc sum(b *[]int, k int) int {\n\tb = nil\n\treturn k\n}\n",
		"address":     "package p\n\nfunc sum(b *[]int, k int) int {\n\tpp := &b\n\treturn len(**pp)\n}\n",
		"write thru":  "package p\n\nfunc sum(b *[]int, k int) int {\n\t*b = nil\n\treturn k\n}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"p/f.go":   src,
				"p/use.go": "package p\n\nfunc Use() int {\n\tx := []int{1}\n\treturn sum(&x, 1)\n}\n",
			})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, derefArgs(nil))

			if !res.IsError || !strings.Contains(errText(res), "deref_param") {
				t.Errorf("want deref_param refusal, got IsError=%v %q", res.IsError, errText(res))
			}
			if got := fileText(t, dir, "p/f.go"); got != src {
				t.Errorf("f.go changed:\n%s", got)
			}
			assertFileContains(t, dir, "p/use.go", "sum(&x, 1)")
		})
	}
}

func TestChangeSignature_DerefParamIgnoresShadowingLocal(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc sum(b *[]int, k int) int {\n\tt := len(*b)\n\tif true {\n\t\tb := &k\n\t\tt += *b\n\t}\n\treturn t\n}\n",
		"p/use.go": "package p\n\nfunc Use() int {\n\tx := []int{1}\n\treturn sum(&x, 1)\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, derefArgs(nil))

	assertFileContains(t, dir, "p/f.go", "len(b)")
	assertFileContains(t, dir, "p/f.go", "t += *b")
	goVets(t, dir, "")
}

func TestChangeSignature_DerefParamReportsFuncValueReferenceAsSkipped(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   derefTarget,
		"p/use.go": "package p\n\nvar fn = sum\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := changeSigOK(t, sess, derefArgs(nil))

	if sites, _ := out["skipped_sites"].([]any); len(sites) != 1 {
		t.Errorf("skipped_sites = %v, want 1 entry", out["skipped_sites"])
	}
}
