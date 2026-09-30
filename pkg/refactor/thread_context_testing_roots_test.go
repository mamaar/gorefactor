package refactor

import (
	"strings"
	"testing"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// assertCallersPass fails unless the call site inside caller passes want.
func assertCallersPass(t *testing.T, plan *threadPlan, caller, want string) {
	t.Helper()
	if got := argIn(t, plan, caller); got != want {
		t.Errorf("%s must pass %q, got %q", caller, want, got)
	}
}

func TestThreadContext_TestingParamHelperIsRoot(t *testing.T) {
	cases := []struct {
		name, helper, caller, want string
	}{
		{"T", "func h(t *testing.T) { Seed() }", "func TestX(t *testing.T) { h(t) }", "t.Context()"},
		{"TB", "func h(tb testing.TB) { Seed() }", "func TestX(t *testing.T) { h(t) }", "tb.Context()"},
		{"B", "func h(b *testing.B) { Seed() }", "func BenchmarkX(b *testing.B) { h(b) }", "b.Context()"},
		{"NotFirst", "func h(name string, t *testing.T) { Seed() }", "func TestX(t *testing.T) { h(\"n\", t) }", "t.Context()"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := threadPlanFor(t, map[string]string{
				"p/lib.go":    seedSrc,
				"p/x_test.go": "package p\n\nimport \"testing\"\n\n" + tc.helper + "\n\n" + tc.caller + "\n",
			}, "p/lib.go", "Seed")
			assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
			assertCallersPass(t, plan, "h", tc.want)
			for _, e := range plan.ArgFor {
				if fn := e.Site.Fn; fn != nil && fn.Name.Name != "h" {
					t.Errorf("only the helper's call may be edited, got edit in %s", fn.Name.Name)
				}
			}
		})
	}
}

func TestThreadContext_TestingParamHelperInNonTestFileIsRoot(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/lib.go":    seedSrc,
		"p/h.go":      "package p\n\nimport \"testing\"\n\nfunc h(t *testing.T) { Seed() }\n",
		"p/x_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { h(t) }\n",
	}, "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	assertCallersPass(t, plan, "h", "t.Context()")
}

func TestThreadContext_TestingParamMethodIsRoot(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/lib.go": seedSrc,
		"p/s_test.go": `package p

import "testing"

type suite struct{}

func (s *suite) h(t *testing.T) { Seed() }

func TestX(t *testing.T) { (&suite{}).h(t) }
`,
	}, "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	assertCallersPass(t, plan, "h", "t.Context()")
}

func TestThreadContext_TestingHelperPassesInScopeCtx(t *testing.T) {
	cases := map[string]string{
		"param": "func h(t *testing.T, ctx context.Context) { Seed() }\n\nfunc TestX(t *testing.T) { h(t, context.Background()) }",
		"local": "func h(t *testing.T) {\n\tctx := context.Background()\n\tSeed()\n\t_ = ctx\n}\n\nfunc TestX(t *testing.T) { h(t) }",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			plan := threadPlanFor(t, map[string]string{
				"p/lib.go":    seedSrc,
				"p/h_test.go": "package p\n\nimport (\n\t\"context\"\n\t\"testing\"\n)\n\n" + body + "\n",
			}, "p/lib.go", "Seed")
			assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
			assertNames(t, fakeNames(plan), nil, "Fake")
			assertCallersPass(t, plan, "h", "ctx")
		})
	}
}

func TestThreadContext_UnnamedOrBlankTestingParamIsClimbed(t *testing.T) {
	for _, params := range []string{"*testing.T", "_ *testing.T"} {
		t.Run(params, func(t *testing.T) {
			plan := threadPlanFor(t, map[string]string{
				"p/lib.go":    seedSrc,
				"p/h_test.go": "package p\n\nimport \"testing\"\n\nfunc h(" + params + ") { Seed() }\n\nfunc TestX(t *testing.T) { h(t) }\n",
			}, "p/lib.go", "Seed")
			assertNames(t, gainingNames(plan), []string{"Seed", "h"}, "Funcs")
			assertCallersPass(t, plan, "h", "ctx")
			assertCallersPass(t, plan, "TestX", "t.Context()")
		})
	}
}

