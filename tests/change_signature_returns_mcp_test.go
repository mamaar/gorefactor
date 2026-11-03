package tests_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func returnArgs(fn, file, sub string, returns ...string) map[string]any {
	return map[string]any{"function_name": fn, "source_file": file, "subcommand": sub, "returns": returns}
}

func TestChangeSignature_AddReturnLeavesSameNamedMethodAndItsCallsAlone(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\ntype T struct{}\n\nfunc Get() int { return 1 }\n\nfunc (T) Get() int { return 2 }\n",
		"p/use.go": "package p\n\nfunc Use() int {\n\ta := Get()\n\tb := T{}.Get()\n\treturn a + b\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("Get", "p/f.go", "add_return", "int", "error"))

	assertFileContains(t, dir, "p/f.go", "func Get() (int, error) { return 1, nil }")
	assertFileContains(t, dir, "p/f.go", "func (T) Get() int { return 2 }")
	assertFileContains(t, dir, "p/use.go", "a, _ := Get()")
	assertFileContains(t, dir, "p/use.go", "b := T{}.Get()")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveReturnLeavesSameNamedFunctionInOtherPackageAlone(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc Get() (int, error) { return 1, nil }\n",
		"q/g.go":   "package q\n\nfunc Get() (int, error) { return 2, nil }\n",
		"p/use.go": "package p\n\nimport \"example.com/threadmcp/q\"\n\nfunc Use() int {\n\ta, _ := Get()\n\tb, _ := q.Get()\n\treturn a + b\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("Get", "p/f.go", "remove_return", "int"))

	assertFileContains(t, dir, "p/f.go", "func Get() int { return 1 }")
	assertFileContains(t, dir, "q/g.go", "func Get() (int, error) { return 2, nil }")
	assertFileContains(t, dir, "p/use.go", "a := Get()")
	assertFileContains(t, dir, "p/use.go", "b, _ := q.Get()")
	goVets(t, dir, "")
}

func TestChangeSignature_AddReturnRewritesCallersInOtherPackages(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lib/lib.go": "package lib\n\nfunc Load(n int) string { return \"x\" }\n",
		"app/app.go": "package app\n\nimport \"example.com/threadmcp/lib\"\n\nfunc Run() string {\n\ts := lib.Load(1)\n\tlib.Load(2)\n\treturn s\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("Load", "lib/lib.go", "add_return", "string", "error"))

	assertFileContains(t, dir, "app/app.go", "s, _ := lib.Load(1)")
	assertFileContains(t, dir, "app/app.go", "_, _ = lib.Load(2)")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveReturnDropsErrorFromCallersThatIgnoreIt(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f() (int, error) { return 1, nil }\n",
		"p/use.go": "package p\n\nfunc Use() int {\n\ta, _ := f()\n\tf()\n\tgo f()\n\tdefer f()\n\treturn a\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))

	assertFileContains(t, dir, "p/f.go", "func f() int { return 1 }")
	assertFileContains(t, dir, "p/use.go", "a := f()")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveOnlyReturnTurnsAssignmentsIntoCalls(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nvar n int\n\nfunc f() int { n++; return 0 }\n",
		"p/use.go": "package p\n\nfunc Use() {\n\t_ = f()\n\tf()\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("f", "p/f.go", "remove_return"))

	assertFileContains(t, dir, "p/f.go", "func f() { n++; return }")
	assertFileContains(t, dir, "p/use.go", "\tf()\n\tf()\n")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveReturnRefusesWhenTheRemovedVariableIsReadLater(t *testing.T) {
	f := "package p\n\nfunc f() (int, error) { return 1, nil }\n"
	use := "package p\n\nfunc Use() int {\n\tx, err := f()\n\tprintln(err.Error())\n\treturn x\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))

	if !res.IsError || !strings.Contains(errText(res), "use.go:5") {
		t.Errorf("want a refusal naming use.go:5, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/f.go", f)
	assertFileUnchanged(t, dir, "p/use.go", use)
}

func TestChangeSignature_ReturnChangesRefuseCallsInsideExpressions(t *testing.T) {
	f := "package p\n\nfunc f() (int, error) { return 1, nil }\n\nfunc g(a int, err error) {}\n"
	shapes := map[string]string{
		"return f()":      "package p\n\nfunc A() (int, error) { return f() }\n",
		"argument g(f())": "package p\n\nfunc B() { g(f()) }\n",
		"func value":      "package p\n\nvar h = f\n",
	}
	for name, use := range shapes {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))

			if !res.IsError || !strings.Contains(errText(res), "use.go:") {
				t.Errorf("want a refusal naming use.go, got IsError=%v %q", res.IsError, errText(res))
			}
			assertFileUnchanged(t, dir, "p/f.go", f)
			assertFileUnchanged(t, dir, "p/use.go", use)
		})
	}
}

func TestChangeSignature_AddReturnRefusesCallsInsideExpressions(t *testing.T) {
	f := "package p\n\nfunc f() int { return 1 }\n\nfunc g(a int) {}\n"
	shapes := map[string]string{
		"return f()":      "package p\n\nfunc A() int { return f() }\n",
		"argument g(f())": "package p\n\nfunc B() { g(f()) }\n",
		"operand":         "package p\n\nfunc C() int { y := f() + 1; return y }\n",
		"func value":      "package p\n\nvar h = f\n",
	}
	for name, use := range shapes {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, returnArgs("f", "p/f.go", "add_return", "int", "error"))

			if !res.IsError || !strings.Contains(errText(res), "use.go:") {
				t.Errorf("want a refusal naming use.go, got IsError=%v %q", res.IsError, errText(res))
			}
			assertFileUnchanged(t, dir, "p/f.go", f)
			assertFileUnchanged(t, dir, "p/use.go", use)
		})
	}
}

