package refactor

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// writeVerifyTypesWorkspace writes files under a temp module directory and
// loads a workspace through the engine, mirroring writeRenameFieldWorkspace
// in rename_field_test.go. SkipCompilation is true because VerifyPlanTypes
// is the thing under test here, not the engine's own (disk-based,
// destructive) compilation check.
func writeVerifyTypesWorkspace(t *testing.T, files map[string]string) *types.Workspace {
	t.Helper()
	dir := t.TempDir()

	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/verifytest\n\ngo 1.21\n"
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	engine := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}
	return ws
}

// findVerifyFile locates the *types.File in ws whose path ends with rel
// (e.g. "foo/lib.go").
func findVerifyFile(t *testing.T, ws *types.Workspace, rel string) *types.File {
	t.Helper()
	for _, pkg := range ws.Packages {
		for _, group := range []map[string]*types.File{pkg.Files, pkg.TestFiles} {
			for _, f := range group {
				if strings.HasSuffix(f.Path, rel) {
					return f
				}
			}
		}
	}
	t.Fatalf("file %s not found in workspace", rel)
	return nil
}

// replaceChange builds a types.Change that replaces the first occurrence of
// oldText in f's original content with newText, computing byte offsets from
// the actual file content instead of hand-counting them.
func replaceChange(t *testing.T, f *types.File, oldText, newText string) types.Change {
	t.Helper()
	content := string(f.OriginalContent)
	idx := strings.Index(content, oldText)
	if idx < 0 {
		t.Fatalf("substring %q not found in %s", oldText, f.Path)
	}
	return types.Change{
		File:    f.Path,
		Start:   idx,
		End:     idx + len(oldText),
		OldText: oldText,
		NewText: newText,
	}
}

