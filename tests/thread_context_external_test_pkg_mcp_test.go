package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

func TestAddContextParameter_ThreadExternalTestPackageUsesTestingContext(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{
			name: "NestedInOtherCallsArguments",
			body: "func TestX(t *testing.T) {\n\tcheck(t, p.Seed())\n}\n\nfunc check(t *testing.T, err error) {}",
			want: "check(t, p.Seed(t.Context()))",
		},
		{
			name: "Direct",
			body: "func TestX(t *testing.T) {\n\t_ = p.Seed()\n}",
			want: "p.Seed(t.Context())",
		},
		{
			name: "HelperWithTB",
			body: "func TestX(t *testing.T) { helper(t) }\n\nfunc helper(tb testing.TB) { _ = p.Seed() }",
			want: "p.Seed(tb.Context())",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"go.mod":      "module example.com/threadmcp\n\ngo 1.24\n",
				"p/lib.go":    "package p\n\nfunc Seed() error { return nil }\n",
				"p/x_test.go": "package p_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/threadmcp/p\"\n)\n\n" + tc.body + "\n",
			})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()
			callJSON(t, sess, "add_context_parameter", map[string]any{
				"function_name": "Seed", "source_file": "p/lib.go", "thread": true,
			})
			got := fileText(t, dir, "p/x_test.go")
			if !strings.Contains(got, tc.want) || strings.Contains(got, "context.TODO()") {
				t.Errorf("want %q and no context.TODO() in:\n%s", tc.want, got)
			}
			goVets(t, dir, "")
		})
	}
}
