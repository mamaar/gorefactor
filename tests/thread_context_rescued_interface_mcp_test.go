package tests_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

// common's test imports helper and helper's test imports common, so importers
// of common are checked against a production-only copy of it.
func TestAddContextParameter_Thread_InterfaceOfPackageWhoseTestsImportACycleBack(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/threadmcp\n\ngo 1.24\n",
		"common/common.go": `package common

type Opts struct{ Refresh bool }

type Source interface {
	Token(service string, opts Opts) (string, error)
}

type Manager struct{}

func (m *Manager) Token(service string, opts Opts) (string, error) { return service, nil }
`,
		"common/common_test.go": `package common

import (
	"testing"

	"example.com/threadmcp/helper"
)

func TestManager(t *testing.T) {
	var src Source = &Manager{}
	if _, err := src.Token(helper.Name, Opts{}); err != nil {
		t.Fatal(err)
	}
}
`,
		"helper/helper.go": "package helper\n\nconst Name = \"h\"\n",
		"helper/helper_test.go": `package helper

import (
	"testing"

	"example.com/threadmcp/common"
)

func TestToken(t *testing.T) {
	var src common.Source = &common.Manager{}
	if _, err := src.Token(Name, common.Opts{}); err != nil {
		t.Fatal(err)
	}
}
`,
		"app/app.go": `package app

import (
	"context"

	"example.com/threadmcp/common"
)

type App struct{ src common.Source }

func New() *App { return &App{src: inProc{}} }

func (a *App) Load() (string, error) { return a.src.Token("x", common.Opts{}) }

func Run(a *App) (string, error) { return a.Load() }

func Concrete(m *common.Manager) (string, error) { return m.Token("c", common.Opts{}) }

type inProc struct{}

func (inProc) Token(service string, opts common.Opts) (string, error) {
	return lookup(context.Background(), service)
}

func lookup(ctx context.Context, service string) (string, error) { return service, ctx.Err() }
`,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", map[string]any{
		"function_name": "Manager.Token", "source_file": filepath.Join(dir, "common/common.go"), "thread": true,
	})

	assertFileContains(t, dir, "common/common.go", "\tToken(ctx context.Context, service string, opts Opts) (string, error)\n")
	assertFileContains(t, dir, "common/common.go", "func (m *Manager) Token(ctx context.Context, service string, opts Opts) (string, error)")
	assertFileContains(t, dir, "common/common_test.go", "src.Token(t.Context(), helper.Name, Opts{})")
	assertFileContains(t, dir, "helper/helper_test.go", "src.Token(t.Context(), Name, common.Opts{})")
	assertFileContains(t, dir, "app/app.go", "func (a *App) Load(ctx context.Context) (string, error) { return a.src.Token(ctx, \"x\", common.Opts{}) }")
	assertFileContains(t, dir, "app/app.go", "func Run(ctx context.Context, a *App) (string, error) { return a.Load(ctx) }")
	assertFileContains(t, dir, "app/app.go", "return m.Token(ctx, \"c\", common.Opts{})")
	assertFileContains(t, dir, "app/app.go", "func (inProc) Token(ctx context.Context, service string, opts common.Opts) (string, error) {\n\treturn lookup(ctx, service)")
	goVets(t, dir, "")
}
