package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const foldF = "package p\n\nfunc f() (int, error) { return 1, nil }\n\nfunc g() bool { return true }\n"

// removeErrorFolds runs remove_return on f against use.go and returns the dir.
func removeErrorFolds(t *testing.T, use string) string {
	t.Helper()
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": foldF, "p/use.go": use})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()
	changeSigOK(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))
	goVets(t, dir, "")
	return dir
}

func TestChangeSignature_RemoveReturnFoldsDeadErrorChecks(t *testing.T) {
	cases := map[string]struct {
		use  string
		want []string
		omit []string
	}{
		"plain check": {
			use:  "package p\n\nfunc Use() (int, error) {\n\tx, err := f()\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\treturn x, nil\n}\n",
			want: []string{"x := f()\n", "return x, nil"},
			omit: []string{"err :=", "err !="},
		},
		"else branch kept": {
			use:  "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif err != nil {\n\t\treturn 0\n\t} else {\n\t\tx++\n\t}\n\treturn x\n}\n",
			want: []string{"x := f()\n", "x++\n", "return x"},
			omit: []string{"err", "else", "return 0"},
		},
		"nil check keeps then branch": {
			use:  "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif err == nil {\n\t\tx++\n\t}\n\treturn x\n}\n",
			want: []string{"x := f()\n", "x++\n", "return x"},
			omit: []string{"err", "if"},
		},
		"else if chain": {
			use:  "package p\n\nfunc Use(n int) int {\n\tx, err := f()\n\tif n > 1 {\n\t\treturn n\n\t} else if err != nil {\n\t\treturn 0\n\t} else if n > 0 {\n\t\treturn 2\n\t}\n\treturn x\n}\n",
			want: []string{"x := f()\n", "else if n > 0"},
			omit: []string{"err", "return 0"},
		},
		"compound condition": {
			use:  "package p\n\nfunc Use(n int) int {\n\tx, err := f()\n\tif n > 0 || err != nil {\n\t\treturn 0\n\t}\n\tif n < 0 && err == nil {\n\t\treturn 1\n\t}\n\treturn x\n}\n",
			want: []string{"if n > 0 {", "if n < 0 {"},
			omit: []string{"err"},
		},
		"init statement keeps the call": {
			use:  "package p\n\nfunc Use() int {\n\tif x, err := f(); err != nil {\n\t\treturn 0\n\t} else {\n\t\treturn x\n\t}\n}\n",
			want: []string{"x := f()", "return x"},
			omit: []string{"err", "return 0"},
		},
		"import used only by the dead branch": {
			use:  "package p\n\nimport \"fmt\"\n\nfunc Use() (int, error) {\n\tx, err := f()\n\tif err != nil {\n\t\treturn 0, fmt.Errorf(\"f: %w\", err)\n\t}\n\treturn x, nil\n}\n",
			omit: []string{"fmt", "err :=", "err !="},
		},
		"kept operand with a call": {
			use:  "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif err != nil || g() {\n\t\treturn 0\n\t}\n\treturn x\n}\n",
			want: []string{"if g() {"},
			omit: []string{"err"},
		},
		"else-if that takes the place of its if": {
			use:  "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif err != nil {\n\t\treturn 0\n\t} else if err == nil {\n\t\tx++\n\t}\n\treturn x\n}\n",
			want: []string{"x := f()\n\tx++\n\treturn x"},
			omit: []string{"err", "{\n\t\tx++"},
		},
		"write inside the dead branch": {
			use:  "package p\n\nimport \"fmt\"\n\nfunc Use() (int, error) {\n\tx, err := f()\n\tif err != nil {\n\t\terr = fmt.Errorf(\"w: %w\", err)\n\t\treturn 0, err\n\t}\n\treturn x, nil\n}\n",
			want: []string{"x := f()\n\treturn x, nil"},
			omit: []string{"fmt"},
		},
		"second call inside the dead branch": {
			use:  "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif err != nil {\n\t\ty, err2 := f()\n\t\tif err2 != nil {\n\t\t\treturn 0\n\t\t}\n\t\treturn y\n\t}\n\treturn x\n}\n",
			want: []string{"x := f()\n\treturn x"},
			omit: []string{"err", "y"},
		},
		"import used only by the dead branch of an init form": {
			use:  "package p\n\nimport \"fmt\"\n\nfunc Use() int {\n\tif x, err := f(); err != nil {\n\t\tfmt.Println(\"bad\")\n\t\treturn 0\n\t} else {\n\t\ty := x\n\t\treturn y\n\t}\n}\n",
			omit: []string{"fmt", "err"},
		},
		"no blank line before the closing brace of a wrapped init": {
			use:  "package p\n\nfunc Use() int {\n\tx := 5\n\tif x, err := f(); err == nil {\n\t\tprintln(x)\n\t}\n\treturn x\n}\n",
			want: []string{"println(x)\n\t}\n"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := removeErrorFolds(t, c.use)
			for _, w := range c.want {
				assertFileContains(t, dir, "p/use.go", w)
			}
			for _, o := range c.omit {
				assertFileOmits(t, dir, "p/use.go", o)
			}
		})
	}
}

func TestChangeSignature_RemoveReturnFoldsInitStatementWithNothingLeft(t *testing.T) {
	f := "package p\n\nfunc f() error { return nil }\n"
	use := "package p\n\nfunc Use() {\n\tif err := f(); err != nil {\n\t\tprintln(err.Error())\n\t}\n\tprintln(1)\n}\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": f, "p/use.go": use})
	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, returnArgs("f", "p/f.go", "remove_return"))

	assertFileContains(t, dir, "p/f.go", "func f() { return }")
	assertFileContains(t, dir, "p/use.go", "\tf()\n\tprintln(1)")
	assertFileOmits(t, dir, "p/use.go", "err")
	goVets(t, dir, "")
}