func TestChangeSignature_ReturnChangesRefuseUnrewritableReturnsInTheTarget(t *testing.T) {
	cases := map[string]struct {
		sub     string
		src     string
		returns []string
	}{
		"bare return with named results": {
			"remove_return", "package p\n\nfunc f() (n int, err error) {\n\tn = 1\n\treturn\n}\n", []string{"int"},
		},
		"forwarded call": {
			"add_return", "package p\n\nfunc g() (int, error) { return 1, nil }\n\nfunc f() (int, error) { return g() }\n", []string{"int", "error", "string"},
		},
		"dropped value with a call": {
			"remove_return", "package p\n\nfunc g() error { return nil }\n\nfunc f() (int, error) { return 1, g() }\n", []string{"int"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/f.go": c.src})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, returnArgs("f", "p/f.go", c.sub, c.returns...))

			if !res.IsError || !strings.Contains(errText(res), "f.go:") {
				t.Errorf("want a refusal naming f.go, got IsError=%v %q", res.IsError, errText(res))
			}
			assertFileUnchanged(t, dir, "p/f.go", c.src)
		})
	}
}

func TestChangeSignature_ReturnChangeListsEveryRefusedSite(t *testing.T) {
	f := "package p\n\nfunc f() int { return 1 }\n\nfunc g(a int) {}\n"
	use := "package p\n\nfunc A() { g(f()) }\n\nfunc B() { g(f()) }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, returnArgs("f", "p/f.go", "add_return", "int", "error"))

	if !res.IsError || !strings.Contains(errText(res), "use.go:3") || !strings.Contains(errText(res), "use.go:5") {
		t.Errorf("want both sites listed, got IsError=%v %q", res.IsError, errText(res))
	}
}

func TestChangeSignature_AddReturnPropagatesToInterfaceAndImplementations(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go": "package p\n\ntype Runner interface{ Run() int }\n",
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run() int { return 1 }\n",
		"p/b.go":     "package p\n\ntype B struct{}\n\nfunc (*B) Run() int { return 2 }\n\nfunc (*B) Other() int { return 3 }\n",
		"p/use.go":   "package p\n\nfunc Use(r Runner, a A, b *B) int {\n\tx := r.Run()\n\ty := a.Run()\n\tz := b.Run()\n\tw := b.Other()\n\treturn x + y + z + w\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := returnArgs("Runner.Run", "p/iface.go", "add_return", "int", "error")
	args["propagate"] = true
	changeSigOK(t, sess, args)

	assertFileContains(t, dir, "p/iface.go", "Run() (int, error)")
	assertFileContains(t, dir, "p/a.go", "return 1, nil")
	assertFileContains(t, dir, "p/b.go", "return 2, nil")
	assertFileContains(t, dir, "p/b.go", "Other() int { return 3 }")
	assertFileContains(t, dir, "p/use.go", "x, _ := r.Run()")
	assertFileContains(t, dir, "p/use.go", "w := b.Other()")
	goVets(t, dir, "")
}

