package extractsig

import (
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/types"
)

// setup writes files under a temp module, parses the workspace and
// type-checks the package holding target.
//
// Type-checking is explicit and mandatory: this project checks packages
// lazily, so a freshly parsed workspace carries no TypesInfo at all and every
// Uses/Defs lookup in Analyze would come back nil. Tests that forget this get
// a *Refusal instead of an assertion failure, which is the whole point of the
// guard in Analyze.
func setup(t *testing.T, target string, files map[string]string) (*types.Workspace, *types.Package, *types.File) {
	t.Helper()

	dir := t.TempDir()
	all := map[string]string{"go.mod": "module example.com/m\n\ngo 1.21\n"}
	maps.Copy(all, files)
	for rel, content := range all {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	set, err := analysis.LoadWorkspaceSet(dir, analysis.DefaultBuildConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("LoadWorkspaceSet: %v", err)
	}
	parser, ws := set.Primary().Parser, set.Primary().Workspace

	pkgDir := filepath.Join(dir, filepath.Dir(target))
	pkg, ok := ws.Packages[pkgDir]
	if !ok {
		t.Fatalf("package %s not found in workspace (have %v)", pkgDir, slices(ws))
	}
	parser.EnsureTypeChecked(ws, pkg)
	if pkg.TypesErr != nil {
		t.Fatalf("type-checking %s failed: %v", pkg.ImportPath, pkg.TypesErr)
	}
	if pkg.TypesInfo == nil || pkg.TypesPkg == nil {
		t.Fatal("expected TypesInfo and TypesPkg after EnsureTypeChecked")
	}

	file, ok := pkg.Files[filepath.Base(target)]
	if !ok {
		t.Fatalf("file %s not found in package", target)
	}
	return ws, pkg, file
}

func slices(ws *types.Workspace) []string {
	var out []string
	for p := range ws.Packages {
		out = append(out, p)
	}
	return out
}

// markLine returns the 1-based line number of the unique line containing mark.
// Hard-coded line numbers in fixtures rot the moment a line is added; marks do
// not.
func markLine(t *testing.T, src, mark string) int {
	t.Helper()
	found := 0
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, mark) {
			if found != 0 {
				t.Fatalf("mark %q appears on lines %d and %d", mark, found, i+1)
			}
			found = i + 1
		}
	}
	if found == 0 {
		t.Fatalf("mark %q not found in source", mark)
	}
	return found
}

type sigCase struct {
	name  string
	src   string            // content of app/app.go
	extra map[string]string // additional files, relative paths

	startMark string
	endMark   string // empty means startMark
	endOffset int    // lines to add to the end line

	wantParams       []Param
	wantResults      []Result
	wantTail         bool
	wantEnclosing    []Result
	wantImports      map[string]string
	wantBodyContains []string
	wantBodyAbsent   []string
}

