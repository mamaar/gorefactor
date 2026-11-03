package tests_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/pkg/types"
)

func movePackageFixture(t *testing.T, files map[string]string, source, target string) string {
	t.Helper()
	dir := writeFiles(t, files)
	eng := createEngine(t)

	plan, err := eng.MovePackage(loadWorkspace(t, eng, dir), types.MovePackageRequest{SourcePackage: source, TargetPackage: target})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	return dir
}

func TestMovePackageCarriesEmbedAssetsAndTestdata(t *testing.T) {
	dir := movePackageFixture(t, map[string]string{
		"p/p.go": "package p\n\nimport \"embed\"\n\n//go:embed templates/*\nvar FS embed.FS\n",
		"p/p_test.go": "package p\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n" +
			"func TestGolden(t *testing.T) { _, _ = os.ReadFile(\"testdata/x.golden\") }\n",
		"p/templates/a.txt":   "template a\n",
		"p/testdata/x.golden": "golden\n",
		"use/use.go":          "package use\n\nimport \"scratch/p\"\n\nvar _ = p.FS\n",
	}, "p", "q")

	for rel, want := range map[string]string{"q/templates/a.txt": "template a\n", "q/testdata/x.golden": "golden\n"} {
		got := readRel(t, dir, rel)
		if got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}

	assertGone(t, dir, "p")
	goVets(t, dir, "")
}

func TestMovePackageLeavesSubpackagesCarriesPlainSubdirs(t *testing.T) {
	dir := movePackageFixture(t, map[string]string{
		"p/p.go":               "package p\n",
		"p/sub/sub.go":         "package sub\n",
		"p/sub/sub.txt":        "stays with sub\n",
		"p/docs/deep/page.md":  "docs\n",
		"p/testdata/in/in.go":  "package in\n\nfunc F() {}\n",
		"p/testdata/in/in.txt": "data\n",
		"p/.golangci.yml":      "hidden\n",
	}, "p", "q")

	for _, rel := range []string{"q/p.go", "q/docs/deep/page.md", "q/testdata/in/in.go", "q/testdata/in/in.txt", "q/.golangci.yml"} {
		readRel(t, dir, rel)
	}

	for _, rel := range []string{"p/p.go", "p/docs", "p/testdata", "p/.golangci.yml"} {
		assertGone(t, dir, rel)
	}

	readRel(t, dir, "p/sub/sub.go")
	readRel(t, dir, "p/sub/sub.txt")
}

func TestMovePackageCarriesEmptyAndBinaryFilesAndModes(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p/p.go":          "package p\n",
		"p/data/.gitkeep": "",
		"p/data/blob.bin": "\x00\xff\xfe\x01binary",
		"p/run.sh":        "#!/bin/sh\n",
	})

	err := os.Chmod(filepath.Join(dir, "p/run.sh"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	eng := createEngine(t)

	plan, err := eng.MovePackage(loadWorkspace(t, eng, dir), types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
	if err != nil {
		t.Fatalf("MovePackage: %v", err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	got := readRel(t, dir, "q/data/blob.bin")
	if got != "\x00\xff\xfe\x01binary" {
		t.Errorf("binary file corrupted: %q", got)
	}

	readRel(t, dir, "q/data/.gitkeep")

	info, err := os.Stat(filepath.Join(dir, "q/run.sh"))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o755 {
		t.Errorf("q/run.sh mode = %v, want 0755", info.Mode().Perm())
	}

	assertGone(t, dir, "p")
}

func TestMoveDirCarriesWholeTree(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/foo.go":              "package a\n",
		"a/README.md":           "readme\n",
		"a/assets/img/logo.png": "png",
		"a/.hidden/note":        "hidden\n",
		"a/b/bar.go":            "package b\n",
		"a/b/bar.json":          "{}\n",
	})
	eng := createEngine(t)

	plan, err := eng.MoveDir(loadWorkspace(t, eng, dir), types.MoveDirRequest{SourceDir: "a", TargetDir: "c"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	for _, rel := range []string{"c/foo.go", "c/README.md", "c/assets/img/logo.png", "c/.hidden/note", "c/b/bar.go", "c/b/bar.json"} {
		readRel(t, dir, rel)
	}

	assertGone(t, dir, "a")
}

func TestMoveDirWithoutGoFilesAtRootCarriesNonGoFiles(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/README.md": "readme\n",
		"a/b/bar.go":  "package b\n",
	})
	eng := createEngine(t)

	plan, err := eng.MoveDir(loadWorkspace(t, eng, dir), types.MoveDirRequest{SourceDir: "a", TargetDir: "c"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	readRel(t, dir, "c/README.md")
	assertGone(t, dir, "a")
}

func TestMoveRefusesExistingNonGoTargetWritingNothing(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p/p.go":       "package p\n",
		"p/data.txt":   "source\n",
		"q/data.txt":   "already here\n",
		"p/other/o.md": "other\n",
	})
	eng := createEngine(t)

	_, err := eng.MovePackage(loadWorkspace(t, eng, dir), types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
	if err == nil || !strings.Contains(err.Error(), "q/data.txt") {
		t.Fatalf("want a refusal naming q/data.txt, got %v", err)
	}

	got := readRel(t, dir, "q/data.txt")
	if got != "already here\n" {
		t.Errorf("target overwritten: %q", got)
	}

	readRel(t, dir, "p/p.go")
	readRel(t, dir, "p/data.txt")
}

func TestMoveRefusesSymlinkAndRepositoryDirs(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, dir string){
		"symlink": func(t *testing.T, dir string) {
			t.Helper()

			err := os.Symlink("p.go", filepath.Join(dir, "p/link.go.txt"))
			if err != nil {
				t.Skip(err)
			}
		},
		"git": func(t *testing.T, dir string) {
			t.Helper()
			mustWrite(t, filepath.Join(dir, "p/.git/HEAD"), "x")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"p/p.go": "package p\n"})
			setup(t, dir)
			eng := createEngine(t)
			ws := loadWorkspace(t, eng, dir)

			_, err := eng.MovePackage(ws, types.MovePackageRequest{SourcePackage: "p", TargetPackage: "q"})
			if err == nil {
				t.Error("MovePackage should refuse")
			}

			_, err = eng.MoveDir(ws, types.MoveDirRequest{SourceDir: "p", TargetDir: "q"})
			if err == nil {
				t.Error("MoveDir should refuse")
			}
		})
	}
}

