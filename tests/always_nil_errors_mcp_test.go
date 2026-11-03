package tests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

type alwaysNilJSON struct {
	Function  string   `json:"function"`
	Signature string   `json:"signature"`
	File      string   `json:"file"`
	Line      int      `json:"line"`
	Configs   []string `json:"configs"`
	CallSites int      `json:"call_sites"`
	Fixable   bool     `json:"fixable"`
	Propagate bool     `json:"propagate"`
	Reason    string   `json:"reason"`
}

type alwaysNilReportJSON struct {
	Funcs      []alwaysNilJSON `json:"always_nil_errors"`
	TotalCount int             `json:"total_count"`
}

// detectAlwaysNil runs detect_always_nil_errors on an open session.
func detectAlwaysNil(t *testing.T, sess *mcptest.Session) alwaysNilReportJSON {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "detect_always_nil_errors", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("detect_always_nil_errors: %v", err)
	}
	if res.IsError {
		t.Fatalf("detect_always_nil_errors: %s", errText(res))
	}
	var out alwaysNilReportJSON
	if err := json.Unmarshal([]byte(errText(res)), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, errText(res))
	}
	if out.TotalCount != len(out.Funcs) {
		t.Errorf("total_count %d, want %d", out.TotalCount, len(out.Funcs))
	}
	return out
}

// callAlwaysNilErrors runs detect_always_nil_errors over files.
func callAlwaysNilErrors(t *testing.T, files map[string]string) alwaysNilReportJSON {
	t.Helper()
	files["go.mod"] = "module example.com/nilerr\n\ngo 1.21\n"
	dir := writeInlineWorkspace(t, files)
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	return detectAlwaysNil(t, sess)
}

// findAlwaysNil returns the finding for function.
func findAlwaysNil(t *testing.T, out alwaysNilReportJSON, function string) alwaysNilJSON {
	t.Helper()
	for _, f := range out.Funcs {
		if f.Function == function {
			return f
		}
	}
	t.Fatalf("no finding for %s: %+v", function, out.Funcs)
	return alwaysNilJSON{}
}

// findAlwaysNilIn returns the finding for function declared in file.
func findAlwaysNilIn(t *testing.T, out alwaysNilReportJSON, function, file string) alwaysNilJSON {
	t.Helper()
	for _, f := range out.Funcs {
		if f.Function == function && f.File == file {
			return f
		}
	}
	t.Fatalf("no finding for %s in %s: %+v", function, file, out.Funcs)
	return alwaysNilJSON{}
}

func wantNoAlwaysNil(t *testing.T, out alwaysNilReportJSON, function string) {
	t.Helper()
	for _, f := range out.Funcs {
		if f.Function == function {
			t.Errorf("%s listed, want it not listed: %+v", function, f)
		}
	}
}

func wantNilFixable(t *testing.T, f alwaysNilJSON) {
	t.Helper()
	if !f.Fixable || f.Reason != "" {
		t.Errorf("%s: fixable=%v reason=%q, want fixable", f.Function, f.Fixable, f.Reason)
	}
}

func wantNilRefused(t *testing.T, f alwaysNilJSON, reason string) {
	t.Helper()
	if f.Fixable || !strings.Contains(f.Reason, reason) {
		t.Errorf("%s: fixable=%v reason=%q, want a refusal containing %q", f.Function, f.Fixable, f.Reason, reason)
	}
}

func TestAlwaysNilErrors_ListsFunctionsWhoseEveryReturnIsNil(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func Plain() error { return nil }

func Branch(x bool) error {
	if x {
		return nil
	}
	return nil
}

func Pair() (int, error) { return 1, nil }

func use() {
	Plain()
	Branch(true)
	Pair()
}
`})

	if len(out.Funcs) != 3 {
		t.Fatalf("got %+v, want Plain, Branch and Pair", out.Funcs)
	}
	for _, name := range []string{"Plain", "Branch", "Pair"} {
		wantNilFixable(t, findAlwaysNil(t, out, name))
	}
	f := findAlwaysNil(t, out, "Plain")
	if f.CallSites != 1 || f.File != "a/a.go" || f.Line != 3 {
		t.Errorf("Plain = %+v", f)
	}
}

func TestAlwaysNilErrors_OneNonNilReturnIsNotAFinding(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

import "errors"

var errSentinel = errors.New("sentinel")

func Mixed(x bool) error {
	if x {
		return errSentinel
	}
	return nil
}

func use() { Mixed(true) }
`})

	if len(out.Funcs) != 0 {
		t.Fatalf("got %+v, want none", out.Funcs)
	}
}

func TestAlwaysNilErrors_NestedFuncLitReturningNonNilStillCounts(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

import "errors"

var errSentinel = errors.New("sentinel")

func Outer() error {
	check := func() error { return errSentinel }
	_ = check
	return nil
}
`})

	wantNilFixable(t, findAlwaysNil(t, out, "Outer"))
}

func TestAlwaysNilErrors_NamedErrorWithBareReturnIsNotAFinding(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func Bare() (err error) {
	err = nil
	return
}
`})

	if len(out.Funcs) != 0 {
		t.Fatalf("got %+v, want none", out.Funcs)
	}
}