func TestAnalyze(t *testing.T) {
	cases := []sigCase{
		{
			// The headline case. Every one of the old syntactic analyser's
			// defects shows up here at once: orDefault is a package-level func
			// (must not be a param), Kind/Label/Checks are field
			// names reached through a selector (must not be params), res is a
			// struct *value* whose field is reassigned (must be a result),
			// and the local types must render bare rather than as
			// interface{} or map[string]string.
			name: "package level func and selector fields are not params",
			src: `package app

type scanReport struct {
	Checks []string
}

type sourceInfo struct {
	Kind string
}

type feedInfo struct {
	Label string
}

func orDefault(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func analyze(auth sourceInfo, feed feedInfo) scanReport {
	var res scanReport
	res.Checks = append(res.Checks,
		"kind="+orDefault(auth.Kind, "<empty>"),
		"label="+feed.Label,
	)
	return res
}
`,
			startMark: "res.Checks = append(res.Checks,",
			endOffset: 3,
			wantParams: []Param{
				{Name: "res", Type: "scanReport"},
				{Name: "auth", Type: "sourceInfo"},
				{Name: "feed", Type: "feedInfo"},
			},
			wantResults:      []Result{{Name: "res", Type: "scanReport"}},
			wantBodyContains: []string{"res.Checks = append(res.Checks,"},
		},
		{
			name: "method name reached through a selector is not a param",
			src: `package app

type box struct{ n int }

func (b *box) doThing() int { return b.n }

func run(x *box) int {
	total := 0
	total += x.doThing()
	return total
}
`,
			startMark: "total += x.doThing()",
			wantParams: []Param{
				{Name: "total", Type: "int"},
				{Name: "x", Type: "*box"},
			},
			wantResults: []Result{{Name: "total", Type: "int"}},
		},
		{
			name: "package-level vars and consts are not params",
			src: `package app

var defaultPrefix = "p:"

const limit = 10

func run(name string) string {
	out := ""
	if len(name) < limit {
		out = defaultPrefix + name
	}
	return out
}
`,
			startMark: "if len(name) < limit {",
			endOffset: 2,
			wantParams: []Param{
				{Name: "name", Type: "string"},
				{Name: "out", Type: "string"},
			},
			wantResults: []Result{{Name: "out", Type: "string"}},
		},
		{
			name: "builtins are not params",
			src: `package app

func run(items []string) []string {
	var out []string
	if len(items) > 0 {
		out = append(out, items[0])
	}
	return out
}
`,
			startMark: "if len(items) > 0 {",
			endOffset: 2,
			wantParams: []Param{
				{Name: "items", Type: "[]string"},
				{Name: "out", Type: "[]string"},
			},
			wantResults:    []Result{{Name: "out", Type: "[]string"}},
			wantBodyAbsent: []string{"\n\tif len"}, // dedented: no leading tab
		},
		{
			name: "import qualifier is not a param and an imported type renders qualified",
			extra: map[string]string{
				"other/other.go": `package other

type Config struct {
	Prefix string
}
`,
			},
			src: `package app

import (
	"strings"

	"example.com/m/other"
)

func check(cfg other.Config, name string) bool {
	ok := false
	if strings.HasPrefix(name, cfg.Prefix) {
		ok = true
	}
	return ok
}
`,
			startMark: "if strings.HasPrefix(name, cfg.Prefix) {",
			endOffset: 2,
			wantParams: []Param{
				{Name: "name", Type: "string"},
				{Name: "cfg", Type: "other.Config"},
				{Name: "ok", Type: "bool"},
			},
			wantResults: []Result{{Name: "ok", Type: "bool"}},
			// The file already imports other, so nothing needs adding.
			wantImports: map[string]string{},
		},
		{
			// The type comes from a package this *file* does not import (a
			// sibling file in the same package does), so the rendered type
			// needs an import recorded alongside it.
			name: "a type from a package the file does not import is recorded",
			extra: map[string]string{
				"other/other.go": `package other

type Config struct {
	Prefix string
}
`,
				"app/helper.go": `package app

import "example.com/m/other"

func newConfig() other.Config { return other.Config{} }
`,
			},
			src: `package app

func use(name string) string {
	cfg := newConfig()
	out := ""
	if cfg.Prefix != "" {
		out = cfg.Prefix + name
	}
	return out
}
`,
			startMark: `if cfg.Prefix != "" {`,
			endOffset: 2,
			wantParams: []Param{
				{Name: "cfg", Type: "other.Config"},
				{Name: "out", Type: "string"},
				{Name: "name", Type: "string"},
			},
			wantResults: []Result{{Name: "out", Type: "string"}},
			wantImports: map[string]string{"example.com/m/other": "other"},
		},
		{
			// A rendered type must use the alias the source file already
			// chose, not the package's own name.
			name: "an aliased import renders under its alias",
			src: `package app

import str "strings"

func build(parts []string) string {
	var b str.Builder
	for _, p := range parts {
		b.WriteString(p)
	}
	return b.String()
}
`,
			startMark: "for _, p := range parts {",
			endOffset: 2,
			wantParams: []Param{
				{Name: "parts", Type: "[]string"},
				{Name: "b", Type: "str.Builder"},
			},
			// WriteString has a pointer receiver and b is an addressable
			// value, so the call implicitly takes &b — the mutation is lost
			// unless b travels back out.
			wantResults: []Result{{Name: "b", Type: "str.Builder"}},
			wantImports: map[string]string{},
		},
		{
			name: "mutation through a pointer or map is not forced into results",
			src: `package app

type Result struct {
	Items []string
}

func fill(r *Result, seen map[string]bool, data []string) {
	for _, d := range data {
		r.Items = append(r.Items, d)
		seen[d] = true
	}
}
`,
			startMark: "for _, d := range data {",
			endOffset: 3,
			wantParams: []Param{
				{Name: "data", Type: "[]string"},
				{Name: "r", Type: "*Result"},
				{Name: "seen", Type: "map[string]bool"},
			},
			wantResults: nil,
		},
		{
			name: "a variable declared in the block and read afterwards is returned",
			src: `package app

func run() int {
	count := 3
	doubled := count * 2
	return doubled + count
}
`,
			startMark:   "doubled := count * 2",
			wantParams:  []Param{{Name: "count", Type: "int"}},
			wantResults: []Result{{Name: "doubled", Type: "int"}},
		},
		{
			name: "a variable declared in the block and unused afterwards is not returned",
			src: `package app

func run() int {
	count := 3
	doubled := count * 2
	_ = doubled
	return count
}
`,
			startMark:   "doubled := count * 2",
			endMark:     "_ = doubled",
			wantParams:  []Param{{Name: "count", Type: "int"}},
			wantResults: nil,
		},
		{
			name: "early returns that always terminate become tail returns",
			src: `package app

import "errors"

func classify(n int) (string, error) {
	if n < 0 {
		return "", errors.New("negative")
	}
	if n == 0 {
		return "zero", nil
	}
	return "positive", nil
}
`,
			startMark:  "if n < 0 {",
			endMark:    `return "positive", nil`,
			wantParams: []Param{{Name: "n", Type: "int"}},
			wantTail:   true,
			wantEnclosing: []Result{
				{Name: "", Type: "string"},
				{Name: "", Type: "error"},
			},
		},
		{
			name: "named results of the enclosing function keep their names",
			src: `package app

func classify(n int) (label string, err error) {
	if n < 0 {
		return "neg", nil
	}
	return "pos", nil
}
`,
			startMark:  "if n < 0 {",
			endMark:    `return "pos", nil`,
			wantParams: []Param{{Name: "n", Type: "int"}},
			wantTail:   true,
			wantEnclosing: []Result{
				{Name: "label", Type: "string"},
				{Name: "err", Type: "error"},
			},
		},
		{
			// panic is a terminating statement, so an if/else where one arm
			// returns and the other panics still always terminates.
			name: "panic counts as a terminating path",
			src: `package app

func pick(n int) string {
	if n > 0 {
		return "pos"
	} else {
		panic("bad")
	}
}
`,
			startMark:  "if n > 0 {",
			endOffset:  4,
			wantParams: []Param{{Name: "n", Type: "int"}},
			wantTail:   true,
			wantEnclosing: []Result{
				{Name: "", Type: "string"},
			},
		},
		{
			// A closure body is still part of the block, so variables it
			// captures are parameters of the extracted function too.
			name: "variables captured by a nested closure are params",
			src: `package app

func run(factor int) int {
	total := 0
	apply := func(v int) int { return v * factor }
	total = apply(2)
	return total
}
`,
			startMark: "apply := func(v int) int { return v * factor }",
			endMark:   "total = apply(2)",
			wantParams: []Param{
				{Name: "factor", Type: "int"},
				{Name: "total", Type: "int"},
			},
			wantResults: []Result{{Name: "total", Type: "int"}},
		},
		{
			// Composite-literal keys are field names for a struct literal but
			// ordinary expressions for a map literal; only the latter can be
			// a captured variable.
			name: "struct literal keys are not params but map literal keys are",
			src: `package app

type point struct {
	X int
	Y int
}

func build(X int, key string, val int) (point, map[string]int) {
	p := point{X: 1, Y: 2}
	m := map[string]int{key: val}
	return p, m
}
`,
			startMark: "p := point{X: 1, Y: 2}",
			endMark:   "m := map[string]int{key: val}",
			wantParams: []Param{
				{Name: "key", Type: "string"},
				{Name: "val", Type: "int"},
			},
			wantResults: []Result{
				{Name: "p", Type: "point"},
				{Name: "m", Type: "map[string]int"},
			},
		},
		{
			name: "taking the address of a local forces it into results",
			src: `package app

func fill(sink func(*int)) int {
	n := 0
	sink(&n)
	return n
}
`,
			startMark:   "sink(&n)",
			wantParams:  []Param{{Name: "sink", Type: "func(*int)"}, {Name: "n", Type: "int"}},
			wantResults: []Result{{Name: "n", Type: "int"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"app/app.go": tc.src}
			maps.Copy(files, tc.extra)
			ws, pkg, file := setup(t, "app/app.go", files)

			start := markLine(t, tc.src, tc.startMark)
			end := start + tc.endOffset
			if tc.endMark != "" {
				end = markLine(t, tc.src, tc.endMark) + tc.endOffset
			}

			got, err := Analyze(ws, pkg, file, start, end)
			if err != nil {
				t.Fatalf("Analyze(%d, %d): %v", start, end, err)
			}

			if !reflect.DeepEqual(got.Params, tc.wantParams) {
				t.Errorf("Params = %+v, want %+v", got.Params, tc.wantParams)
			}
			if !reflect.DeepEqual(got.Results, tc.wantResults) {
				t.Errorf("Results = %+v, want %+v", got.Results, tc.wantResults)
			}
			if got.TailReturns != tc.wantTail {
				t.Errorf("TailReturns = %v, want %v", got.TailReturns, tc.wantTail)
			}
			if !reflect.DeepEqual(got.EnclosingResults, tc.wantEnclosing) {
				t.Errorf("EnclosingResults = %+v, want %+v", got.EnclosingResults, tc.wantEnclosing)
			}
			if tc.wantImports != nil && !reflect.DeepEqual(got.Imports, tc.wantImports) {
				t.Errorf("Imports = %v, want %v", got.Imports, tc.wantImports)
			}
			if got.Imports == nil {
				t.Error("Imports must never be nil")
			}
			if got.EnclosingFunc == nil {
				t.Error("EnclosingFunc must be set")
			}
			if len(got.Stmts) == 0 {
				t.Error("Stmts must be set")
			}

			// interface{} is the old analyser's give-up value; it must never
			// appear in a signature this package produces.
			for _, p := range got.Params {
				if strings.Contains(p.Type, "interface{}") {
					t.Errorf("param %s rendered as %s", p.Name, p.Type)
				}
			}
			for _, r := range got.Results {
				if strings.Contains(r.Type, "interface{}") {
					t.Errorf("result %s rendered as %s", r.Name, r.Type)
				}
			}

			for _, want := range tc.wantBodyContains {
				if !strings.Contains(got.BodyText, want) {
					t.Errorf("BodyText missing %q:\n%s", want, got.BodyText)
				}
			}
			for _, absent := range tc.wantBodyAbsent {
				if strings.Contains(got.BodyText, absent) {
					t.Errorf("BodyText unexpectedly contains %q:\n%s", absent, got.BodyText)
				}
			}
		})
	}
}