func TestMoveDirCarriesVendorUnderTestdata(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p/p.go":                  "package p\n",
		"p/testdata/vendor/x.txt": "x\n",
	})
	eng := createEngine(t)

	plan, err := eng.MoveDir(loadWorkspace(t, eng, dir), types.MoveDirRequest{SourceDir: "p", TargetDir: "q"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	readRel(t, dir, "q/testdata/vendor/x.txt")
}

const (
	brokenFixture      = "package a\n\nfunc f() { x := // want \"broken\"\n"
	unformattedFixture = "package a\nimport (\"os\"; \"fmt\")\nfunc  f( ) {fmt.Println( os.Args )}\n"
)

func TestMovePackageCarriesTestdataGoFilesByteForByte(t *testing.T) {
	dir := movePackageFixture(t, map[string]string{
		"p/p.go":                "package p\n",
		"p/testdata/src/a/a.go": brokenFixture,
		"p/testdata/src/b/b.go": unformattedFixture,
	}, "p", "q")

	got := readRel(t, dir, "q/testdata/src/a/a.go")
	if got != brokenFixture {
		t.Errorf("invalid fixture changed: %q", got)
	}

	got = readRel(t, dir, "q/testdata/src/b/b.go")
	if got != unformattedFixture {
		t.Errorf("unformatted fixture changed: %q", got)
	}
}

func TestMoveDirCarriesInvalidTestdataGoByteForByte(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/a.go":            "package a\n",
		"a/testdata/x/x.go": strings.Replace(brokenFixture, "package a", "package x", 1),
	})
	eng := createEngine(t)

	plan, err := eng.MoveDir(loadWorkspace(t, eng, dir), types.MoveDirRequest{SourceDir: "a", TargetDir: "c"})
	if err != nil {
		t.Fatalf("MoveDir: %v", err)
	}

	err = eng.ExecutePlan(plan)
	if err != nil {
		t.Fatalf("ExecutePlan: %v", err)
	}

	got := readRel(t, dir, "c/testdata/x/x.go")
	if got != strings.Replace(brokenFixture, "package a", "package x", 1) {
		t.Errorf("invalid fixture changed: %q", got)
	}
}

func TestMovePackageCarriesAssetsBesideNestedPackage(t *testing.T) {
	dir := movePackageFixture(t, map[string]string{
		"p/p.go":             "package p\n\nimport _ \"embed\"\n\n//go:embed assets/logo.png\nvar Logo []byte\n",
		"p/assets/logo.png":  "png",
		"p/assets/gen/x.go":  "package gen\n",
		"p/assets/gen/x.txt": "stays\n",
	}, "p", "q")
	readRel(t, dir, "q/assets/logo.png")
	readRel(t, dir, "p/assets/gen/x.go")
	readRel(t, dir, "p/assets/gen/x.txt")
	assertGone(t, dir, "p/assets/logo.png")
	goVets(t, dir, "")
}

func TestMovePackageLeavesNestedModuleAndGoDirsStartingWithDot(t *testing.T) {
	dir := movePackageFixture(t, map[string]string{
		"p/p.go":             "package p\n",
		"p/sub/go.mod":       "module other\n",
		"p/sub/README":       "r\n",
		"p/sub/deep/note":    "n\n",
		"p/_example/main.go": "package main\n",
		"p/_example/in.txt":  "i\n",
	}, "p", "q")
	for _, rel := range []string{"p/sub/go.mod", "p/sub/README", "p/sub/deep/note", "p/_example/main.go", "p/_example/in.txt"} {
		readRel(t, dir, rel)
	}

	assertGone(t, dir, "q/sub")
	assertGone(t, dir, "q/_example")
}

func TestMovePackageCarriesVendorWithoutGoAndLeavesOneWith(t *testing.T) {
	dir := movePackageFixture(t, map[string]string{
		"p/p.go":          "package p\n",
		"p/vendor/a/a.go": "package a\n",
		"p/vendor/note":   "n\n",
	}, "p", "q")
	readRel(t, dir, "q/vendor/note")
	readRel(t, dir, "p/vendor/a/a.go")
}

func TestMoveDirRefusesNestedModule(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a/a.go":              "package a\n",
		"a/mod/go.mod":        "module other\n",
		"a/testdata/m/go.mod": "module fixture\n",
	})
	eng := createEngine(t)

	_, err := eng.MoveDir(loadWorkspace(t, eng, dir), types.MoveDirRequest{SourceDir: "a", TargetDir: "c"})
	if err == nil || !strings.Contains(err.Error(), "a/mod/go.mod") {
		t.Fatalf("want a refusal naming a/mod/go.mod, got %v", err)
	}
}

func TestMovePackageCarriesEmptyGoFile(t *testing.T) {
	dir := movePackageFixture(t, map[string]string{"p/p.go": "package p\n", "p/empty.go": ""}, "p", "q")
	readRel(t, dir, "q/empty.go")
	assertGone(t, dir, "p")
}