func TestChangeSignature_AddParamWithIllTypedDefaultIsRefusedByTheTypeCheck(t *testing.T) {
	f := "package p\n\nfunc f(n int) int { return n }\n\nfunc Use() int { return f(1) }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	args := map[string]any{
		"function_name": "f", "source_file": "p/f.go", "subcommand": "add_param",
		"params": []map[string]any{param("n", "int"), param("m", "int")}, "default_value": `"x"`, "position": 1,
	}

	t.Run("apply", func(t *testing.T) {
		res := changeSig(t, sess, args)

		if !res.IsError || !strings.Contains(errText(res), "cannot use") {
			t.Errorf("want a refusal listing the type error, got IsError=%v %q", res.IsError, errText(res))
		}
		assertFileUnchanged(t, dir, "p/f.go", f)
	})

	t.Run("dry run", func(t *testing.T) {
		dry := map[string]any{"dry_run": true}
		maps.Copy(dry, args)

		out := decodePlanResult(t, changeSig(t, sess, dry))

		if out.Success || !strings.Contains(out.BlockingIssue, "cannot use") {
			t.Errorf("want success=false and a blocking_issue with the type error, got success=%v %q", out.Success, out.BlockingIssue)
		}
		if len(out.Diffs) == 0 {
			t.Errorf("diffs are empty, want the planned changes")
		}
		assertFileUnchanged(t, dir, "p/f.go", f)
	})
}

func TestChangeSignature_RemoveParamCheckedAcrossPackages(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lib/lib.go": "package lib\n\nfunc F(a, b int) int { return a }\n",
		"app/app.go": "package app\n\nimport \"example.com/threadmcp/lib\"\n\nfunc Run() int { return lib.F(1, 2) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("F", "lib/lib.go", param("a", "int")))

	assertFileContains(t, dir, "app/app.go", "lib.F(1)")
	goVets(t, dir, "")
}

func TestChangeSignature_AddReturnRewritesCallsInTestFiles(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":        "package p\n\nfunc Load() string { return \"x\" }\n",
		"p/f_test.go":   "package p\n\nimport \"testing\"\n\nfunc TestIn(t *testing.T) {\n\ts := Load()\n\t_ = s\n}\n",
		"p/ext_test.go": "package p_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/p\"\n)\n\nfunc TestExt(t *testing.T) {\n\ts := p.Load()\n\t_ = s\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("Load", "p/f.go", "add_return", "string", "error"))

	assertFileContains(t, dir, "p/f_test.go", "s, _ := Load()")
	assertFileContains(t, dir, "p/ext_test.go", "s, _ := p.Load()")
	goVets(t, dir, "")
}

func TestChangeSignature_TypeCheckKeepsTypeIdentityThroughUneditedPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"a/a.go": "package a\n\ntype T struct{}\n\nfunc F(t *T, n int) int { return n }\n",
		"c/c.go": "package c\n\nimport \"example.com/threadmcp/a\"\n\nfunc Get() *a.T { return nil }\n",
		"b/b.go": "package b\n\nimport (\n\t\"example.com/threadmcp/a\"\n\t\"example.com/threadmcp/c\"\n)\n\nfunc Run() int { return a.F(c.Get(), 1) }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	args := map[string]any{
		"function_name": "F", "source_file": "a/a.go", "subcommand": "add_param",
		"params": []map[string]any{param("t", "*T"), param("n", "int"), param("m", "int")}, "default_value": "0", "position": 2,
	}

	changeSigOK(t, sess, args)

	assertFileContains(t, dir, "b/b.go", "a.F(c.Get(), 1, 0)")
	goVets(t, dir, "")
}