// TestAnalyzeParamOrderIsSourceOrder pins the ordering contract: parameters
// follow first appearance in the block, not alphabetical order. The call site
// generated by the caller depends on it.
func TestAnalyzeParamOrderIsSourceOrder(t *testing.T) {
	src := `package app

func run(zebra, alpha, middle int) int {
	sum := 0
	sum = zebra + alpha + middle
	return sum
}
`
	ws, pkg, file := setup(t, "app/app.go", map[string]string{"app/app.go": src})
	line := markLine(t, src, "sum = zebra + alpha + middle")

	got, err := Analyze(ws, pkg, file, line, line)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	var names []string
	for _, p := range got.Params {
		names = append(names, p.Name)
	}
	want := []string{"sum", "zebra", "alpha", "middle"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("param order = %v, want %v", names, want)
	}
}

// TestAnalyzeBodyTextDedent checks that the extracted body loses the
// indentation of its old nesting depth but keeps its internal shape.
func TestAnalyzeBodyTextDedent(t *testing.T) {
	src := `package app

func run(items []string) int {
	n := 0
	for _, it := range items {
		if it != "" {
			n++
		}
	}
	return n
}
`
	ws, pkg, file := setup(t, "app/app.go", map[string]string{"app/app.go": src})
	start := markLine(t, src, "for _, it := range items {")
	end := start + 4

	got, err := Analyze(ws, pkg, file, start, end)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	want := "for _, it := range items {\n\tif it != \"\" {\n\t\tn++\n\t}\n}"
	if got.BodyText != want {
		t.Errorf("BodyText =\n%q\nwant\n%q", got.BodyText, want)
	}
}

