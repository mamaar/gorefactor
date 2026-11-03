package tests_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const sdtfSource = `package p

import "strings"

func Live() int { return 1 }

func Shout() string { return strings.ToUpper("x") }
`

const sdtfTest = `package p

import (
	"fmt"
	"strings"
	"testing"
)

type fixture struct{}

var sample = 1

func fmtHelper() string { return fmt.Sprint(1) }

func strHelper() string { return strings.ToUpper("x") }

func TestDead(t *testing.T) { _ = Live() }

func TestLive(t *testing.T) { _ = strHelper() }

func BenchmarkDead(b *testing.B) { _ = Live() }

func FuzzDead(f *testing.F) { _ = Live() }

func ExampleLive() { _ = Live() }
`

func writeSafeDeleteTestFilesFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":      "module example.com/sdtf\n\ngo 1.21\n",
		"p/p.go":      sdtfSource,
		"p/p_test.go": sdtfTest,
	} {
		p := filepath.Join(dir, name)

		err := os.MkdirAll(filepath.Dir(p), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(p, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestSafeDelete_DeclarationsInTestFiles(t *testing.T) {
	for _, tc := range []struct{ symbol, gone string }{
		{"TestDead", "func TestDead"},
		{"BenchmarkDead", "func BenchmarkDead"},
		{"FuzzDead", "func FuzzDead"},
		{"ExampleLive", "func ExampleLive"},
		{"fixture", "type fixture"},
		{"sample", "var sample"},
		{"fmtHelper", "func fmtHelper"},
	} {
		t.Run(tc.symbol, func(t *testing.T) {
			dir := writeSafeDeleteTestFilesFixture(t)

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			path := filepath.Join(dir, "p", "p_test.go")

			for _, source := range []string{"p/p_test.go", path} {
				dry := callJSON(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": source, "dry_run": true})

				b, ok := dry["blocking_issue"]
				if ok {
					t.Fatalf("unexpected blocking_issue: %v", b)
				}

				if readFile(t, path) != sdtfTest {
					t.Fatal("dry run modified the file")
				}
			}

			callJSON(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": "p/p_test.go"})

			got := readFile(t, path)
			if strings.Contains(got, tc.gone) {
				t.Errorf("%q still present:\n%s", tc.gone, got)
			}

			if !strings.Contains(got, "func TestLive") {
				t.Errorf("neighbour deleted:\n%s", got)
			}
		})
	}
}

func TestSafeDelete_PrunesImportsOrphanedByDeletion(t *testing.T) {
	for _, tc := range []struct {
		name, file, symbol string
		kept, dropped      []string
	}{
		{"test file", "p/p_test.go", "fmtHelper", []string{`"strings"`, `"testing"`}, []string{`"fmt"`}},
		{"source file", "p/p.go", "Shout", nil, []string{`"strings"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeSafeDeleteTestFilesFixture(t)

			sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
			defer sess.Close()

			callJSON(t, sess, "safe_delete", map[string]any{"symbol": tc.symbol, "source_file": tc.file})

			got := readFile(t, filepath.Join(dir, tc.file))
			for _, imp := range tc.kept {
				if !strings.Contains(got, imp) {
					t.Errorf("import %s dropped though still used:\n%s", imp, got)
				}
			}

			for _, imp := range tc.dropped {
				if strings.Contains(got, imp) {
					t.Errorf("import %s left behind:\n%s", imp, got)
				}
			}
		})
	}
}

func TestSafeDelete_TestFileSymbolStillBlockedByReferences(t *testing.T) {
	dir := writeSafeDeleteTestFilesFixture(t)

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res, text := sdCall(t, sess, "safe_delete", map[string]any{"symbol": "strHelper", "source_file": "p/p_test.go", "dry_run": true})
	if !res.IsError || !strings.Contains(text, "found 1 references") {
		t.Errorf("want blocked with 1 reference, got error=%v text=%s", res.IsError, text)
	}
}
