package tests_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func removeParamArgs(fn, file string, params ...map[string]any) map[string]any {
	return map[string]any{"function_name": fn, "source_file": file, "subcommand": "remove_param", "params": params}
}

func TestChangeSignature_RemoveParamDropsTheRemovedArgument(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\n" +
			"func first(a, b, c int) int { return b + c }\n\n" +
			"func middle(a int, b string, c int) int { return a + c }\n\n" +
			"func grouped(a, b, c int) int { return a + c }\n",
		"p/use.go": "package p\n\nfunc Use() int { return first(1, 2, 3) + middle(4, \"x\", 5) + grouped(6, 7, 8) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("first", "p/f.go", param("b", "int"), param("c", "int")))
	changeSigOK(t, sess, removeParamArgs("middle", "p/f.go", param("a", "int"), param("c", "int")))
	changeSigOK(t, sess, removeParamArgs("grouped", "p/f.go", param("a", "int"), param("c", "int")))

	assertFileContains(t, dir, "p/use.go", "first(2, 3)")
	assertFileContains(t, dir, "p/use.go", "middle(4, 5)")
	assertFileContains(t, dir, "p/use.go", "grouped(6, 8)")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamRemovesUnusedTrailingVariadic(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":        "package p\n\nfunc f(x int, opts ...string) int { return x }\n",
		"p/use.go":      "package p\n\nfunc Use() int { return f(1) }\n",
		"p/use_test.go": "package p\n\nimport \"testing\"\n\nfunc TestUse(t *testing.T) { f(2) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("x", "int")))

	assertFileContains(t, dir, "p/f.go", "func f(x int) int")
	assertFileContains(t, dir, "p/use.go", "f(1)")
	assertFileContains(t, dir, "p/use_test.go", "f(2)")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamKeepsVariadicTailAndSpread(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f(skip int, xs ...int) int { return len(xs) }\n",
		"p/use.go": "package p\n\nfunc Use(ys []int) int { return f(1, 2, 3) + f(1) + f(1, ys...) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("xs", "...int")))

	assertFileContains(t, dir, "p/use.go", "f(2, 3) + f() + f(ys...)")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamRefusals(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		use      string
		remain   []map[string]any
		contains []string
	}{
		{
			name:     "calls pass variadic arguments",
			target:   "func f(x int, opts ...string) int { return x }",
			use:      "func A() int { return f(1, \"a\") }\nfunc B() int { return f(1, \"a\", \"b\") }",
			remain:   []map[string]any{param("x", "int")},
			contains: []string{"use.go:3", "use.go:4", "variadic"},
		},
		{
			name:     "call spreads a slice",
			target:   "func f(x int, opts ...string) int { return x }",
			use:      "func A(s []string) int { return f(1, s...) }",
			remain:   []map[string]any{param("x", "int")},
			contains: []string{"use.go:3", "variadic"},
		},
		{
			name:     "body uses the parameter",
			target:   "func f(a, b int) int { return a + b }",
			use:      "func A() int { return f(1, 2) }",
			remain:   []map[string]any{param("a", "int")},
			contains: []string{"f.go:3", "b"},
		},
		{
			name:     "call passes a multi-value call",
			target:   "func f(a, b int) int { return a }",
			use:      "func g() (int, int) { return 1, 2 }\nfunc A() int { return f(g()) }",
			remain:   []map[string]any{param("a", "int")},
			contains: []string{"use.go:4", "arguments"},
		},
		{
			name:     "call passes a multi-value call to a variadic function, removing the fixed parameter",
			target:   "func f(a int, xs ...int) int { return len(xs) }",
			use:      "func g() (int, int) { return 1, 2 }\nfunc A() int { return f(g()) }",
			remain:   []map[string]any{param("xs", "...int")},
			contains: []string{"use.go:4", "multi-value"},
		},
		{
			name:     "call passes a multi-value call to a variadic function, removing the variadic",
			target:   "func f(a int, xs ...int) int { return a }",
			use:      "func g() (int, int) { return 1, 2 }\nfunc A() int { return f(g()) }",
			remain:   []map[string]any{param("a", "int")},
			contains: []string{"use.go:4", "multi-value"},
		},
		{
			name:     "method expression passes the receiver as an argument",
			target:   "type T struct{}\n\nfunc (T) f(a int, xs ...int) int { return len(xs) }",
			use:      "func A() int { return T.f(T{}, 1) }",
			remain:   []map[string]any{param("xs", "...int")},
			contains: []string{"use.go:3", "method expression"},
		},
		{
			name:     "dropped argument calls a function",
			target:   "func f(a, b int) int { return a }",
			use:      "func h() int { return 1 }\nfunc A() int { return f(1, h()) }",
			remain:   []map[string]any{param("a", "int")},
			contains: []string{"use.go:4", "side effects"},
		},
		{
			name:     "dropped argument receives from a channel",
			target:   "func f(a, b int) int { return a }",
			use:      "func A(c chan int) int { return f(1, <-c) }",
			remain:   []map[string]any{param("a", "int")},
			contains: []string{"use.go:3", "side effects"},
		},
		{
			name:     "reference is not a call",
			target:   "func f(a, b int) int { return a }",
			use:      "var g = f",
			remain:   []map[string]any{param("a", "int")},
			contains: []string{"use.go:3", "not a call"},
		},
		{
			name:     "unnamed removal is ambiguous",
			target:   "func f(int, int) {}",
			use:      "func A() { f(1, 2) }",
			remain:   []map[string]any{param("", "int")},
			contains: []string{"ambiguous"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fSrc := "package p\n\n" + tt.target + "\n"
			useSrc := "package p\n\n" + tt.use + "\n"
			dir := writeInlineWorkspace(t, map[string]string{"p/f.go": fSrc, "p/use.go": useSrc})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, removeParamArgs("f", "p/f.go", tt.remain...))

			if !res.IsError {
				t.Fatalf("want a refusal, got success: %s", errText(res))
			}
			for _, want := range tt.contains {
				if !strings.Contains(errText(res), want) {
					t.Errorf("error %q does not contain %q", errText(res), want)
				}
			}
			assertFileUnchanged(t, dir, "p/f.go", fSrc)
			assertFileUnchanged(t, dir, "p/use.go", useSrc)
		})
	}
}

const removeParamIface = "package p\n\ntype Runner interface{ Run(n, m int) int }\n\nfunc Use(r Runner) int { return r.Run(1, 2) }\n"

func TestChangeSignature_RemoveParamOnInterfaceMethodUpdatesImplementationsAndCalls(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go": removeParamIface,
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n",
		"p/b.go":     "package p\n\ntype B struct{}\n\nfunc (B) Run(n, m int) int { return n + 1 }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

	assertFileContains(t, dir, "p/iface.go", "Run(n int) int")
	assertFileContains(t, dir, "p/iface.go", "r.Run(1)")
	assertFileContains(t, dir, "p/a.go", "Run(n int) int")
	assertFileContains(t, dir, "p/b.go", "Run(n int) int")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamRefusesWhenImplementationBodyUsesIt(t *testing.T) {
	a := "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n"
	b := "package p\n\ntype B struct{}\n\nfunc (B) Run(n, m int) int { return n + m }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/iface.go": removeParamIface, "p/a.go": a, "p/b.go": b})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	cases := map[string]map[string]any{
		"concrete target with propagate": removeParamArgs("A.Run", "p/a.go", param("n", "int")),
		"interface target":               removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")),
	}
	cases["concrete target with propagate"]["propagate"] = true
	for name, args := range cases {
		res := changeSig(t, sess, args)
		if !res.IsError || !strings.Contains(errText(res), "b.go:5") {
			t.Errorf("%s: want a refusal naming b.go:5, got IsError=%v %q", name, res.IsError, errText(res))
		}
	}
	assertFileUnchanged(t, dir, "p/iface.go", removeParamIface)
	assertFileUnchanged(t, dir, "p/a.go", a)
	assertFileUnchanged(t, dir, "p/b.go", b)
}