func TestAnalyzeRefusals(t *testing.T) {
	cases := []struct {
		name      string
		src       string
		startMark string
		endMark   string
		endOffset int
		// startLine/endLine override the marks entirely when non-zero.
		startLine, endLine int
		wantReason         string // substring
	}{
		{
			name: "early returns with fall-through",
			src: `package app

import "errors"

func classify(n int) (string, error) {
	if n < 0 {
		return "", errors.New("negative")
	}
	return "positive", nil
}
`,
			startMark:  "if n < 0 {",
			endOffset:  2,
			wantReason: "fall-through",
		},
		{
			name: "range not aligned to statement boundaries",
			src: `package app

func run(n int) int {
	if n < 0 {
		n = -n
	}
	return n
}
`,
			startMark:  "n = -n",
			endOffset:  1, // ends on the closing brace of the if
			wantReason: "does not align to statement boundaries",
		},
		{
			name: "continue that would escape the extracted function",
			src: `package app

func run(items []string) int {
	n := 0
	for _, it := range items {
		if it == "" {
			continue
		}
		n++
	}
	return n
}
`,
			startMark:  `if it == "" {`,
			endOffset:  2,
			wantReason: "`continue` that would escape",
		},
		{
			name: "break that would escape the extracted function",
			src: `package app

func run(items []string) int {
	n := 0
	for _, it := range items {
		if it == "" {
			break
		}
		n++
	}
	return n
}
`,
			startMark:  `if it == "" {`,
			endOffset:  2,
			wantReason: "`break` that would escape",
		},
		{
			name: "goto whose label is outside the selection",
			src: `package app

func run(n int) int {
	if n < 0 {
		goto done
	}
	n = n * 2
done:
	return n
}
`,
			startMark:  "if n < 0 {",
			endOffset:  2,
			wantReason: "`goto` whose target label is outside",
		},
		{
			name: "block declares a named type",
			src: `package app

func run() int {
	type local struct{ n int }
	v := local{n: 1}
	return v.n
}
`,
			startMark:  "type local struct{ n int }",
			endMark:    "v := local{n: 1}",
			wantReason: "declares a named type",
		},
		{
			name: "line range outside any function",
			src: `package app

var top = 1

func run() int {
	return top
}
`,
			startLine:  3,
			endLine:    3,
			wantReason: "not inside a function body",
		},
		{
			name: "line range crossing a function boundary",
			src: `package app

func a() int {
	return 1
}

func b() int {
	return 2
}
`,
			startMark:  "return 1",
			endOffset:  4,
			wantReason: "crosses the boundary of function",
		},
		{
			name: "returns inside a function literal cannot be extracted",
			src: `package app

func run() func() int {
	n := 1
	return func() int {
		if n > 0 {
			return n
		}
		return 0
	}
}
`,
			startMark:  "if n > 0 {",
			endMark:    "return 0",
			wantReason: "function literal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, pkg, file := setup(t, "app/app.go", map[string]string{"app/app.go": tc.src})

			start, end := tc.startLine, tc.endLine
			if start == 0 {
				start = markLine(t, tc.src, tc.startMark)
				end = start + tc.endOffset
				if tc.endMark != "" {
					end = markLine(t, tc.src, tc.endMark) + tc.endOffset
				}
			}

			got, err := Analyze(ws, pkg, file, start, end)
			if err == nil {
				t.Fatalf("expected a refusal, got analysis %+v", got)
			}
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("expected *Refusal, got %T: %v", err, err)
			}
			if !strings.Contains(refusal.Reason, tc.wantReason) {
				t.Errorf("refusal reason %q does not contain %q", refusal.Reason, tc.wantReason)
			}
		})
	}
}

// TestAnalyzeWithoutTypeInfo is the guard against the failure mode this
// package exists to prevent: without type information every object lookup
// returns nil, so an analysis would come back empty and plausible instead of
// absent.
func TestAnalyzeWithoutTypeInfo(t *testing.T) {
	src := `package app

func run(n int) int {
	n = n * 2
	return n
}
`
	ws, pkg, file := setup(t, "app/app.go", map[string]string{"app/app.go": src})

	pkg.TypesInfo = nil
	line := markLine(t, src, "n = n * 2")

	_, err := Analyze(ws, pkg, file, line, line)
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("expected *Refusal, got %T: %v", err, err)
	}
	if !strings.Contains(refusal.Reason, "type information is unavailable") {
		t.Errorf("unexpected reason: %q", refusal.Reason)
	}
}
