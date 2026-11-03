package tests_test

import (
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const threadSeedLib = "package p\n\nfunc Seed() {}\n"

// threadVetted threads Seed over files, vets the result with the real
// toolchain and returns the rewritten x_test.go.
func threadVetted(t *testing.T, body string) string {
	t.Helper()
	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod":      "module example.com/threadmcp\n\ngo 1.24\n",
		"p/lib.go":    threadSeedLib,
		"p/x_test.go": "package p\n\nimport \"testing\"\n\n" + body + "\n",
	})

	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", map[string]any{
		"function_name": "Seed", "source_file": "p/lib.go", "thread": true,
	})
	goVets(t, dir, "")

	return fileText(t, dir, "p/x_test.go")
}

func TestAddContextParameter_ThreadTestingRootsVetClean(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
		notWant    []string
	}{
		{
			name: "CleanupHelperGainsCtx",
			body: "func TestX(t *testing.T) {\n\tt.Cleanup(func() { teardown(t) })\n}\n\nfunc teardown(t *testing.T) { Seed() }",
			want: []string{"func teardown(ctx context.Context, t *testing.T)", "teardown(context.TODO(), t)", "Seed(ctx)"},
		},
		{
			name: "CleanupAndDirectCalls",
			body: "func TestX(t *testing.T) {\n\tt.Cleanup(func() { teardown(t) })\n\tteardown(t)\n}\n\nfunc teardown(t *testing.T) { Seed() }",
			want: []string{"teardown(context.TODO(), t)", "teardown(t.Context(), t)"},
		},
		{
			name: "BenchmarkCleanup",
			body: "func BenchmarkX(b *testing.B) {\n\tb.Cleanup(func() { teardown(b) })\n}\n\nfunc teardown(b *testing.B) { Seed() }",
			want: []string{"teardown(context.TODO(), b)"}, notWant: []string{"Context()"},
		},
		{
			name: "TBCleanup",
			body: "func TestX(t *testing.T) { run(t) }\n\nfunc run(tb testing.TB) {\n\ttb.Cleanup(func() { teardown(tb) })\n}\n\nfunc teardown(tb testing.TB) { Seed() }",
			want: []string{"teardown(context.TODO(), tb)"}, notWant: []string{"Context()"},
		},
		{
			name: "SeedInCleanupClosure",
			body: "func TestX(t *testing.T) {\n\tt.Cleanup(func() { Seed() })\n}",
			want: []string{"Seed(context.TODO())"}, notWant: []string{"t.Context()"},
		},
		{
			name: "NonTestingCleanupKeepsHelperRoot",
			body: "type reg struct{}\n\nfunc (reg) Cleanup(f func()) { f() }\n\nfunc TestX(t *testing.T) {\n\tvar r reg\n\tr.Cleanup(func() { teardown(t) })\n}\n\nfunc teardown(t *testing.T) { Seed() }",
			want: []string{"Seed(t.Context())"},
		},
		{
			name: "ShadowedHelperIsClimbed",
			body: "func helper(t *testing.T, items []int) {\n\tfor _, t := range items {\n\t\t_ = t\n\t\tSeed()\n\t}\n}\n\nfunc TestX(t *testing.T) { helper(t, nil) }",
			want: []string{"func helper(ctx context.Context, t *testing.T, items []int)", "Seed(ctx)", "helper(t.Context(), t, nil)"},
		},
		{
			name: "ShadowedInTestDeclUsesTODO",
			body: "func TestX(t *testing.T) {\n\tfor _, t := range []int{1} {\n\t\t_ = t\n\t\tSeed()\n\t}\n}",
			want: []string{"Seed(context.TODO())"}, notWant: []string{"t.Context()"},
		},
		{
			name: "ShadowedInRunClosureUsesTODO",
			body: "func TestX(t *testing.T) {\n\tt.Run(\"a\", func(t *testing.T) {\n\t\tfor _, t := range []int{1} {\n\t\t\t_ = t\n\t\t\tSeed()\n\t\t}\n\t})\n}",
			want: []string{"Seed(context.TODO())"}, notWant: []string{"t.Context()"},
		},
		{
			name: "ShadowedAndUnshadowedSites",
			body: "func helper(t *testing.T, items []int) {\n\tSeed()\n\tfor _, t := range items {\n\t\t_ = t\n\t\tSeed()\n\t}\n}\n\nfunc TestX(t *testing.T) { helper(t, nil) }",
			want: []string{"Seed(ctx)"}, notWant: []string{"Seed(t.Context())"},
		},
		{
			name: "FirstOfTwoParamsShadowed",
			body: "func helper(a *testing.T, b *testing.T, items []int) {\n\tfor _, a := range items {\n\t\t_ = a\n\t\tSeed()\n\t}\n}\n\nfunc TestX(t *testing.T) { helper(t, t, nil) }",
			want: []string{"Seed(b.Context())"}, notWant: []string{"a.Context()", "func helper(ctx"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := threadVetted(t, tc.body)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q:\n%s", w, got)
				}
			}

			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q:\n%s", w, got)
				}
			}
		})
	}
}
