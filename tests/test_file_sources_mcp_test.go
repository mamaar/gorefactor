package tests_test

import (
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const tfsTest = `package p

import "testing"

type S struct{ n int }

func (s S) Double() int { return s.n * 2 }

func (s S) Run() int {
	a := s.n + 1
	b := a * 3
	return b
}

func add(a, b int) int {
	return a + b
}

func TestAll(t *testing.T) {
	x := 1
	y := 2
	z := x + y
	if z != 3 {
		t.Fatal(z)
	}
	v := add(1, 2) + 1
	w := 5
	_ = w + v
	_ = S{n: 1}.Double()
}
`

func tfsLine(t *testing.T, needle string) int {
	t.Helper()
	for i, l := range strings.Split(tfsTest, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("%q not in fixture", needle)
	return 0
}

// Every operation that takes a caller-supplied file must accept a _test.go
// file; the result is vetted because go build never compiles test files.
func TestOperationsAcceptTestFileSources(t *testing.T) {
	cases := []struct {
		tool string
		args func(t *testing.T) map[string]any
		want string
	}{
		{"extract_function", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "start_line": tfsLine(t, "z := x + y"), "end_line": tfsLine(t, "z := x + y"), "new_function_name": "sum"}
		}, "func sum"},
		{"extract_method", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "start_line": tfsLine(t, "b := a * 3"), "end_line": tfsLine(t, "b := a * 3"), "new_method_name": "triple", "target_struct": "S"}
		}, ") triple("},
		{"extract_variable", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "start_line": tfsLine(t, "z := x + y"), "end_line": tfsLine(t, "z := x + y"), "variable_name": "total", "expression": "x + y"}
		}, "total := x + y"},
		{"extract_constant", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "line": tfsLine(t, "y := 2"), "column": 7, "constant_name": "Two"}
		}, "y := Two"},
		{"inline_variable", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "variable_name": "w"}
		}, "_ = 5 + v"},
		{"inline_function", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "function_name": "add"}
		}, "v := 1 + 2 + 1"},
		{"inline_method", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"target_file": "p/p_test.go", "method_name": "Double", "source_struct": "S"}
		}, "(S{n: 1}).n * 2"},
		{"add_context_parameter", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "function_name": "add"}
		}, "func add(ctx context.Context"},
		{"change_signature", func(t *testing.T) map[string]any {
			t.Helper()
			return map[string]any{"source_file": "p/p_test.go", "function_name": "add", "subcommand": "add_param", "params": []map[string]any{{"name": "a", "type": "int"}, {"name": "b", "type": "int"}, {"name": "c", "type": "int"}}, "default_value": "0"}
		}, "c int"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{
				"go.mod":      "module example.com/tfs\n\ngo 1.21\n",
				"p/p.go":      "package p\n",
				"p/p_test.go": tfsTest,
			})
			sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
			defer sess.Close()

			args := tc.args(t)
			dry := map[string]any{"dry_run": true}
			maps.Copy(dry, args)
			callJSON(t, sess, tc.tool, dry)
			path := filepath.Join(dir, "p", "p_test.go")
			if readFile(t, path) != tfsTest {
				t.Fatal("dry run modified the file")
			}

			callJSON(t, sess, tc.tool, args)
			got := readFile(t, path)
			if !strings.Contains(got, tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, got)
			}
			vet := exec.Command("go", "vet", "./...")
			vet.Dir = dir
			out, err := vet.CombinedOutput()
			if err != nil {
				t.Errorf("go vet: %v\n%s\n%s", err, out, got)
			}
		})
	}
}

func TestSourceFileNotFoundNamesTheFile(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"go.mod":      "module example.com/tfs\n\ngo 1.21\n",
		"p/p_test.go": tfsTest,
	})
	sess := mcptest.Dial(t.Context(), t, mcpTransport(), dir)
	defer sess.Close()

	res, text := sdCall(t, sess, "extract_function", map[string]any{"source_file": "p/missing_test.go", "start_line": 1, "end_line": 1, "new_function_name": "f", "dry_run": true})
	if !res.IsError || !strings.Contains(text, "missing_test.go") {
		t.Errorf("want an error naming the file, got %v %q", res.IsError, text)
	}
}
