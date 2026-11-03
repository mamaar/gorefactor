package tests_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const logPkg = "package lg\n\nfunc Warn(msg string) {}\n"

func callSitesArgs(dryRun bool) map[string]any {
	return map[string]any{
		"call_sites_of": []string{"example.com/threadmcp/lg.Warn"},
		"thread":        true,
		"entry_package": "example.com/threadmcp",
		"dry_run":       dryRun,
	}
}

func hasReason(out map[string]any, prefix string) bool {
	for _, r := range skippedReasons(out) {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}

	return false
}

func TestAddContextParameter_CallSitesOfSeedsSharedCallerOnce(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport (\n\t\"context\"\n\n\t\"example.com/threadmcp/p\"\n)\n\nfunc main() { p.Run(); p.Has(context.Background()) }\n",
		"p/p.go": `package p

import (
	"context"

	"example.com/threadmcp/lg"
)

func Run() { a(); b() }

func a() { lg.Warn("a") }

func b() { lg.Warn("b") }

func Has(ctx context.Context) { lg.Warn("has") }
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	got := fileText(t, dir, "p/p.go")
	for _, want := range []string{
		"func Run(ctx context.Context) { a(ctx); b(ctx) }",
		"func a(ctx context.Context) { lg.Warn(\"a\") }",
		"func b(ctx context.Context) { lg.Warn(\"b\") }",
		"func Has(ctx context.Context) { lg.Warn(\"has\") }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("p.go lacks %q:\n%s", want, got)
		}
	}

	m := fileText(t, dir, "main.go")
	if !strings.Contains(m, "p.Run(context.TODO())") {
		t.Errorf("main.go:\n%s", m)
	}

	if out["success"] != true {
		t.Errorf("success = %v", out["success"])
	}

	goBuilds(t, dir, "")
}

func TestAddContextParameter_CallSitesOfClosureCapturesEnclosingCtx(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Spawn() }\n",
		"p/p.go": `package p

import "example.com/threadmcp/lg"

func Spawn() { go func() { lg.Warn("x") }() }
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	got := fileText(t, dir, "p/p.go")
	if !strings.Contains(got, "func Spawn(ctx context.Context) { go func() { lg.Warn(\"x\") }() }") {
		t.Errorf("p.go:\n%s", got)
	}

	m := fileText(t, dir, "main.go")
	if !strings.Contains(m, "p.Spawn(context.TODO())") {
		t.Errorf("main.go:\n%s", m)
	}

	goBuilds(t, dir, "")
}

func TestAddContextParameter_CallSitesOfKeepsOwnContextWithoutCancel(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Refresh(); p.Inline() }\n",
		"p/p.go": `package p

import (
	"context"

	"example.com/threadmcp/lg"
)

func work(ctx context.Context) {}

func Refresh() {
	ctx := context.Background()
	work(ctx)
	lg.Warn("r")
}

func Inline() {
	work(context.TODO())
	lg.Warn("i")
}
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	got := fileText(t, dir, "p/p.go")
	for _, want := range []string{
		"func Refresh(ctx context.Context) {\n\tctx = context.WithoutCancel(ctx)\n\twork(ctx)",
		"func Inline(ctx context.Context) {\n\twork(context.WithoutCancel(ctx))",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("p.go lacks %q:\n%s", want, got)
		}
	}

	goBuilds(t, dir, "")
}

func TestAddContextParameter_CallSitesOfSkipsSeedsThatCannotGainCtx(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go": `package main

import (
	"fmt"

	"example.com/threadmcp/p"
)

func main() {
	fmt.Println(p.T{})
	h := p.Value
	h()
	p.Plain()
}
`,
		"p/p.go": `package p

import "example.com/threadmcp/lg"

type T struct{}

func (T) String() string { lg.Warn("s"); return "t" }

func Value() { lg.Warn("v") }

func Plain() { lg.Warn("p") }
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	got := fileText(t, dir, "p/p.go")
	for _, want := range []string{"func (T) String() string {", "func Value() {", "func Plain(ctx context.Context) {"} {
		if !strings.Contains(got, want) {
			t.Errorf("p.go lacks %q:\n%s", want, got)
		}
	}

	for _, prefix := range []string{"external interface:", "func value:"} {
		if !hasReason(out, prefix) {
			t.Errorf("skipped_sites lacks a %q entry: %v", prefix, skippedReasons(out))
		}
	}

	goBuilds(t, dir, "")
}

