package analysis

import (
	"io"
	"log/slog"
	"testing"
)

const rootMarker = "example.com/w/skip.ManualTool"

func markedUnusedFacts(t *testing.T, files map[string]string, markers ...string) map[string]unusedFact {
	t.Helper()
	set, _ := loadSet(t, files)

	v := set.Primary()
	for _, pkg := range v.Workspace.Packages {
		v.Parser.EnsureTypeChecked(v.Workspace, pkg)
	}

	idx, err := v.ReferenceIndex()
	if err != nil {
		t.Fatal(err)
	}

	a := NewUnusedAnalyzer(v.Workspace, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.SetIndex(idx)
	a.SetRootMarkers(markers)

	return unusedFacts(a.FindUnusedSymbols())
}

func rootMarkerFiles(testSrc, prodSrc string) map[string]string {
	return map[string]string{
		"skip/skip.go": "package skip\n\nimport \"testing\"\n\nfunc ManualTool(t *testing.T) {}\n",
		"p/f.go":       "package p\n\nfunc reached() {}\n\nfunc viaHelper() {}\n\n" + prodSrc,
		"p/f_test.go":  "package p\n\nimport (\n\t\"testing\"\n\n\t\"example.com/w/skip\"\n)\n\n" + testSrc,
	}
}

func TestRootMarkers_SymbolReachedOnlyFromMarkedTestIsLive(t *testing.T) {
	files := rootMarkerFiles("func TestTool(t *testing.T) { skip.ManualTool(t); reached() }\n", "")

	assertReported(t, markedUnusedFacts(t, files), "f.go:reached", "")
	assertNotReported(t, markedUnusedFacts(t, files, rootMarker), "f.go:reached")
}

func TestRootMarkers_SymbolReachedFromUnmarkedTestIsStillReported(t *testing.T) {
	files := rootMarkerFiles("func TestPlain(t *testing.T) { reached() }\n", "")

	assertReported(t, markedUnusedFacts(t, files, rootMarker), "f.go:reached", "")
}

func TestRootMarkers_SymbolReachedThroughMarkedTestsHelperIsLive(t *testing.T) {
	files := rootMarkerFiles("func TestTool(t *testing.T) { skip.ManualTool(t); helper() }\n\nfunc helper() { viaHelper() }\n", "")

	assertNotReported(t, markedUnusedFacts(t, files, rootMarker), "f.go:viaHelper")
}

func TestRootMarkers_SymbolsBehindLiveProductionCodeAreLiveToo(t *testing.T) {
	files := rootMarkerFiles("func TestTool(t *testing.T) { skip.ManualTool(t); entry() }\n", "func entry() { deeper() }\n\nfunc deeper() {}\n")

	assertNotReported(t, markedUnusedFacts(t, files, rootMarker), "f.go:entry", "f.go:deeper")
}

func TestRootMarkers_MarkerCalledFromNonTestFunctionHasNoEffect(t *testing.T) {
	files := rootMarkerFiles("func TestPlain(t *testing.T) { reached() }\n", "func Prod(t *testing.T) { skip.ManualTool(t) }\n")
	files["p/f.go"] = "package p\n\nimport (\n\t\"testing\"\n\n\t\"example.com/w/skip\"\n)\n\nfunc reached() {}\n\nfunc viaHelper() {}\n\nfunc prod(t *testing.T) { skip.ManualTool(t) }\n"

	facts := markedUnusedFacts(t, files, rootMarker)

	assertReported(t, facts, "f.go:reached", "")
	assertReported(t, facts, "f.go:prod", "")
}