func TestThreadContext_SeedWithTestingParamStillGainsCtx(t *testing.T) {
	plan := threadPlanFor(t, map[string]string{
		"p/seed.go":   "package p\n\nimport \"testing\"\n\nfunc Seed(t *testing.T) {}\n",
		"p/x_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { Seed(t) }\n",
	}, "p/seed.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	assertCallersPass(t, plan, "TestX", "t.Context()")
	if strings.Contains(strings.Join(gainingNames(plan), ","), "TestX") {
		t.Error("TestX must not gain a parameter")
	}
}

// renderedFile renders the plan and returns the rewritten content of path.
func renderedFile(t *testing.T, ws *pkgtypes.Workspace, plan *threadPlan, path string) string {
	t.Helper()
	changes, err := renderThreadPlan(ws, plan)
	if err != nil {
		t.Fatalf("renderThreadPlan: %v", err)
	}
	got := renderedContent(t, ws, wsPath(ws, path), changes)
	assertParses(t, path, got)
	return got
}

func TestThreadContext_ShadowedTestingParamHelperIsClimbed(t *testing.T) {
	const helper = "package p\n\nimport \"testing\"\n\nfunc helper(t *testing.T, items []int) {\n\tfor _, t := range items {\n\t\t_ = t\n\t\tSeed()\n\t}\n}\n\nfunc TestX(t *testing.T) { helper(t, nil) }\n"
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		"p/lib.go":    seedSrc,
		"p/x_test.go": helper,
	}, "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed", "helper"}, "Funcs")
	assertCallersPass(t, plan, "helper", "ctx")
	assertCallersPass(t, plan, "TestX", "t.Context()")

	got := renderedFile(t, ws, plan, "p/x_test.go")
	for _, want := range []string{"func helper(ctx context.Context, t *testing.T, items []int)", "Seed(ctx)", "helper(t.Context(), t, nil)"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q:\n%s", want, got)
		}
	}
}

func TestThreadContext_ShadowedTestingParamInTestDeclUsesTODO(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		"p/lib.go": seedSrc,
		"p/x_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tfor _, t := range []int{1} {\n\t\t_ = t\n\t\tSeed()\n\t}\n}\n",
	}, "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	assertCallersPass(t, plan, "TestX", "context.TODO()")
	if got := renderedFile(t, ws, plan, "p/x_test.go"); strings.Contains(got, "t.Context()") {
		t.Errorf("shadowed t must not be used:\n%s", got)
	}
}

func TestThreadContext_ShadowedTestingParamInRunClosureUsesTODO(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		"p/lib.go": seedSrc,
		"p/x_test.go": "package p\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tt.Run(\"a\", func(t *testing.T) {\n\t\tfor _, t := range []int{1} {\n\t\t\t_ = t\n\t\t\tSeed()\n\t\t}\n\t})\n}\n",
	}, "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	got := renderedFile(t, ws, plan, "p/x_test.go")
	if !strings.Contains(got, "Seed(context.TODO())") || strings.Contains(got, "t.Context()") {
		t.Errorf("expected context.TODO() and no t.Context():\n%s", got)
	}
}

func TestThreadContext_HelperWithShadowedAndUnshadowedSites(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		"p/lib.go": seedSrc,
		"p/x_test.go": "package p\n\nimport \"testing\"\n\nfunc helper(t *testing.T, items []int) {\n\tSeed()\n\tfor _, t := range items {\n\t\t_ = t\n\t\tSeed()\n\t}\n}\n\nfunc TestX(t *testing.T) { helper(t, nil) }\n",
	}, "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed", "helper"}, "Funcs")
	got := renderedFile(t, ws, plan, "p/x_test.go")
	if strings.Count(got, "Seed(ctx)") != 2 || strings.Contains(got, "Seed(t.Context())") {
		t.Errorf("both Seed calls must pass the helper's ctx:\n%s", got)
	}
}

func TestThreadContext_UnshadowedTestingParamPassesTContext(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, map[string]string{
		"p/lib.go": seedSrc,
		"p/x_test.go": "package p\n\nimport \"testing\"\n\nfunc helper(t *testing.T, items []int) {\n\tfor _, i := range items {\n\t\t_ = i\n\t\tSeed()\n\t}\n}\n",
	}, "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	if got := renderedFile(t, ws, plan, "p/x_test.go"); !strings.Contains(got, "Seed(t.Context())") {
		t.Errorf("expected t.Context():\n%s", got)
	}
}