func TestChangeSignature_RemoveReturnRefusesErrorReadsItCannotFold(t *testing.T) {
	cases := map[string]string{
		"errors.Is":                                    "package p\n\nimport \"errors\"\n\nfunc Use() bool {\n\t_, err := f()\n\tif errors.Is(err, errors.ErrUnsupported) {\n\t\treturn true\n\t}\n\treturn false\n}\n",
		"reassigned then read":                         "package p\n\nfunc Use() error {\n\tx, err := f()\n\tif x > 0 {\n\t\terr = g()\n\t}\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n\nfunc g() error { return nil }\n",
		"read in the kept branch":                      "package p\n\nfunc Use() error {\n\t_, err := f()\n\tif err == nil {\n\t\treturn err\n\t}\n\treturn nil\n}\n",
		"call before the decided operand":              "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif g() && err != nil {\n\t\treturn 0\n\t}\n\treturn x\n}\n",
		"call before the dropped operand":              "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif g() || err == nil {\n\t\tx++\n\t}\n\treturn x\n}\n",
		"dereference before the decided operand":       "package p\n\ntype T struct{ n int }\n\nfunc Use(p *T) int {\n\tx, err := f()\n\tif p.n > 0 || err == nil {\n\t\tx++\n\t}\n\treturn x\n}\n",
		"type assertion before the decided operand":    "package p\n\nfunc Use(v any) int {\n\tx, err := f()\n\tif v.(int) > 0 || err == nil {\n\t\tx++\n\t}\n\treturn x\n}\n",
		"else-if with an init statement":               "package p\n\nfunc Use(n int) int {\n\tx, err := f()\n\tif n > 0 {\n\t\treturn 1\n\t} else if y := n; err == nil {\n\t\treturn y + x\n\t}\n\treturn 0\n}\n",
		"local read only in a dropped operand":         "package p\n\nfunc Use() int {\n\tn := 3\n\tx, err := f()\n\tif n > 0 || err == nil {\n\t\tx++\n\t}\n\treturn x\n}\n",
		"init variable read only in a dropped operand": "package p\n\nfunc Use() int {\n\tif x, err := f(); x > 0 || err == nil {\n\t\tprintln(1)\n\t}\n\treturn 0\n}\n",
		"local read only in a decided inner chain":     "package p\n\nfunc Use(m int) int {\n\tk := 2\n\tx, err := f()\n\tif m > 0 || (k > 0 && err != nil) {\n\t\tx++\n\t}\n\treturn x\n}\n",
		"label used only by the dead branch":           "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif err != nil {\n\t\tgoto fail\n\t}\n\treturn x\nfail:\n\treturn 0\n}\n",
		"continue label used only by the dead branch":  "package p\n\nfunc Use(xs []int) int {\n\ts := 0\nouter:\n\tfor _, v := range xs {\n\t\tx, err := f()\n\t\tif err != nil {\n\t\t\tcontinue outer\n\t\t}\n\t\ts += x + v\n\t}\n\treturn s\n}\n",
		"two removed errors in one condition":          "package p\n\nfunc Use() int {\n\tx, err1 := f()\n\ty, err2 := f()\n\tif err1 != nil || err2 != nil {\n\t\treturn 0\n\t}\n\treturn x + y\n}\n",
		"unreachable code after an else-if chain":      "package p\n\nfunc Use(n int) int {\n\tx, err := f()\n\tif n > 0 {\n\t\treturn 1\n\t} else if err == nil {\n\t\treturn x\n\t}\n\tprintln(\"tail\")\n\treturn 0\n}\n",
		"unreachable code after a bare block":          "package p\n\nfunc Use() int {\n\tx, err := f()\n\t{\n\t\tif err == nil {\n\t\t\treturn x\n\t\t}\n\t}\n\treturn 0\n}\n",
		"only break of an endless loop":                "package p\n\nfunc Use() int {\n\tfor {\n\t\tx, err := f()\n\t\tif err != nil {\n\t\t\tbreak\n\t\t}\n\t\treturn x\n\t}\n\treturn 0\n}\n",
		"stored in a bool":                             "package p\n\nfunc Use() bool {\n\t_, err := f()\n\tok := err == nil\n\treturn ok\n}\n",
		"code after a terminating kept branch":         "package p\n\nfunc Use() int {\n\tx, err := f()\n\tif err == nil {\n\t\treturn x\n\t}\n\tprintln(\"tail\")\n\treturn 0\n}\n",
		"variable used only by the dead branch":        "package p\n\nfunc Use() int {\n\tvar msg string\n\tx, err := f()\n\tif err != nil {\n\t\tmsg = err.Error()\n\t\tprintln(msg)\n\t\treturn 0\n\t}\n\treturn x\n}\n",
	}
	for name, use := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeInlineWorkspace(t, map[string]string{"p/f.go": foldF, "p/use.go": use})
			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			res := changeSig(t, sess, returnArgs("f", "p/f.go", "remove_return", "int"))

			if !res.IsError || !strings.Contains(errText(res), "use.go:") {
				t.Errorf("want a refusal naming use.go, got IsError=%v %q", res.IsError, errText(res))
			}
			assertFileUnchanged(t, dir, "p/f.go", foldF)
			assertFileUnchanged(t, dir, "p/use.go", use)
		})
	}
}
