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

func TestChangeSignature_RemoveParamRemovesTheOnlyParameter(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\ntype T struct{}\n\nfunc only(a int) {}\n\nfunc (T) M(a int) {}\n",
		"p/use.go": "package p\n\nfunc Use() { only(1); T{}.M(2) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("only", "p/f.go"))
	changeSigOK(t, sess, removeParamArgs("T.M", "p/f.go"))

	assertFileContains(t, dir, "p/f.go", "func only()")
	assertFileContains(t, dir, "p/f.go", "func (T) M()")
	assertFileContains(t, dir, "p/use.go", "only(); T{}.M()")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamOnFunctionDeclaredInATestFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go":      "package p\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc helper(a, b int) int { return b }\n\nfunc TestIt(t *testing.T) { helper(1, 2) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("helper", "p/p_test.go", param("b", "int")))

	assertFileContains(t, dir, "p/p_test.go", "func helper(b int) int")
	assertFileContains(t, dir, "p/p_test.go", "helper(2)")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamKeepsFuncAndArrayTypedParameters(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f(unused int, cb func(string) error, buf [4]byte) error { _ = buf; return cb(\"\") }\n",
		"p/use.go": "package p\n\nfunc Use() error { return f(1, func(string) error { return nil }, [4]byte{}) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go",
		param("cb", "func(string) error"), param("buf", "[4]byte")))

	assertFileContains(t, dir, "p/f.go", "func f(cb func(string) error, buf [4]byte) error")
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

func TestChangeSignature_ImplementationInTestFileIsRewrittenAndChecked(t *testing.T) {
	a := "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n"

	t.Run("rewritten", func(t *testing.T) {
		dir := writeInlineWorkspace(t, map[string]string{
			"p/iface.go": removeParamIface, "p/a.go": a,
			"p/b_test.go": "package p\n\ntype B struct{}\n\nfunc (B) Run(n, m int) int { return n + 1 }\n",
		})

		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		changeSigOK(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

		assertFileContains(t, dir, "p/b_test.go", "Run(n int) int")
		goVets(t, dir, "")
	})
	t.Run("body use refused", func(t *testing.T) {
		b := "package p\n\ntype B struct{}\n\nfunc (B) Run(n, m int) int { return n + m }\n"
		dir := writeInlineWorkspace(t, map[string]string{"p/iface.go": removeParamIface, "p/a.go": a, "p/b_test.go": b})

		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
		defer sess.Close()

		res := changeSig(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

		if !res.IsError || !strings.Contains(errText(res), "b_test.go:5") {
			t.Errorf("want a refusal naming b_test.go:5, got IsError=%v %q", res.IsError, errText(res))
		}

		assertFileUnchanged(t, dir, "p/b_test.go", b)
	})
}

func TestChangeSignature_GenericTargetIsRefusedForEverySubcommand(t *testing.T) {
	f := "package p\n\nfunc f[T any](x T, n int) int { return n }\n"
	use := "package p\n\nfunc Use() int { return f[string](\"a\", 1) + f(2, 3) }\n"

	subcommands := map[string]map[string]any{
		"add_param":     {"params": []map[string]any{param("x", "T"), param("n", "int"), param("m", "int")}, "default_value": "0", "position": 2},
		"remove_param":  {"params": []map[string]any{param("x", "T")}},
		"add_return":    {"returns": []string{"int", "error"}},
		"remove_return": {"returns": []string{}},
	}
	for sub, extra := range subcommands {
		t.Run(sub, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			args := map[string]any{"function_name": "f", "source_file": "p/f.go", "subcommand": sub}
			maps.Copy(args, extra)

			res := changeSig(t, sess, args)

			if !res.IsError || !strings.Contains(errText(res), "type parameters") {
				t.Errorf("want a refusal mentioning type parameters, got IsError=%v %q", res.IsError, errText(res))
			}

			assertFileUnchanged(t, dir, "p/f.go", f)
			assertFileUnchanged(t, dir, "p/use.go", use)
		})
	}
}

func TestChangeSignature_MethodOnGenericReceiverIsRefused(t *testing.T) {
	f := "package p\n\ntype Stack[T any] struct{}\n\nfunc (s *Stack[T]) Push(x T, n int) int { return n }\n"
	use := "package p\n\nfunc Use() int { return (&Stack[string]{}).Push(\"a\", 1) }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, map[string]any{
		"function_name": "Stack.Push", "source_file": "p/f.go", "subcommand": "remove_param",
		"params": []map[string]any{param("x", "T")},
	})

	if !res.IsError || !strings.Contains(errText(res), "type parameters") {
		t.Errorf("want a refusal mentioning type parameters, got IsError=%v %q", res.IsError, errText(res))
	}

	assertFileUnchanged(t, dir, "p/f.go", f)
	assertFileUnchanged(t, dir, "p/use.go", use)
}

func TestChangeSignature_RemoveParamDeletesTheSpanAndKeepsTheRestOfTheSignature(t *testing.T) {
	tests := []struct {
		name   string
		target string
		remain []map[string]any
		want   string
	}{
		{"multi-line first", "func f(\n\tx int,\n\ty string,\n) string {\n\treturn y\n}", []map[string]any{param("y", "string")},
			"func f(\n\ty string,\n) string"},
		{"multi-line last", "func f(\n\tx int,\n\ty string,\n) int {\n\treturn x\n}", []map[string]any{param("x", "int")},
			"func f(\n\tx int,\n) int"},
		{"multi-line only", "func f(\n\tx int,\n) {\n}", nil, "func f() {"},
		{"comment inside", "func f(x int /* why */, y string) string {\n\treturn y\n}", []map[string]any{param("y", "string")},
			"func f(y string) string"},
		{"comment on kept param", "func f(x int, y string /* kept */) int {\n\treturn x\n}", []map[string]any{param("x", "int")},
			"func f(x int /* kept */) int"},
		{"interface typed", "func f(x int, v interface{ M() }) {\n\t_ = v\n}", []map[string]any{param("v", "interface{ M() }")},
			"func f(v interface{ M() })"},
		{"shared type first", "func f(x, y int) int {\n\treturn y\n}", []map[string]any{param("y", "int")}, "func f(y int) int"},
		{"shared type last", "func f(x, y int) int {\n\treturn x\n}", []map[string]any{param("x", "int")}, "func f(x int) int"},
		{"shared type middle", "func f(a, x, b int) int {\n\treturn a + b\n}", []map[string]any{param("a", "int"), param("b", "int")}, "func f(a, b int) int"},
		{"receive-only channel", "func f(x int, c <-chan int) {\n\t<-c\n}", []map[string]any{param("c", "<-chan int")}, "func f(c <-chan int)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"p/f.go": "package p\n\n" + tc.target + "\n",
			})

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			changeSigOK(t, sess, removeParamArgs("f", "p/f.go", tc.remain...))

			assertFileContains(t, dir, "p/f.go", tc.want)
			goVets(t, dir, "")
		})
	}
}

