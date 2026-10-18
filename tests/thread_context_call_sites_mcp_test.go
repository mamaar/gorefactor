package tests_test

import (
	"context"
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
	if m := fileText(t, dir, "main.go"); !strings.Contains(m, "p.Run(context.TODO())") {
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

	if got := fileText(t, dir, "p/p.go"); !strings.Contains(got, "func Spawn(ctx context.Context) { go func() { lg.Warn(\"x\") }() }") {
		t.Errorf("p.go:\n%s", got)
	}
	if m := fileText(t, dir, "main.go"); !strings.Contains(m, "p.Spawn(context.TODO())") {
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
