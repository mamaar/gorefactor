package refactor

import (
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

const unnamedIfaceFiles = `package p

type Searcher interface {
	Find(string) (int, error)
}

type fake struct{}

func (fake) Find(string) (int, error) { return 0, nil }

type blank struct{}

func (blank) Find(_ string) (int, error) { return 0, nil }

type real struct{}

func (real) Find(s string) (int, error) { return leaf(s) }

type chained struct{}

func (chained) Find(string) (int, error) { return leaf("x") }

func leaf(s string) (int, error) { return 0, nil }

func Use(s Searcher) { s.Find("x") }
`

func TestThreadContext_UnnamedParametersKeepTheListUniform(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{"p/p.go": unnamedIfaceFiles})

	threadAcrossAndApply(t, eng, "p/p.go", "leaf")

	assertOnce(t, dir, "p/p.go", "\tFind(context.Context, string) (int, error)\n")
	assertOnce(t, dir, "p/p.go", "func (fake) Find(context.Context, string) (int, error)")
	assertOnce(t, dir, "p/p.go", "func (blank) Find(ctx context.Context, _ string) (int, error)")
	assertOnce(t, dir, "p/p.go", "func (real) Find(ctx context.Context, s string) (int, error)")
	assertOnce(t, dir, "p/p.go", "func (chained) Find(ctx context.Context, _ string) (int, error) { return leaf(ctx, \"x\") }")
	vetBuilds(t, dir, nil)
}

func TestThreadContext_UnnamedParametersOfSeedWithEmptyList(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go": "package p\n\nfunc leaf() {}\n\nfunc Use() { leaf() }\n",
	})

	threadAcrossAndApply(t, eng, "p/p.go", "leaf")

	assertOnce(t, dir, "p/p.go", "func leaf(ctx context.Context) {}")
	vetBuilds(t, dir, nil)
}

func TestAddContextParameter_UnnamedParametersGetAnUnnamedContext(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go": "package p\n\nfunc f(int, string) int { return 0 }\n\nfunc Use() int { return f(1, \"\") }\n",
	})

	plan, err := eng.AddContextParameterAcrossConfigs(types.AddContextParameterRequest{FunctionName: "f", SourceFile: dir + "/p/p.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/p.go", "func f(context.Context, int, string) int")
	assertOnce(t, dir, "p/p.go", "f(context.TODO(), 1, \"\")")
	vetBuilds(t, dir, nil)
}

func TestAddContextParameter_UnnamedParametersNameTheContextWhenTheBodyUsesIt(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go": "package p\n\nimport \"context\"\n\nfunc g(ctx context.Context, s string) {}\n\n" +
			"func f(string) {\n\tctx := context.TODO()\n\tg(ctx, \"\")\n}\n",
	})

	plan, err := eng.AddContextParameterAcrossConfigs(types.AddContextParameterRequest{FunctionName: "f", SourceFile: dir + "/p/p.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/p.go", "func f(ctx context.Context, _ string)")
	vetBuilds(t, dir, nil)
}

func TestChangeSignature_AddingNamedParameterToUnnamedListNamesTheRest(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go": "package p\n\nfunc f(int) int { return 0 }\n\nfunc Use() int { return f(1) }\n",
	})

	plan, err := eng.ChangeSignatureAcrossConfigs(ChangeSignatureRequest{
		FunctionName: "f", SourceFile: dir + "/p/p.go", Subcommand: "add_param",
		NewParams:        []Parameter{{Name: "n", Type: "string"}, {Type: "int"}},
		DefaultValue:     `""`,
		NewParamPosition: 0,
		Scope:            types.WorkspaceScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/p.go", "func f(n string, _ int) int")
	vetBuilds(t, dir, nil)
}

func TestAddContextParameter_PropagatedInterfaceMethodWithUnnamedParameters(t *testing.T) {
	eng, dir := loadConfigsEngine(t, map[string]string{
		"p/p.go": "package p\n\ntype I interface {\n\tFind(string) int\n}\n\ntype A struct{}\n\nfunc (A) Find(string) int { return 0 }\n\n" +
			"func Use(a A) int { return a.Find(\"\") }\n",
	})

	plan, err := eng.AddContextParameterAcrossConfigs(types.AddContextParameterRequest{
		FunctionName: "A.Find", SourceFile: dir + "/p/p.go", Propagate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ExecutePlan(plan); err != nil {
		t.Fatal(err)
	}

	assertOnce(t, dir, "p/p.go", "\tFind(context.Context, string) int\n")
	assertOnce(t, dir, "p/p.go", "func (A) Find(context.Context, string) int")
	vetBuilds(t, dir, nil)
}
