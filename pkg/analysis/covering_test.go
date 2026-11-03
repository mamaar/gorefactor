package analysis

import (
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var coveringPrimary = NewBuildConfig("linux", "amd64", true)

func coveringTree(t *testing.T, files map[string]string) *SourceTree {
	t.Helper()

	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)

		err := os.MkdirAll(filepath.Dir(full), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(full, []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
	}

	tree := NewSourceTree(token.NewFileSet())

	err := tree.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	return tree
}

func constrained(pkg, constraint string) string {
	if constraint == "" {
		return "package " + pkg + "\n"
	}

	return "//go:build " + constraint + "\n\npackage " + pkg + "\n"
}

func realRepoFixture() map[string]string {
	files := map[string]string{}
	for _, d := range []string{"a", "b", "c", "d", "e", "f"} {
		files[d+"/base.go"] = constrained(d, "")
	}

	files["a/x_fts5_test.go"] = constrained("a", "fts5")
	files["a/x_nofts5_test.go"] = constrained("a", "!fts5")
	files["b/v1_test.go"] = constrained("b", "feltboks_v1_tests")
	files["c/it_test.go"] = constrained("c", "gcp_integration")
	files["d/disk_unix.go"] = constrained("d", "unix")
	files["d/disk_windows.go"] = constrained("d", "windows")
	files["d/disk_other.go"] = constrained("d", "!unix && !windows")
	files["e/open_ios.go"] = constrained("e", "ios")
	files["e/open_other.go"] = constrained("e", "!ios")
	files["f/sig_notwin.go"] = constrained("f", "!windows")
	files["f/sig_windows.go"] = "package f\n"

	return files
}

func allFiles(tree *SourceTree) []*SourceFile {
	var out []*SourceFile
	for _, dir := range tree.Dirs() {
		out = append(out, tree.Dir(dir)...)
	}

	return out
}

func configKeys(cov Covering) []string {
	var keys []string
	for _, c := range cov.Configs {
		keys = append(keys, c.Key())
	}

	return keys
}

func findFile(t *testing.T, tree *SourceTree, name string) *SourceFile {
	t.Helper()

	for _, f := range allFiles(tree) {
		if strings.HasSuffix(f.Path, filepath.FromSlash(name)) {
			return f
		}
	}

	t.Fatalf("no file %s", name)

	return nil
}

func TestComputeCovering_RealRepoShape(t *testing.T) {
	tree := coveringTree(t, realRepoFixture())
	cov := ComputeCovering(tree, coveringPrimary)

	if len(cov.Uncovered) != 0 {
		t.Fatalf("uncovered = %+v", cov.Uncovered)
	}

	if len(cov.Configs) != 5 {
		t.Fatalf("configs = %v, want 5", configKeys(cov))
	}

	if cov.Configs[0].Key() != coveringPrimary.Key() {
		t.Fatalf("Configs[0] = %s, want primary", cov.Configs[0].Key())
	}

	for _, f := range allFiles(tree) {
		if !slices.ContainsFunc(cov.Configs, func(c BuildConfig) bool { return c.MatchesFile(f) }) {
			t.Errorf("%s matched by no config", f.Path)
		}
	}

	wantTags := []string{"feltboks_v1_tests", "fts5", "gcp_integration"}
	var tagged, windows, ios, other int

	for _, c := range cov.Configs[1:] {
		switch {
		case slices.Equal(c.Tags, wantTags) && c.GOOS == "linux" && c.GOARCH == "amd64":
			tagged++
		case c.GOOS == "windows":
			windows++
		case c.GOOS == "ios":
			ios++
		case c.MatchesFile(findFile(t, tree, "d/disk_other.go")):
			other++
		}
	}

	if tagged != 1 || windows != 1 || ios != 1 || other != 1 {
		t.Errorf("tagged=%d windows=%d ios=%d other=%d in %v", tagged, windows, ios, other, configKeys(cov))
	}
}

func TestComputeCovering_FTS5AndNotFTS5NeverShareAConfig(t *testing.T) {
	files := map[string]string{
		"a/base.go":          constrained("a", ""),
		"a/x_fts5_test.go":   constrained("a", "fts5"),
		"a/x_nofts5_test.go": constrained("a", "!fts5"),
	}
	cov := ComputeCovering(coveringTree(t, files), coveringPrimary)

	if len(cov.Configs) != 2 || !slices.Equal(cov.Configs[1].Tags, []string{"fts5"}) {
		t.Fatalf("configs = %v", configKeys(cov))
	}

	if cov.Configs[1].GOOS != "linux" || cov.Configs[1].GOARCH != "amd64" {
		t.Errorf("tagged config left the primary port: %s", cov.Configs[1].Key())
	}
}

func TestComputeCovering_IndependentCustomTagsMerge(t *testing.T) {
	files := map[string]string{
		"a/base.go":        constrained("a", ""),
		"a/x_fts5_test.go": constrained("a", "fts5"),
		"c/base.go":        constrained("c", ""),
		"c/it_test.go":     constrained("c", "gcp_integration"),
	}
	cov := ComputeCovering(coveringTree(t, files), coveringPrimary)

	if len(cov.Configs) != 2 || !slices.Equal(cov.Configs[1].Tags, []string{"fts5", "gcp_integration"}) {
		t.Fatalf("configs = %v", configKeys(cov))
	}
}

func TestComputeCovering_IgnoreTagIsUncovered(t *testing.T) {
	files := map[string]string{
		"a/gen.go":    "//go:build ignore\n\npackage main\n",
		"a/legacy.go": "// +build ignore\n\npackage main\n",
	}
	cov := ComputeCovering(coveringTree(t, files), coveringPrimary)

	if len(cov.Configs) != 1 || len(cov.Uncovered) != 2 {
		t.Fatalf("configs=%v uncovered=%+v", configKeys(cov), cov.Uncovered)
	}

	for _, u := range cov.Uncovered {
		if !strings.Contains(u.Reason, "ignore") {
			t.Errorf("%s: reason %q does not mention ignore", u.Path, u.Reason)
		}
	}
}

func TestComputeCovering_ContradictoryConstraintIsUnsatisfiable(t *testing.T) {
	files := map[string]string{"a/x.go": constrained("a", "linux && windows")}
	cov := ComputeCovering(coveringTree(t, files), coveringPrimary)

	if len(cov.Uncovered) != 1 || !strings.Contains(cov.Uncovered[0].Reason, "unsatisfiable") {
		t.Fatalf("uncovered = %+v", cov.Uncovered)
	}

	if cov.Uncovered[0].Constraint != "linux && windows" {
		t.Errorf("constraint = %q", cov.Uncovered[0].Constraint)
	}
}

func TestComputeCovering_ParseErrorIsUncovered(t *testing.T) {
	files := map[string]string{"a/bad.go": "package a\n\nfunc {"}
	cov := ComputeCovering(coveringTree(t, files), coveringPrimary)

	if len(cov.Uncovered) != 1 || !strings.HasPrefix(cov.Uncovered[0].Reason, "parse error: ") {
		t.Fatalf("uncovered = %+v", cov.Uncovered)
	}
}

func TestComputeCovering_CgoFileNeedsCgoConfig(t *testing.T) {
	files := map[string]string{"a/c.go": "package a\n\nimport \"C\"\n"}
	cov := ComputeCovering(coveringTree(t, files), NewBuildConfig("linux", "amd64", false))

	if len(cov.Uncovered) != 0 || len(cov.Configs) != 2 {
		t.Fatalf("configs=%v uncovered=%+v", configKeys(cov), cov.Uncovered)
	}

	c := cov.Configs[1]
	if !c.Cgo || c.GOOS != "linux" || c.GOARCH != "amd64" {
		t.Errorf("config = %s", c.Key())
	}
}

func TestComputeCovering_Deterministic(t *testing.T) {
	files := realRepoFixture()
	files["g/gen.go"] = constrained("g", "ignore")
	tree := coveringTree(t, files)

	first := ComputeCovering(tree, coveringPrimary)
	second := ComputeCovering(tree, coveringPrimary)

	if !slices.Equal(configKeys(first), configKeys(second)) || !slices.Equal(first.Uncovered, second.Uncovered) {
		t.Errorf("first=%v second=%v", configKeys(first), configKeys(second))
	}
}

func TestComputeCovering_NoConstraintsYieldsOnlyPrimary(t *testing.T) {
	files := map[string]string{"a/a.go": constrained("a", ""), "b/b.go": constrained("b", "")}
	cov := ComputeCovering(coveringTree(t, files), coveringPrimary)

	if len(cov.Configs) != 1 || cov.Configs[0].Key() != coveringPrimary.Key() || len(cov.Uncovered) != 0 {
		t.Fatalf("configs=%v uncovered=%+v", configKeys(cov), cov.Uncovered)
	}
}