func TestAddContextParameter_CallSitesOfReportsInitAndVarInitializerSites(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Plain() }\n",
		"p/p.go": `package p

import "example.com/threadmcp/lg"

func init() { lg.Warn("i") }

var _ = func() int { lg.Warn("v"); return 0 }()

func Plain() { lg.Warn("p") }
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", callSitesArgs(true))

	if out["success"] != true || out["blocking_issue"] != nil {
		t.Errorf("want a clean dry run, got %v", out)
	}

	for _, prefix := range []string{"init:", "var initializer:"} {
		if !hasReason(out, prefix) {
			t.Errorf("skipped_sites lacks a %q entry: %v", prefix, skippedReasons(out))
		}
	}
}

func TestAddContextParameter_CallSitesOfLeavesCallersOutsideEntry(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Run() }\n",
		"p/p.go": `package p

import "example.com/threadmcp/lg"

func Run() { helper() }

func helper() { lg.Warn("h") }

func OtherOnly() { helper() }
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	got := fileText(t, dir, "p/p.go")
	for _, want := range []string{
		"func Run(ctx context.Context) { helper(ctx) }",
		"func OtherOnly() { helper(context.TODO()) }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("p.go lacks %q:\n%s", want, got)
		}
	}

	if !hasReason(out, "outside entry:") {
		t.Errorf("skipped_sites lacks an outside entry: entry: %v", skippedReasons(out))
	}

	goBuilds(t, dir, "")
}

func TestAddContextParameter_CallSitesOfRejectsBadCombinations(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/lg\"\n\nfunc main() { lg.Warn(\"m\") }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	cases := map[string]func(map[string]any){
		"thread":        func(a map[string]any) { delete(a, "thread") },
		"entry_package": func(a map[string]any) { delete(a, "entry_package") },
		"function_name": func(a map[string]any) { a["function_name"] = "main" },
		"unknown":       func(a map[string]any) { a["call_sites_of"] = []string{"example.com/threadmcp/lg.Nope"} },
	}
	for want, mutate := range cases {
		args := callSitesArgs(true)
		mutate(args)

		res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "add_context_parameter", Arguments: args})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}

		if !res.IsError || !strings.Contains(errText(res), want) {
			t.Errorf("%s: want an error naming it, got error=%v %s", want, res.IsError, errText(res))
		}
	}
}

func TestAddContextParameter_CallSitesOfTargetsKeepTheirSignatures(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": "package lg\n\nfunc Error(msg string) {}\n\nfunc Failure(msg string) { Error(msg) }\n",
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Run() }\n",
		"p/p.go": `package p

import "example.com/threadmcp/lg"

func Run() { lg.Failure("x") }
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := callSitesArgs(false)
	args["call_sites_of"] = []string{"example.com/threadmcp/lg.Error", "example.com/threadmcp/lg.Failure"}
	callJSON(t, sess, "add_context_parameter", args)

	got := fileText(t, dir, "lg/lg.go")
	if strings.Contains(got, "context") {
		t.Errorf("a target was changed:\n%s", got)
	}

	got = fileText(t, dir, "p/p.go")
	if !strings.Contains(got, "func Run(ctx context.Context)") {
		t.Errorf("p.go:\n%s", got)
	}

	goBuilds(t, dir, "")
}

func TestAddContextParameter_CallSitesOfInlineRootContextsKeepNoCancelInClosures(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Run() }\n",
		"p/p.go": `package p

import (
	"context"

	"example.com/threadmcp/lg"
)

func work(ctx context.Context) {}

func Run() {
	work(context.Background())
	go func() { work(context.TODO()) }()
	lg.Warn("r")
}
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	got := fileText(t, dir, "p/p.go")
	for _, want := range []string{"work(context.WithoutCancel(ctx))", "go func() { work(context.WithoutCancel(ctx)) }()"} {
		if !strings.Contains(got, want) {
			t.Errorf("p.go lacks %q:\n%s", want, got)
		}
	}

	goBuilds(t, dir, "")
}

func withinArgs(dryRun bool) map[string]any {
	args := callSitesArgs(dryRun)
	args["within_function"] = "example.com/threadmcp/p.Run"

	return args
}

