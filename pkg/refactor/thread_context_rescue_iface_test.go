package refactor

import (
	"strings"
	"testing"
)

// common's test imports helper and helper's test imports common, so every
// importer of common is checked against a production-only copy of it.
func rescuedInterfaceFiles() map[string]string {
	return map[string]string{
		"common/common.go": `package common

type Opts struct{ Refresh bool }

type Source interface {
	Token(service string, opts Opts) (string, error)
}

type Manager struct{}

func (m *Manager) Token(service string, opts Opts) (string, error) { return service, nil }

func Direct(m *Manager) (string, error) { return m.Token("d", Opts{}) }
`,
		"common/common_test.go": `package common

import (
	"testing"

	"example.com/threadtest/helper"
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

	"example.com/threadtest/common"
)

func fetch(src common.Source) (string, error) { return src.Token(Name, common.Opts{}) }

func TestFetch(t *testing.T) {
	if _, err := fetch(&common.Manager{}); err != nil {
		t.Fatal(err)
	}
}
`,
		"app/app.go": `package app

import (
	"context"

	"example.com/threadtest/common"
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
	}
}

// withoutOpts drops the Opts parameter and argument, leaving a signature made
// of basic types only.
var withoutOpts = strings.NewReplacer(", opts Opts", "", ", opts common.Opts", "", ", Opts{}", "", ", common.Opts{}", "")

func TestThreadContext_InterfaceOfPackageSeenThroughProductionOnlyCopy(t *testing.T) {
	variants := map[string]*strings.Replacer{
		"signature of basic types":             withoutOpts,
		"signature naming the package's types": strings.NewReplacer(),
	}
	for name, variant := range variants {
		t.Run(name, func(t *testing.T) {
			files := rescuedInterfaceFiles()
			for rel, src := range files {
				files[rel] = variant.Replace(src)
			}

			eng, dir := loadConfigsEngine(t, files)
			v := eng.WorkspaceSet().Primary()
			v.Parser.EnsureTypeChecked(v.Workspace, v.Workspace.Packages[dir+"/common"])

			threadAcrossAndApply(t, eng, "common/common.go", "Manager.Token")

			for rel, wants := range map[string][]string{
				"common/common.go": {
					"\tToken(ctx context.Context, service string, opts Opts) (string, error)\n",
					"func (m *Manager) Token(ctx context.Context, service string, opts Opts) (string, error)",
					"return m.Token(ctx, \"d\", Opts{})",
				},
				"common/common_test.go": {"src.Token(t.Context(), helper.Name, Opts{})"},
				"helper/helper_test.go": {
					"func fetch(ctx context.Context, src common.Source) (string, error)",
					"return src.Token(ctx, Name, common.Opts{})",
					"fetch(t.Context(), &common.Manager{})",
				},
				"app/app.go": {
					"func (a *App) Load(ctx context.Context) (string, error)",
					"return a.src.Token(ctx, \"x\", common.Opts{})",
					"func Run(ctx context.Context, a *App) (string, error) { return a.Load(ctx) }",
					"func Concrete(ctx context.Context, m *common.Manager) (string, error)",
					"return m.Token(ctx, \"c\", common.Opts{})",
					"func (inProc) Token(ctx context.Context, service string, opts common.Opts) (string, error) {\n\treturn lookup(ctx, service)",
				},
			} {
				for _, want := range wants {
					assertOnce(t, dir, rel, variant.Replace(want))
				}
			}

			vetBuilds(t, dir, linuxBuild)
		})
	}
}
