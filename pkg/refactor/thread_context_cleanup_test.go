package refactor

import (
	"strings"
	"testing"
)

func cleanupFile(body string) map[string]string {
	return map[string]string{
		"p/lib.go":    seedSrc,
		"p/x_test.go": "package p\n\nimport \"testing\"\n\n" + body + "\n",
	}
}

func TestThreadContext_HelperCalledFromCleanupClosureIsClimbed(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, cleanupFile(`func TestX(t *testing.T) {
	t.Cleanup(func() { teardown(t) })
}

func teardown(t *testing.T) { Seed() }`), "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed", "teardown"}, "Funcs")
	assertCallersPass(t, plan, "teardown", "ctx")
	assertCallersPass(t, plan, "TestX", "context.TODO()")

	got := renderedFile(t, ws, plan, "p/x_test.go")
	for _, want := range []string{"func teardown(ctx context.Context, t *testing.T)", "teardown(context.TODO(), t)", "Seed(ctx)"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q:\n%s", want, got)
		}
	}

	if strings.Contains(got, "t.Context()") {
		t.Errorf("nothing in a cleanup path may use t.Context():\n%s", got)
	}
}

func TestThreadContext_HelperCalledFromCleanupAndDirectlyIsClimbed(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, cleanupFile(`func TestX(t *testing.T) {
	t.Cleanup(func() { teardown(t) })
	teardown(t)
}

func teardown(t *testing.T) { Seed() }`), "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed", "teardown"}, "Funcs")

	got := renderedFile(t, ws, plan, "p/x_test.go")
	for _, want := range []string{"teardown(context.TODO(), t)", "teardown(t.Context(), t)"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("want exactly one %q:\n%s", want, got)
		}
	}
}

func TestThreadContext_CleanupReceiverTypes(t *testing.T) {
	cases := map[string]string{
		"B":  "func BenchmarkX(b *testing.B) {\n\tb.Cleanup(func() { teardown(b) })\n}\n\nfunc teardown(b *testing.B) { Seed() }",
		"TB": "func TestX(t *testing.T) { run(t) }\n\nfunc run(tb testing.TB) {\n\ttb.Cleanup(func() { teardown(tb) })\n}\n\nfunc teardown(tb testing.TB) { Seed() }",
		"F":  "func FuzzX(f *testing.F) {\n\tf.Cleanup(func() { teardown(f) })\n}\n\nfunc teardown(f *testing.F) { Seed() }",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			ws, plan := buildThreadPlanWithWorkspace(t, cleanupFile(body), "p/lib.go", "Seed")
			assertCallersPass(t, plan, "teardown", "ctx")

			got := renderedFile(t, ws, plan, "p/x_test.go")
			if !strings.Contains(got, "(context.TODO(), ") || strings.Contains(got, "Context()") {
				t.Errorf("cleanup closure must pass context.TODO():\n%s", got)
			}
		})
	}
}

func TestThreadContext_CleanupMethodOnNonTestingTypeKeepsHelperRoot(t *testing.T) {
	plan := threadPlanFor(t, cleanupFile(`type reg struct{}

func (reg) Cleanup(f func()) { f() }

func TestX(t *testing.T) {
	var r reg
	r.Cleanup(func() { teardown(t) })
}

func teardown(t *testing.T) { Seed() }`), "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	assertCallersPass(t, plan, "teardown", "t.Context()")
}

func TestThreadContext_HelperNeverCalledFromCleanupStaysRoot(t *testing.T) {
	plan := threadPlanFor(t, cleanupFile(`func TestX(t *testing.T) {
	t.Cleanup(func() { other() })
	teardown(t)
}

func other() {}

func teardown(t *testing.T) { Seed() }`), "p/lib.go", "Seed")
	assertNames(t, gainingNames(plan), []string{"Seed"}, "Funcs")
	assertCallersPass(t, plan, "teardown", "t.Context()")
}

func TestThreadContext_SeedDirectlyInCleanupClosureUsesTODO(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, cleanupFile(`func TestX(t *testing.T) {
	t.Cleanup(func() { Seed() })
}`), "p/lib.go", "Seed")

	got := renderedFile(t, ws, plan, "p/x_test.go")
	if !strings.Contains(got, "Seed(context.TODO())") || strings.Contains(got, "t.Context()") {
		t.Errorf("expected context.TODO():\n%s", got)
	}
}

func TestThreadContext_TestingClosureInsideCleanupUsesTODO(t *testing.T) {
	ws, plan := buildThreadPlanWithWorkspace(t, cleanupFile(`func TestX(t *testing.T) {
	t.Cleanup(func() {
		t.Run("a", func(t *testing.T) { Seed() })
	})
}`), "p/lib.go", "Seed")

	got := renderedFile(t, ws, plan, "p/x_test.go")
	if !strings.Contains(got, "Seed(context.TODO())") || strings.Contains(got, "t.Context()") {
		t.Errorf("expected context.TODO():\n%s", got)
	}
}
