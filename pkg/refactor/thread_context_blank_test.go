package refactor

import (
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestThreadContext_BlankContextParameterIsRenamedAndUsed(t *testing.T) {
	for name, tc := range map[string]struct{ decl, want string }{
		"blank":   {"func A(_ context.Context, n int) { S() }", "func A(ctx context.Context, n int) { S(ctx) }"},
		"unnamed": {"func A(context.Context, int) { S() }", "func A(ctx context.Context, _ int) { S(ctx) }"},
	} {
		t.Run(name, func(t *testing.T) {
			eng, dir := loadConfigsEngine(t, map[string]string{
				"main.go": "package main\n\nimport \"context\"\n\nfunc S() {}\n\n" + tc.decl + "\n\nfunc main() { A(context.TODO(), 1) }\n",
			})

			threadAcrossAndApply(t, eng, "main.go", "S")

			assertOnce(t, dir, "main.go", tc.want)
			vetBuilds(t, dir, linuxBuild)
		})
	}
}

func TestThreadContext_BlankContextParameterWithTakenNameIsRefused(t *testing.T) {
	eng, _ := loadConfigsEngine(t, map[string]string{
		"main.go": "package main\n\nimport \"context\"\n\nfunc S() {}\n\nfunc A(_ context.Context) {\n\tctx := 1\n\t_ = ctx\n\tS()\n}\n\nfunc main() { A(context.TODO()) }\n",
	})

	_, err := eng.ThreadContextAcrossConfigs(types.ThreadContextRequest{FunctionName: "S", SourceFile: "main.go"})

	if err == nil || !strings.Contains(err.Error(), "ctx") {
		t.Errorf("err = %v, want a refusal naming the ctx conflict", err)
	}
}
