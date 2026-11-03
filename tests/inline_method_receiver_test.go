package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestInlineMethod_SubstitutesTheCallersReceiverForTheMethodsOwn(t *testing.T) {
	const tdef = "package p\n\ntype T struct {\n\tn int\n\tin *T\n}\n\nfunc (x *T) Twice() int { return x.n * 2 }\n\nfunc (x *T) Sum() int { return x.n + x.n }\n\nfunc mk() *T { return &T{} }\n"
	for _, tc := range []struct{ name, use, want string }{
		{"identifier", "func Use(r *T) int { return r.Twice() }", "return r.n * 2 }"},
		{"selector chain", "func Use(r *T) int { return r.in.Twice() }", "return r.in.n * 2 }"},
		{"call receiver used once", "func Use() int { return mk().Twice() }", "return mk().n * 2 }"},
		{"call receiver used twice is bound once", "func Use() int { return mk().Sum() }", "func(x *T) int { return x.n + x.n }(mk())"},
		{"pure receiver used twice", "func Use(r *T) int { return r.in.Sum() }", "return r.in.n + r.in.n }"},
	} {
		method := "Twice"
		if strings.Contains(tc.use, ".Sum()") {
			method = "Sum"
		}
		t.Run(tc.name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/t.go": tdef, "p/use.go": "package p\n\n" + tc.use + "\n"})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			callJSON(t, sess, "inline_method", map[string]any{"method_name": method, "source_struct": "T", "target_file": "p/use.go"})

			assertFileContains(t, dir, "p/use.go", tc.want)
			goVets(t, dir, "")
		})
	}
}

func TestInlineMethod_DoesNotRewriteAFieldNamedLikeTheReceiver(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":   "package p\n\ntype T struct{ t int }\n\nfunc (t T) Get() int { return t.t }\n",
		"p/use.go": "package p\n\nfunc Use(r T) int { return r.Get() }\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "inline_method", map[string]any{"method_name": "Get", "source_struct": "T", "target_file": "p/use.go"})

	assertFileContains(t, dir, "p/use.go", "return r.t }")
	goVets(t, dir, "")
}