func TestChangeSignature_RemoveParamKeepsMultiLineSignaturesOfImplementationsAndInterface(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go": "package p\n\ntype Doer interface {\n\tDo(\n\t\tx int,\n\t\ty string, // note\n\t)\n}\n\n" +
			"type One struct{}\n\nfunc (One) Do(\n\tx int, // first\n\ty string,\n) {\n\t_ = y\n}\n\n" +
			"func use(d Doer) { d.Do(1, \"\") }\n\nvar _ Doer = One{}\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := removeParamArgs("Doer.Do", "p/p.go", param("y", "string"))
	args["propagate"] = true
	changeSigOK(t, sess, args)

	assertFileContains(t, dir, "p/p.go", "func (One) Do(\n\t// first\n\ty string,\n)")
	assertFileContains(t, dir, "p/p.go", "d.Do(\"\")")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamResolvesTheTargetByObjectWhenAnExternalTestPackageRedeclaresTheName(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lib/lib.go":      "package lib\n\nfunc helper(y string) {}\n\nfunc Use() { helper(\"\") }\n",
		"lib/ext_test.go": "package lib_test\n\nimport \"testing\"\n\nfunc helper(a int, b string) {}\n\nfunc TestX(t *testing.T) { helper(1, \"\") }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("helper", "lib/ext_test.go", param("a", "int")))

	assertFileContains(t, dir, "lib/ext_test.go", "func helper(a int)")
	assertFileContains(t, dir, "lib/ext_test.go", "helper(1)")
	assertFileContains(t, dir, "lib/lib.go", "func helper(y string)")
	assertFileContains(t, dir, "lib/lib.go", "helper(\"\")")
	goVets(t, dir, "")
}

func TestChangeSignature_TestEntryPointsAreRefusedForEverySubcommand(t *testing.T) {
	files := map[string]string{
		"p/p.go":      "package p\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestIt(t *testing.T) {}\n\nfunc BenchmarkIt(b *testing.B) {}\n\nfunc Testimony(t *testing.T, extra int) {}\n",
	}
	dir := writeInlineWorkspace(t, files)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	for _, args := range []map[string]any{
		removeParamArgs("TestIt", "p/p_test.go"),
		{"function_name": "BenchmarkIt", "source_file": "p/p_test.go", "subcommand": "add_param",
			"params": []map[string]any{param("b", "*testing.B"), param("n", "int")}, "default_value": "0", "position": 1},
		{"function_name": "TestIt", "source_file": "p/p_test.go", "subcommand": "add_return", "returns": []string{"error"}},
	} {
		res := changeSig(t, sess, args)
		if !res.IsError || !strings.Contains(errText(res), "go test entry point") {
			t.Errorf("%v %v: IsError=%v %q, want a go test entry point refusal", args["function_name"], args["subcommand"], res.IsError, errText(res))
		}
	}

	changeSigOK(t, sess, removeParamArgs("Testimony", "p/p_test.go", param("t", "*testing.T")))
}

func TestChangeSignature_AddParamAppliesToAMultiLineSignature(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nfunc f(\n\tx int,\n\ty string,\n) string {\n\treturn y\n}\n\nfunc Use() string { return f(1, \"\") }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, map[string]any{
		"function_name": "f", "source_file": "p/f.go", "subcommand": "add_param",
		"params":        []map[string]any{param("x", "int"), param("y", "string"), param("z", "bool")},
		"default_value": "false", "position": 2,
	})

	assertFileContains(t, dir, "p/f.go", "z bool")
	assertFileContains(t, dir, "p/f.go", "f(1, \"\", false)")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamPrunesImportUsedOnlyByParamType(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nimport \"io\"\n\nfunc f(w io.Writer, n int) int { return n }\n",
		"p/use.go": "package p\n\nfunc Use() int { return f(nil, 1) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("n", "int")))

	assertFileOmits(t, dir, "p/f.go", "\"io\"")
	assertFileContains(t, dir, "p/f.go", "func f(n int) int")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamPrunesImportUsedOnlyByDroppedArgumentInAnotherFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f(n int, s string) int { return len(s) }\n",
		"p/use.go": "package p\n\nimport \"strconv\"\n\nfunc Use() int { return f(strconv.IntSize, \"x\") }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("s", "string")))

	assertFileContains(t, dir, "p/use.go", "f(\"x\")")
	assertFileOmits(t, dir, "p/use.go", "\"strconv\"")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamKeepsImportStillUsedElsewhere(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nimport \"io\"\n\nfunc f(w io.Writer, n int) int { return n }\n\nfunc g() io.Writer { return nil }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("n", "int")))

	assertFileContains(t, dir, "p/f.go", "\"io\"")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamPrunesAliasedImportUsedOnlyByParamType(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nimport stdio \"io\"\n\nfunc f(w stdio.Writer, n int) int { return n }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("n", "int")))

	assertFileOmits(t, dir, "p/f.go", "stdio \"io\"")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamNeverPrunesDotOrBlankImports(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go": "package p\n\nimport (\n\t. \"strings\"\n\t_ \"embed\"\n\t\"io\"\n)\n\nfunc f(w io.Writer, b Builder, n int) int { _ = b; return n }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("b", "Builder"), param("n", "int")))

	assertFileContains(t, dir, "p/f.go", ". \"strings\"")
	assertFileContains(t, dir, "p/f.go", "_ \"embed\"")
	assertFileOmits(t, dir, "p/f.go", "\"io\"")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamPrunesImportOfImplementationInAnotherPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go": "package p\n\nimport \"io\"\n\ntype Runner interface{ Run(w io.Writer, n int) int }\n\nfunc Use(r Runner) int { return r.Run(nil, 1) }\n",
		"q/impl.go":  "package q\n\nimport \"io\"\n\ntype A struct{}\n\nfunc (A) Run(w io.Writer, n int) int { return n }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

	assertFileOmits(t, dir, "p/iface.go", "\"io\"")
	assertFileOmits(t, dir, "q/impl.go", "\"io\"")
	assertFileContains(t, dir, "q/impl.go", "Run(n int) int")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveParamPrunesImportSharedByTwoDroppedArgumentsInOneFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f(n int, s string) int { return len(s) }\n",
		"p/use.go": "package p\n\nimport \"strconv\"\n\nfunc Use() int { return f(strconv.IntSize, \"a\") + f(strconv.IntSize, \"b\") }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("f", "p/f.go", param("s", "string")))

	assertFileContains(t, dir, "p/use.go", "f(\"a\") + f(\"b\")")
	assertFileOmits(t, dir, "p/use.go", "\"strconv\"")
	goVets(t, dir, "")
}