func TestChangeSignature_TypeCheckKeepsTypeIdentityThroughTestHelperPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go":         "package p\n\ntype T struct{}\n\nfunc F(t *T) int { return 1 }\n",
		"ptest/ptest.go": "package ptest\n\nimport \"example.com/threadmcp/p\"\n\nfunc Fixture() *p.T { return &p.T{} }\n",
		"p/p_test.go":    "package p_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/p\"\n\t\"example.com/threadmcp/ptest\"\n)\n\nfunc TestF(t *testing.T) {\n\tx := p.F(ptest.Fixture())\n\t_ = x\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("F", "p/p.go", "add_return", "int", "error"))

	assertFileContains(t, dir, "p/p_test.go", "x, _ := p.F(ptest.Fixture())")
	goVets(t, dir, "")
}

func TestChangeSignature_TypeCheckOrdersByProductionImportsOnly(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/p.go":         "package p\n\nfunc F(a, b int) int { return a }\n",
		"ptest/ptest.go": "package ptest\n\nimport \"example.com/threadmcp/p\"\n\nfunc Call() int { return p.F(1, 2) }\n",
		"p/p_test.go":    "package p_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/ptest\"\n)\n\nfunc TestF(t *testing.T) { _ = ptest.Call() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("F", "p/p.go", param("a", "int")))

	assertFileContains(t, dir, "ptest/ptest.go", "p.F(1)")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveReturnRefusesReadsThePositionRuleMisses(t *testing.T) {
	f := "package p\n\nfunc f() (int, error) { return 1, nil }\n"
	cases := map[string]map[string]string{
		"read earlier in a loop": {"p/use.go": "package p\n\nfunc Use() {\n\tvar x int\n\tvar err error\n\tfor i := 0; i < 2; i++ {\n\t\tif err != nil {\n\t\t\treturn\n\t\t}\n\t\tx, err = f()\n\t}\n\t_ = x\n}\n"},
		"deferred closure":       {"p/use.go": "package p\n\nfunc Use() int {\n\tvar x int\n\tvar err error\n\tdefer func() {\n\t\tif err != nil {\n\t\t\tprintln()\n\t\t}\n\t}()\n\tx, err = f()\n\treturn x\n}\n"},
		"named result":           {"p/use.go": "package p\n\nfunc Use() (x int, err error) {\n\tx, err = f()\n\treturn\n}\n"},
		"package variable":       {"p/use.go": "package p\n\nvar gerr error\n\nfunc Set() {\n\tvar x int\n\tx, gerr = f()\n\t_ = x\n}\n", "p/read.go": "package p\n\nfunc Read() bool { return gerr != nil }\n"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			files["p/f.go"] = f
			dir := writeInlineWorkspace(t, files)
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))

			if !res.IsError || !strings.Contains(errText(res), "use.go:") && !strings.Contains(errText(res), "read.go:") {
				t.Errorf("want a refusal naming the site, got IsError=%v %q", res.IsError, errText(res))
			}
			assertFileUnchanged(t, dir, "p/f.go", f)
		})
	}
}

func TestChangeSignature_ReturnChangeKeepsNamesOfRemainingResults(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f() (n int, err error) {\n\tn = 1\n\treturn n, nil\n}\n",
		"p/use.go": "package p\n\nfunc Use() int {\n\tx, _ := f()\n\treturn x\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))

	assertFileContains(t, dir, "p/f.go", "func f() (n int)")
	goVets(t, dir, "")
}

func TestChangeSignature_AddReturnRefusesFunctionWithoutResultsThatNeverReturns(t *testing.T) {
	f := "package p\n\nfunc f() { println() }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, returnArgs("f", "p/f.go", "add_return", "int"))

	if !res.IsError || !strings.Contains(errText(res), "f.go:3") || !strings.Contains(errText(res), "return") || strings.Contains(errText(res), "missing return") {
		t.Errorf("want a refusal naming f.go:3 that is not the raw type error, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/f.go", f)
}

func TestChangeSignature_RemoveReturnTurnsDefineWithNoNewVariableIntoAssign(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/f.go":   "package p\n\nfunc f() (int, error) { return 1, nil }\n",
		"p/use.go": "package p\n\nfunc Use() int {\n\tvar a int\n\ta, err := f()\n\tif err != nil {\n\t\treturn 0\n\t}\n\treturn a\n}\n\nfunc Blank() {\n\t_, err := f()\n\tif err != nil {\n\t\treturn\n\t}\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))

	assertFileContains(t, dir, "p/use.go", "\ta = f()\n")
	assertFileContains(t, dir, "p/use.go", "\t_ = f()\n")
	goVets(t, dir, "")
}