// TestVerifyPlanTypes_IntroducesTypeError covers the core failure mode this
// helper exists to catch: a plan whose change type-checks syntactically but
// not semantically must be reported as NOT OK, with the new error listed.
func TestVerifyPlanTypes_IntroducesTypeError(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib.go": `package foo

func f(x int) {}

func g() {
	f(1)
}
`,
	})

	f := findVerifyFile(t, ws, "foo/lib.go")
	change := replaceChange(t, f, "f(1)", `f("oops")`)
	plan := &types.RefactoringPlan{Changes: []types.Change{change}}

	res := VerifyPlanTypes(ws, plan)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if res.OK() {
		t.Fatalf("expected a new type error, got none")
	}
	if len(res.NewErrors) != 1 {
		t.Fatalf("expected exactly 1 new error, got %d: %v", len(res.NewErrors), res.NewErrors)
	}
	if !strings.Contains(res.NewErrors[0], "oops") {
		t.Errorf("expected the new error to mention the bad argument, got: %s", res.NewErrors[0])
	}
}

func TestVerifyPlanTypes_ExternalTestFile(t *testing.T) {
	files := func() map[string]string {
		return map[string]string{
			"foo/lib.go": "package foo\n\nfunc F(x int) {}\n",
			"foo/lib_ext_test.go": `package foo_test

import "example.com/verifytest/foo"

func g() {
	foo.F(1)
}
`,
		}
	}
	for _, tc := range []struct {
		name, newText string
		wantOK        bool
	}{
		{"type error is reported", `foo.F("oops")`, false},
		{"valid edit is OK", "foo.F(2)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := writeVerifyTypesWorkspace(t, files())
			f := findVerifyFile(t, ws, "foo/lib_ext_test.go")
			plan := &types.RefactoringPlan{Changes: []types.Change{replaceChange(t, f, "foo.F(1)", tc.newText)}}

			res := VerifyPlanTypes(ws, plan)
			if res.Skipped != "" {
				t.Fatalf("unexpected skip: %s", res.Skipped)
			}
			if res.OK() != tc.wantOK {
				t.Fatalf("OK() = %v, want %v; new errors: %v", res.OK(), tc.wantOK, res.NewErrors)
			}
		})
	}
}

func TestVerifyPlanTypes_ExternalTestFilesOnly(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib_ext_test.go": "package foo_test\n\nfunc f(x int) {}\n\nfunc g() {\n\tf(1)\n}\n",
	})
	f := findVerifyFile(t, ws, "foo/lib_ext_test.go")
	plan := &types.RefactoringPlan{Changes: []types.Change{replaceChange(t, f, "f(1)", `f("oops")`)}}

	res := VerifyPlanTypes(ws, plan)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if res.OK() {
		t.Fatal("expected a new type error, got none")
	}
}

// TestVerifyPlanTypes_TypeCorrectChange covers the "plan is fine" path: a
// type-correct edit must report OK with no new errors.
func TestVerifyPlanTypes_TypeCorrectChange(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib.go": `package foo

func f(x int) {}

func g() {
	f(1)
}
`,
	})

	f := findVerifyFile(t, ws, "foo/lib.go")
	change := replaceChange(t, f, "f(1)", "f(2)")
	plan := &types.RefactoringPlan{Changes: []types.Change{change}}

	res := VerifyPlanTypes(ws, plan)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if !res.OK() {
		t.Fatalf("expected OK, got new errors: %v", res.NewErrors)
	}
}

// TestVerifyPlanTypes_BaselineSuppressesPreexistingError is the critical
// baseline-diff test: a package with a pre-existing type error, unrelated to
// and untouched by a type-correct change, must still report OK — the
// pre-existing error belongs to the baseline, not to this plan.
func TestVerifyPlanTypes_BaselineSuppressesPreexistingError(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib.go": `package foo

func f(x int) {}

var bad = f("nope")

func g() {
	f(1)
}
`,
	})

	f := findVerifyFile(t, ws, "foo/lib.go")
	change := replaceChange(t, f, "f(1)", "f(2)")
	plan := &types.RefactoringPlan{Changes: []types.Change{change}}

	res := VerifyPlanTypes(ws, plan)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if !res.OK() {
		t.Fatalf("expected OK (pre-existing error must be suppressed by the baseline), got: %v", res.NewErrors)
	}
}

// TestVerifyPlanTypes_NewErrorAlongsidePreexisting covers the mixed case: a
// package with a pre-existing error plus a change that adds a *different*
// error must report only the new one.
func TestVerifyPlanTypes_NewErrorAlongsidePreexisting(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib.go": `package foo

func f(x int) {}

var bad = f("nope")

func g() {
	f(1)
}
`,
	})

	f := findVerifyFile(t, ws, "foo/lib.go")
	change := replaceChange(t, f, "f(1)", `f("new")`)
	plan := &types.RefactoringPlan{Changes: []types.Change{change}}

	res := VerifyPlanTypes(ws, plan)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if res.OK() {
		t.Fatalf("expected a new type error")
	}
	if len(res.NewErrors) != 1 {
		t.Fatalf("expected exactly 1 new error, got %d: %v", len(res.NewErrors), res.NewErrors)
	}
	for _, e := range res.NewErrors {
		if strings.Contains(e, "nope") {
			t.Fatalf("pre-existing error leaked into NewErrors: %s", e)
		}
	}
	if !strings.Contains(res.NewErrors[0], "new") {
		t.Errorf("expected the new error to mention the new bad argument, got: %s", res.NewErrors[0])
	}
}

// TestVerifyPlanTypes_UntrackedFileIsSkipped covers "no usable type info":
// a plan whose only change targets a file the workspace has never heard of
// (e.g. a not-yet-created file) cannot be verified at all. Skipped must be
// set, and the caller must not read the resulting OK()==true as "verified
// clean" — nothing was actually checked.
func TestVerifyPlanTypes_UntrackedFileIsSkipped(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib.go": `package foo

func f() {}
`,
	})

	plan := &types.RefactoringPlan{Changes: []types.Change{
		{
			File:    filepath.Join(ws.RootPath, "foo", "does_not_exist.go"),
			Start:   0,
			End:     0,
			NewText: "package foo\n",
		},
	}}

	res := VerifyPlanTypes(ws, plan)
	if res.Skipped == "" {
		t.Fatalf("expected Skipped to be set for a change targeting an unknown file")
	}
	if !res.OK() {
		t.Fatalf("expected NewErrors empty since nothing was actually checked, got: %v", res.NewErrors)
	}
}

// TestVerifyPlanTypes_NilWorkspaceIsSkipped covers the other "cannot run at
// all" entry point.
func TestVerifyPlanTypes_NilWorkspaceIsSkipped(t *testing.T) {
	plan := &types.RefactoringPlan{Changes: []types.Change{{File: "x.go", Start: 0, End: 0, NewText: "x"}}}
	res := VerifyPlanTypes(nil, plan)
	if res.Skipped == "" {
		t.Fatalf("expected Skipped to be set for a nil workspace")
	}
}

// TestVerifyPlanTypes_EmptyPlanIsTriviallyOK covers the no-op path: a plan
// with no changes cannot introduce any new errors and does not need a
// baseline check at all.
func TestVerifyPlanTypes_EmptyPlanIsTriviallyOK(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib.go": `package foo

func f() {}
`,
	})

	res := VerifyPlanTypes(ws, &types.RefactoringPlan{})
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if !res.OK() {
		t.Fatalf("expected OK for an empty plan, got: %v", res.NewErrors)
	}
}

// TestVerifyPlanTypesWithImporter_SharedImporterAvoidsSpuriousPackageMismatch
// reproduces the exact bug VerifyPlanTypesWithImporter was added to fix.
//
// The setup: package b exports `func Timeout() time.Duration`, and main
// imports b. If some parser P type-checks main *before* verification runs
// (exactly what EnsureTypeChecked does in the real extract operations), then
// b's *types.Package gets memoized with a signature for Timeout whose
// time.Duration was resolved by P's own importer instance. If verification
// then builds a second, independent importer Q (VerifyPlanTypes's old,
// only behavior) to check a plan that adds `import "time"` to main, Q
// resolves that "time" import itself — a second, distinct *types.Package
// also named "time", because go/types' workspaceImporter.Import returns
// the already-memoized b.TypesPkg as-is (it never re-checks a package once
// pkg.TypesChecked is true — see workspaceImporter.Import) without ever
// reconciling it against Q's own importer. So a call passing b.Timeout()'s
// result (P's time.Duration) to a parameter typed time.Duration (Q's) fails
// go/types' assignability check even though the source is correct — the
// "cannot use d (variable of int64 type time.Duration) as time.Duration
// value" absurdity from the bug report.
func TestVerifyPlanTypesWithImporter_SharedImporterAvoidsSpuriousPackageMismatch(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"b/b.go": `package b

import "time"

func Timeout() time.Duration {
	return time.Second
}
`,
		"main.go": `package main

import "example.com/verifytest/b"

func main() {
	_ = b.Timeout()
}
`,
	})

	// Type-check main with our own parser first, mirroring what
	// ExtractFunctionOperation/ExtractMethodOperation do via op.Parser
	// before ever reaching buildVerifiedExtractPlan. This is what memoizes
	// b.TypesPkg — and, buried inside its Timeout signature, this parser's
	// own resolved "time" package — onto ws.Packages *before* verification
	// runs. Skipping this step means the bug does not reproduce at all: b
	// would only ever be resolved by whichever importer VerifyPlanTypes
	// itself builds, so there would be no second "time" to disagree with.
	parser := analysis.NewParser(slog.New(slog.NewTextHandler(io.Discard, nil)))
	parser.BindWorkspace(ws)
	mainPkg, ok := ws.Packages[ws.RootPath]
	if !ok {
		t.Fatalf("root package not found at %s", ws.RootPath)
	}
	parser.EnsureTypeChecked(ws, mainPkg)
	if mainPkg.TypesErr != nil {
		t.Fatalf("baseline type-check of main failed: %v", mainPkg.TypesErr)
	}

	mainFile := findVerifyFile(t, ws, "main.go")

	importChange := replaceChange(t, mainFile,
		`import "example.com/verifytest/b"`,
		"import (\n\t\"example.com/verifytest/b\"\n\t\"time\"\n)")

	content := string(mainFile.OriginalContent)
	fnIdx := strings.Index(content, "func main")
	if fnIdx < 0 {
		t.Fatalf("could not find %q in main.go", "func main")
	}
	fnChange := types.Change{
		File:        mainFile.Path,
		Start:       fnIdx,
		End:         fnIdx,
		NewText:     "func useTimeout(d time.Duration) {\n\t_ = d\n}\n\n",
		Description: "Add extracted function useTimeout",
	}

	callChange := replaceChange(t, mainFile, "_ = b.Timeout()", "useTimeout(b.Timeout())")

	plan := &types.RefactoringPlan{Changes: []types.Change{importChange, fnChange, callChange}}

	// The fix: verifying with the same importer that already checked ws
	// must be clean.
	good := VerifyPlanTypesWithImporter(ws, plan, parser.Importer())
	if good.Skipped != "" {
		t.Fatalf("unexpected skip with shared importer: %s", good.Skipped)
	}
	if !good.OK() {
		t.Fatalf("expected OK with the shared importer, got spurious error(s): %v", good.NewErrors)
	}

	// Prove this test still exercises the bug: the old behavior (a fresh
	// importer built from scratch, which is what VerifyPlanTypes/a nil
	// importer does) must fail on this exact plan. If this assertion ever
	// stops failing, the reproduction has silently broken (e.g. because
	// EnsureTypeChecked above was skipped, or go/types stopped comparing
	// packages by identity) and this test is no longer testing anything.
	buggy := VerifyPlanTypes(ws, plan)
	if buggy.Skipped != "" {
		t.Fatalf("unexpected skip on the buggy path: %s", buggy.Skipped)
	}
	if buggy.OK() {
		t.Fatalf("expected the fresh-importer path to reproduce the spurious cross-package type error, got none")
	}
	found := false
	for _, e := range buggy.NewErrors {
		if strings.Contains(e, "Duration") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a spurious time.Duration mismatch, got: %v", buggy.NewErrors)
	}
}

func TestVerifyPlanTypes_CgoPackageReportsNoNewErrors(t *testing.T) {
	cfg := analysis.NewBuildConfig("linux", "amd64", true)
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/verifytest\n\ngo 1.21\n",
		"main.go": `package main

/*
#include <stdio.h>
*/
import "C"

func viaC() {
	C.puts(C.CString("x"))
}

func f(x int) {}

func main() {
	f(1)
}
`,
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	engine := CreateEngineWithConfig(&EngineConfig{SkipCompilation: true, BuildConfig: &cfg}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ws, err := engine.LoadWorkspace(dir)
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}

	f := findVerifyFile(t, ws, "main.go")
	plan := &types.RefactoringPlan{Changes: []types.Change{replaceChange(t, f, "f(1)", "f(2)")}}

	res := VerifyPlanTypes(ws, plan)
	if res.Skipped != "" {
		t.Fatalf("unexpected skip: %s", res.Skipped)
	}
	if !res.OK() {
		t.Fatalf("expected OK, got new errors: %v", res.NewErrors)
	}
}

func TestVerifyPlanTypes_PlanThatCannotBeAppliedIsANewErrorNotSkipped(t *testing.T) {
	ws := writeVerifyTypesWorkspace(t, map[string]string{
		"foo/lib.go": "package foo\n\nfunc f(x int) {}\n",
	})

	f := findVerifyFile(t, ws, "foo/lib.go")
	change := replaceChange(t, f, "f(x int)", "f()")
	change.OldText = "f(x  int)" // text the file does not hold
	res := VerifyPlanTypes(ws, &types.RefactoringPlan{Changes: []types.Change{change}})

	if res.Skipped != "" || res.OK() {
		t.Fatalf("Skipped=%q NewErrors=%v, want a new error and no skip", res.Skipped, res.NewErrors)
	}
	if !strings.Contains(res.NewErrors[0], "cannot be applied") {
		t.Errorf("error = %q", res.NewErrors[0])
	}
}