const withinFiles = `package p

import "example.com/threadmcp/lg"

type Doer interface{ Do() }

type Impl struct{}

func (Impl) Do() { lg.Warn("impl") }

func Run(d Doer) { inRun(); shared(); d.Do() }

func Other() { inOther(); shared() }

func inRun() { lg.Warn("r") }

func inOther() { lg.Warn("o") }

func shared() { lg.Warn("s") }
`

func TestAddContextParameter_CallSitesOfWithinFunctionSeedsOnlyItsReach(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Run(p.Impl{}); p.Other() }\n",
		"p/p.go":   withinFiles,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", withinArgs(false))

	got := fileText(t, dir, "p/p.go")
	for _, want := range []string{
		"func (Impl) Do(ctx context.Context)",
		"func Run(ctx context.Context, d Doer)",
		"func inRun(ctx context.Context)",
		"func shared(ctx context.Context)",
		"func Other() { inOther(); shared(context.TODO()) }",
		"func inOther() { lg.Warn(\"o\") }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("p.go lacks %q:\n%s", want, got)
		}
	}

	if !hasReason(out, "outside within:") {
		t.Errorf("skipped_sites lacks an outside within: entry: %v", skippedReasons(out))
	}

	goBuilds(t, dir, "")
}

func TestAddContextParameter_WithinFunctionRequiresCallSitesOf(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nfunc main() {}\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := map[string]any{"function_name": "main", "source_file": "main.go", "thread": true, "within_function": "example.com/threadmcp/p.Run"}

	msg := callExpectingError(t, sess, args)
	if !strings.Contains(msg, "within_function") {
		t.Errorf("error should name within_function: %s", msg)
	}
}

func TestReachableReferences_WithinFunctionKeepsLiveTypesOfWholeProgram(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Run(p.Impl{}); p.Other() }\n",
		"p/p.go":   withinFiles,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "reachable_references", map[string]any{
		"entry_package":   "example.com/threadmcp",
		"targets":         []string{"example.com/threadmcp/lg.Warn"},
		"within_function": "example.com/threadmcp/p.Run",
	})

	var got []string
	for _, s := range out["sites"].([]any) {
		got = append(got, s.(map[string]any)["enclosing"].(string))
	}

	slices.Sort(got)

	want := []string{"Impl.Do", "inRun", "shared"}
	if !slices.Equal(got, want) {
		t.Errorf("enclosing = %v, want %v", got, want)
	}
}

func TestAddContextParameter_CallSitesOfSkipsShadowedContextPackage(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Render() }\n",
		"p/p.go": `package p

import "example.com/threadmcp/lg"

func Render() {
	context := "c"
	_ = context
	go func() { helper() }()
}

func helper() { lg.Warn("h") }
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	got := fileText(t, dir, "p/p.go")
	if strings.Contains(got, "context.TODO()") {
		t.Errorf("emitted context.TODO() where context is shadowed:\n%s", got)
	}

	if !hasReason(out, "shadowed context:") {
		t.Errorf("skipped_sites lacks a shadowed context: entry: %v", skippedReasons(out))
	}
}

func TestReachableReferences_WithinFunctionIgnoresFuncValuesTakenOutsideItsReach(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Setup(); p.Run(); p.Admin() }\n",
		"p/p.go": `package p

import "example.com/threadmcp/lg"

var cb func()

func Setup() { cb = admin }

func admin() { lg.Warn("admin") }

func Admin() { admin() }

func local() { lg.Warn("local") }

func Run() {
	f := local
	f()
	cb()
	lg.Warn("run")
}
`,
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "reachable_references", map[string]any{
		"entry_package":   "example.com/threadmcp",
		"targets":         []string{"example.com/threadmcp/lg.Warn"},
		"within_function": "example.com/threadmcp/p.Run",
	})

	var got []string

	for _, s := range out["sites"].([]any) {
		site := s.(map[string]any)
		got = append(got, site["enclosing"].(string))

		path := site["path"].([]any)
		if path[0] != "p.Run" {
			t.Errorf("path of %v does not start at p.Run: %v", site["enclosing"], path)
		}
	}

	slices.Sort(got)

	want := []string{"Run", "local"}
	if !slices.Equal(got, want) {
		t.Errorf("enclosing = %v, want %v", got, want)
	}
}
