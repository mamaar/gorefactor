package refactor

import (
	"strings"
	"testing"
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