func TestAlwaysNilErrors_NamedErrorSetInDeferredClosureIsNotAFinding(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

import "fmt"

func F() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("x")
		}
	}()
	return nil
}
`})

	wantNoAlwaysNil(t, out, "F")
}

func TestAlwaysNilErrors_NamedErrorReadAfterAssignmentIsNotAFinding(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func g() error { return nil }

func F() (err error) {
	err = g()
	_ = err
	return nil
}
`})

	wantNoAlwaysNil(t, out, "F")
}

func TestAlwaysNilErrors_ShadowedNilIsNotTheUniverseNil(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func Shadow() error {
	var nil error
	return nil
}
`})

	if len(out.Funcs) != 0 {
		t.Fatalf("got %+v, want none", out.Funcs)
	}
}

func TestAlwaysNilErrors_ForwardingReturnIsNotAFindingButItsCalleeIs(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func g() error { return nil }

func Fwd() error { return g() }
`})

	if len(out.Funcs) != 1 {
		t.Fatalf("got %+v, want g only", out.Funcs)
	}
	wantNilRefused(t, findAlwaysNil(t, out, "g"), "used in an expression")
}

func TestAlwaysNilErrors_FixableWhenCallersCheckTheErrorWithIfErr(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func F() error { return nil }

func Use() error {
	if err := F(); err != nil {
		return err
	}
	return nil
}
`})

	f := findAlwaysNil(t, out, "F")
	wantNilFixable(t, f)
	if f.CallSites != 1 {
		t.Errorf("F: call_sites = %d", f.CallSites)
	}
}

func TestAlwaysNilErrors_FixableFindingRemovesCleanly(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/nilerr\n\ngo 1.21\n",
		"a/a.go": `package a

import "errors"

func F() error { return nil }

func Use() error {
	if err := F(); err != nil {
		return errors.New("x")
	}
	return nil
}
`,
		"a/use_tagx.go": "//go:build tagx\n\npackage a\n\nfunc UseTagx() {\n\tif err := F(); err != nil {\n\t\treturn\n\t}\n}\n",
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	f := findAlwaysNil(t, detectAlwaysNil(t, sess), "F")
	wantNilFixable(t, f)
	if f.CallSites != 2 {
		t.Errorf("F: call_sites = %d, want the default and tagx callers", f.CallSites)
	}

	changeSigOK(t, sess, returnArgs("F", "a/a.go", "remove_return"))

	if strings.Contains(fileText(t, dir, "a/a.go"), `"errors"`) {
		t.Errorf("errors import kept after the folded branch that used it was removed")
	}
	goVets(t, dir, "", "tagx")
}

func TestAlwaysNilErrors_CallerUsingErrorsIsIsNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

import "errors"

var errSentinel = errors.New("sentinel")

func F() error { return nil }

func Use() bool {
	err := F()
	return errors.Is(err, errSentinel)
}
`})

	wantNilRefused(t, findAlwaysNil(t, out, "F"), "is read here")
}

func TestAlwaysNilErrors_CallerInGeneratedFileIsNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{
		"a/a.go":   "package a\n\nfunc F() error { return nil }\n",
		"a/gen.go": genHeader + "package a\n\nfunc GenUse() {\n\tif err := F(); err != nil {\n\t\treturn\n\t}\n}\n",
	})

	wantNilRefused(t, findAlwaysNil(t, out, "F"), "generated")
}

func TestAlwaysNilErrors_FuncValueUseIsNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func Handler() error { return nil }

func apply(f func() error) { _ = f() }

func use() { apply(Handler) }
`})

	wantNilRefused(t, findAlwaysNil(t, out, "Handler"), "func value")
}

func TestAlwaysNilErrors_SatisfyingAWorkspaceInterfaceIsFixableWithPropagate(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

type Flusher interface{ Flush() error }

type C struct{}

func (C) Flush() error { return nil }

func use(c Flusher) { _ = c.Flush() }

func run() { use(C{}) }
`})

	f := findAlwaysNil(t, out, "C.Flush")
	wantNilFixable(t, f)
	if !f.Propagate {
		t.Errorf("C.Flush: propagate = false, want true")
	}
}

func TestAlwaysNilErrors_ImplementerOutsideTheWorkspaceIsNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

import "io"

type W struct{}

func (W) Close() error { return nil }

var _ io.Closer = W{}
`})

	wantNilRefused(t, findAlwaysNil(t, out, "W.Close"), "declared outside the workspace")
}

func TestAlwaysNilErrors_PropagateFindingRemovesCleanly(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod": "module example.com/nilerr\n\ngo 1.21\n",
		"a/a.go": `package a

type Flusher interface{ Flush() error }

type C struct{}

func (C) Flush() error { return nil }

type D struct{}

func (*D) Flush() error { return nil }

func use(c Flusher) { _ = c.Flush() }