func TestChangeSignature_RemoveParamDropsConversionArgument(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f(a int, b int64) int { return a }\n",
		"p/use.go": "package p\n\nfunc Use(x int) int { return f(1, int64(x)) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("a", "int")))

	assertFileContains(t, dir, "p/use.go", "f(1)")
	goVets(t, dir, "")
}

func TestChangeSignature_PropagateRewritesCallsThroughTheInterface(t *testing.T) {
	files := map[string]string{
		"p/iface.go": removeParamIface,
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n",
		"p/b.go":     "package p\n\ntype B struct{}\n\nfunc (B) Run(n, m int) int { return n + 1 }\n",
	}
	t.Run("remove_param", func(t *testing.T) {
		dir := writeInlineWorkspace(t, maps.Clone(files))
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		args := removeParamArgs("A.Run", "p/a.go", param("n", "int"))
		args["propagate"] = true
		changeSigOK(t, sess, args)

		assertFileContains(t, dir, "p/iface.go", "r.Run(1)")
		goVets(t, dir, "")
	})
	t.Run("add_param", func(t *testing.T) {
		dir := writeInlineWorkspace(t, maps.Clone(files))
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		changeSigOK(t, sess, map[string]any{
			"function_name": "A.Run", "source_file": "p/a.go", "subcommand": "add_param",
			"params": intParams("n", "m", "k"), "default_value": "0", "position": 2, "propagate": true,
		})

		assertFileContains(t, dir, "p/iface.go", "r.Run(1, 2, 0)")
		goVets(t, dir, "")
	})
}
