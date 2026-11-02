package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestChangeSignature_PropagateLeavesSameNameMethodWithOtherSignatureAlone(t *testing.T) {
	c := "package p\n\ntype C struct{}\n\nfunc (C) Run(n string) int { return len(n) }\n\nfunc UseC(c C) int { return c.Run(\"x\") }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go": removeParamIface,
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n",
		"p/c.go":     c,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := removeParamArgs("A.Run", "p/a.go", param("n", "int"))
	args["propagate"] = true
	changeSigOK(t, sess, args)

	assertFileContains(t, dir, "p/iface.go", "Run(n int) int")
	assertFileUnchanged(t, dir, "p/c.go", c)
	goVets(t, dir, "")
}

func TestChangeSignature_PropagateReachesPointerReceiverImplementer(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go": removeParamIface,
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n",
		"p/b.go":     "package p\n\ntype B struct{}\n\nfunc (*B) Run(n, m int) int { return n }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

	assertFileContains(t, dir, "p/b.go", "func (*B) Run(n int) int")
	goVets(t, dir, "")
}

func TestChangeSignature_PropagateReachesImplementerDeclaredOnlyUnderAnotherConfig(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go":   removeParamIface,
		"p/a.go":       "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n",
		"p/b_linux.go": "package p\n\ntype B struct{}\n\nfunc (B) Run(n, m int) int { return n }\n",
		"p/b_other.go": "//go:build !linux\n\npackage p\n\nfunc other() {}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

	assertFileContains(t, dir, "p/b_linux.go", "Run(n int) int")
}

func TestChangeSignature_RefusesConcreteMethodSatisfyingInterfaceWithoutPropagate(t *testing.T) {
	a := "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/iface.go": removeParamIface, "p/a.go": a})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, removeParamArgs("A.Run", "p/a.go", param("n", "int")))

	if !res.IsError || !strings.Contains(errText(res), "Runner") || !strings.Contains(errText(res), "propagate") {
		t.Errorf("want a refusal naming Runner and propagate, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/a.go", a)
}

func TestChangeSignature_RefusesMethodSatisfyingExternalInterfaceEvenWithPropagate(t *testing.T) {
	w := "package p\n\ntype W struct{}\n\nfunc (*W) Write(b []byte) (int, error) { return len(b), nil }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"p/w.go":   w,
		"p/use.go": "package p\n\nimport \"io\"\n\nvar _ io.Writer = (*W)(nil)\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := map[string]any{
		"function_name": "W.Write", "source_file": "p/w.go", "subcommand": "add_param",
		"params": []map[string]any{param("b", "[]byte"), param("n", "int")}, "default_value": "0", "position": 1,
		"propagate": true,
	}
	res := changeSig(t, sess, args)

	if !res.IsError || !strings.Contains(errText(res), "io.Writer") {
		t.Errorf("want a refusal naming io.Writer, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/w.go", w)
}

const promotedIface = "package p\n\ntype Runner interface {\n\tRun(n, m int) int\n\tStop()\n}\n\nfunc Use(r Runner) int { return r.Run(1, 2) }\n"
const promotedImpl = "package p\n\ntype Inner struct{}\n\nfunc (Inner) Run(n, m int) int { return n }\n\ntype Outer struct{ Inner }\n\nfunc (Outer) Stop() {}\n\nvar _ Runner = Outer{}\n"

func TestChangeSignature_RefusesPromotedMethodSatisfyingInterfaceWithoutPropagate(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/iface.go": promotedIface, "p/a.go": promotedImpl})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, removeParamArgs("Inner.Run", "p/a.go", param("n", "int")))

	if !res.IsError || !strings.Contains(errText(res), "Runner") {
		t.Errorf("want a refusal naming Runner, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/a.go", promotedImpl)
}

func TestChangeSignature_PropagateReachesInterfaceSatisfiedThroughPromotion(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/iface.go": promotedIface, "p/a.go": promotedImpl})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := removeParamArgs("Inner.Run", "p/a.go", param("n", "int"))
	args["propagate"] = true
	changeSigOK(t, sess, args)

	assertFileContains(t, dir, "p/iface.go", "Run(n int) int")
	goVets(t, dir, "")
}

func TestChangeSignature_RefusesPromotedMethodSatisfyingExternalInterface(t *testing.T) {
	w := "package p\n\nimport \"sort\"\n\ntype Inner struct{}\n\nfunc (Inner) Len() int { return 0 }\n\n" +
		"type Outer struct{ Inner }\n\nfunc (Outer) Less(i, j int) bool { return false }\n\nfunc (Outer) Swap(i, j int) {}\n\nvar _ sort.Interface = Outer{}\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/w.go": w})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, map[string]any{
		"function_name": "Inner.Len", "source_file": "p/w.go", "subcommand": "add_return",
		"returns": []string{"int", "error"}, "propagate": true,
	})

	if !res.IsError || !strings.Contains(errText(res), "sort.Interface") {
		t.Errorf("want a refusal naming sort.Interface, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/w.go", w)
}

func TestChangeSignature_UpdatesImplementerInExternalTestPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go": removeParamIface,
		"p/a.go":     "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n",
		"p/x_test.go": "package p_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/p\"\n)\n\n" +
			"type fake struct{}\n\nfunc (fake) Run(n, m int) int { return n }\n\nvar _ p.Runner = fake{}\n\nfunc TestX(t *testing.T) {}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

	assertFileContains(t, dir, "p/x_test.go", "func (fake) Run(n int) int")
	goVets(t, dir, "")
}

func TestChangeSignature_RefusesGenericImplementer(t *testing.T) {
	g := "package p\n\ntype G[T any] struct{}\n\nfunc (G[T]) Run(n, m int) int { return n }\n\nvar _ Runner = G[int]{}\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/iface.go": removeParamIface, "p/g.go": g})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, removeParamArgs("Runner.Run", "p/iface.go", param("n", "int")))

	if !res.IsError || !strings.Contains(errText(res), "generic") {
		t.Errorf("want a refusal saying generic implementations are unsupported, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/g.go", g)
}

func TestChangeSignature_ChangesSameNameMethodWithOtherSignatureWithoutPropagate(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/iface.go": removeParamIface,
		"p/c.go":     "package p\n\ntype C struct{}\n\nfunc (C) Run(n, m string) int { return len(n) }\n\nfunc UseC(c C) int { return c.Run(\"x\", \"y\") }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, removeParamArgs("C.Run", "p/c.go", map[string]any{"name": "n", "type": "string"}))

	assertFileContains(t, dir, "p/c.go", "c.Run(\"x\")")
	goVets(t, dir, "")
}

func TestAddContextParameter_RefusesMethodSatisfyingInterfaceWithoutPropagate(t *testing.T) {
	a := "package p\n\ntype A struct{}\n\nfunc (A) Run(n, m int) int { return n }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/iface.go": removeParamIface, "p/a.go": a})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := addContext(t, sess, map[string]any{"function_name": "A.Run", "source_file": "p/a.go"})

	if !res.IsError || !strings.Contains(errText(res), "propagate") {
		t.Errorf("want a refusal mentioning propagate, got IsError=%v %q", res.IsError, errText(res))
	}
	assertFileUnchanged(t, dir, "p/a.go", a)
}