func run() { use(C{}); use(&D{}) }
`,
	})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := detectAlwaysNil(t, sess)
	for _, name := range []string{"C.Flush", "D.Flush"} {
		f := findAlwaysNil(t, out, name)
		wantNilFixable(t, f)
		if !f.Propagate {
			t.Errorf("%s: propagate = false, want true", name)
		}
	}

	callJSON(t, sess, "change_signature", map[string]any{
		"function_name": "C.Flush", "source_file": "a/a.go", "subcommand": "remove_return",
		"returns": []string{}, "propagate": true,
	})

	goVets(t, dir, "")
}

func TestAlwaysNilErrors_ImplementerThatKeepsItsErrorIsNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

import "errors"

var errBad = errors.New("bad")

type Flusher interface{ Flush() error }

type C struct{}

func (C) Flush() error { return nil }

type D struct{}

func (D) Flush() error { return errBad }

func use(c Flusher) { _ = c.Flush() }

func run() { use(C{}); use(D{}) }
`})

	wantNilRefused(t, findAlwaysNil(t, out, "C.Flush"), "does not always return nil")
}

func TestAlwaysNilErrors_TestEntryPointIsNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{
		"a/a.go":      "package a\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestIt(t *testing.T) error { return nil }\n\nfunc Helper() error { return nil }\n",
	})

	wantNilRefused(t, findAlwaysNil(t, out, "TestIt"), "test entry point")
	wantNilFixable(t, findAlwaysNil(t, out, "Helper"))
}

func TestAlwaysNilErrors_MergesDeclarationsAcrossBuildConfigurations(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{
		"a/a.go":            "package a\n\nfunc Shared() error { return nil }\n\nfunc use() { Shared() }\n",
		"a/split_linux.go":  "package a\n\nfunc Split() error { return nil }\n",
		"a/split_darwin.go": "package a\n\nimport \"errors\"\n\nvar errSplit = errors.New(\"split\")\n\nfunc Split() error { return errSplit }\n",
		"a/only_linux.go":   "package a\n\nfunc Only() error { return nil }\n",
	})

	wantNilFixable(t, findAlwaysNil(t, out, "Shared"))
	f := findAlwaysNil(t, out, "Shared")
	if len(f.Configs) != 0 {
		t.Errorf("Shared: configs = %v, want none when every configuration declares it", f.Configs)
	}
	wantNoAlwaysNil(t, out, "Split")
	only := findAlwaysNil(t, out, "Only")
	wantNilFixable(t, only)
	if len(only.Configs) == 0 {
		t.Errorf("Only: configs empty, want the configuration declaring it")
	}
}

func TestAlwaysNilErrors_ResultNamesDoNotSplitConfigurations(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{
		"a/sys_linux.go":  "package a\n\nfunc Sys(x int) (n int, err error) { return 0, nil }\n",
		"a/sys_darwin.go": "package a\n\nfunc Sys(x int) (int, error) { return 0, nil }\n",
	})

	if len(out.Funcs) != 2 {
		t.Fatalf("got %+v, want Sys under both configurations", out.Funcs)
	}
	for _, f := range out.Funcs {
		wantNilFixable(t, f)
	}
}

func TestAlwaysNilErrors_MismatchNamesTheCurrentFileSignatureFirst(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{
		"a/sys_linux.go":  "package a\n\nfunc Sys(x int) error { return nil }\n",
		"a/sys_darwin.go": "package a\n\nfunc Sys(s string) error { return nil }\n",
	})

	if len(out.Funcs) != 2 {
		t.Fatalf("got %+v, want Sys under both configurations", out.Funcs)
	}
	wantNilRefused(t, findAlwaysNilIn(t, out, "Sys", "a/sys_linux.go"),
		"in a/sys_linux.go has signature (x int) (error), which differs from (s string) (error)")
	wantNilRefused(t, findAlwaysNilIn(t, out, "Sys", "a/sys_darwin.go"),
		"in a/sys_darwin.go has signature (s string) (error), which differs from (x int) (error)")
}

func TestAlwaysNilErrors_GeneratedDeclarationsAreNotListed(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{
		"a/a.go":     "package a\n\nfunc Plain() error { return nil }\n",
		"a/a_gen.go": genHeader + "package a\n\nfunc Gen() error { return nil }\n",
	})

	if len(out.Funcs) != 1 {
		t.Fatalf("got %+v, want Plain only", out.Funcs)
	}
	wantNilFixable(t, findAlwaysNil(t, out, "Plain"))
}

func TestAlwaysNilErrors_DeclarationInExternalTestPackageIsListed(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{
		"lib/lib.go": "package lib\n",
		"lib/ext_test.go": `package lib_test

import "testing"

func helper() error { return nil }

func TestExt(t *testing.T) {
	helper()
}
`,
	})

	f := findAlwaysNil(t, out, "helper")
	wantNilFixable(t, f)
	if f.CallSites != 1 {
		t.Errorf("helper: call_sites = %d", f.CallSites)
	}
}

func TestAlwaysNilErrors_FunctionWithoutAReturnIsNotListed(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"a/a.go": `package a

func Panics() error { panic("unreachable") }
`})

	if len(out.Funcs) != 0 {
		t.Fatalf("got %+v, want none", out.Funcs)
	}
}
