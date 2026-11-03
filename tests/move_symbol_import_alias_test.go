package tests_test

import (
	"context"
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const aliasSrcModel = `package model

import "fmt"

type FolderError struct{ Msg string }

func (e FolderError) Error() string { return fmt.Sprintf("folder: %s", e.Msg) }

type Other struct{}
`

func aliasFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":         "module example.com/mv\n\ngo 1.22\n",
		"a/model/m.go":   aliasSrcModel,
		"a/model/use.go": "package model\n\nfunc Same() FolderError { return FolderError{} }\n",
		"b/model/m.go":   "package model\n\ntype Existing struct{}\n",
		"d/model/m.go":   "package model\n\ntype X struct{}\n",
		// imports both packages, target aliased; source import stays in use.
		"c/both.go": `package c

import (
	bmodel "example.com/mv/b/model"
	"example.com/mv/a/model"
)

func Both() (error, model.Other, bmodel.Existing) {
	return model.FolderError{}, model.Other{}, bmodel.Existing{}
}
`,
		// imports only the source, aliased, next to another package named model.
		"e/only.go": `package e

import (
	amodel "example.com/mv/a/model"
	"example.com/mv/d/model"
)

func Only() (error, model.X) {
	return amodel.FolderError{}, model.X{}
}
`,
		// imports only the source, unaliased; nothing else claims the name.
		"f/plain.go": `package f

import "example.com/mv/a/model"

func Plain() error { return model.FolderError{} }
`,
	} {
		mustWrite(t, filepath.Join(dir, name), content)
	}

	return dir
}

func TestMoveSymbol_ReferenceQualifiersRespectExistingImports(t *testing.T) {
	dir := aliasFixture(t)
	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	args := map[string]any{"symbol": "FolderError", "from_package": "a/model", "to_package": "b/model"}
	dry := map[string]any{"dry_run": true}
	maps.Copy(dry, args)
	dryOut := callJSON(t, sess, "move_symbol", dry)

	diffs, _ := dryOut["diffs"].([]any)
	if len(diffs) == 0 {
		t.Fatalf("dry run returned no diffs: %v", dryOut)
	}

	callJSON(t, sess, "move_symbol", args)

	both := mustRead(t, filepath.Join(dir, "c/both.go"))
	if !strings.Contains(both, "bmodel.FolderError{}") || !strings.Contains(both, "model.Other{}") {
		t.Errorf("c/both.go wrong:\n%s", both)
	}

	if strings.Count(both, "example.com/mv/b/model") != 1 || !strings.Contains(both, `"example.com/mv/a/model"`) {
		t.Errorf("c/both.go imports wrong:\n%s", both)
	}

	only := mustRead(t, filepath.Join(dir, "e/only.go"))
	if strings.Contains(only, "amodel") || strings.Contains(only, "a/model") {
		t.Errorf("e/only.go keeps unused source import:\n%s", only)
	}

	if !strings.Contains(only, "bmodel.FolderError{}") || !strings.Contains(only, `bmodel "example.com/mv/b/model"`) || !strings.Contains(only, "model.X{}") {
		t.Errorf("e/only.go wrong:\n%s", only)
	}

	plain := mustRead(t, filepath.Join(dir, "f/plain.go"))
	if !strings.Contains(plain, "model.FolderError{}") || strings.Contains(plain, "a/model") || !strings.Contains(plain, `"example.com/mv/b/model"`) {
		t.Errorf("f/plain.go wrong:\n%s", plain)
	}

	target := mustRead(t, filepath.Join(dir, "b/model/m.go"))
	if !strings.Contains(target, `"fmt"`) || !strings.Contains(target, "type FolderError") || !strings.Contains(target, "func (e FolderError) Error()") {
		t.Errorf("b/model/m.go wrong:\n%s", target)
	}

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("module does not vet clean after the move:\n%s", out)
	}
}

func TestMoveSymbol_MovesEveryMethodOnceWhenNamesOverlapReceiver(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/mv\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "a/model/m.go"), `package model

type LayerError struct{ Inner error }

func (l LayerError) Unwrap() error { return l.Inner }

func (l LayerError) Error() string { return "layer error" }
`)
	mustWrite(t, filepath.Join(dir, "b/model/m.go"), "package model\n\ntype Existing struct{}\n")

	ctx := context.Background()

	sess := mcptest.Dial(ctx, t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "move_symbol", map[string]any{"symbol": "LayerError", "from_package": "a/model", "to_package": "b/model"})

	target := mustRead(t, filepath.Join(dir, "b/model/m.go"))
	for _, want := range []string{"func (l LayerError) Unwrap()", "func (l LayerError) Error()"} {
		if strings.Count(target, want) != 1 {
			t.Errorf("want exactly one %q in target:\n%s", want, target)
		}
	}
}
