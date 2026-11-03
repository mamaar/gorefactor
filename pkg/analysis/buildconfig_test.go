package analysis

import (
	"go/build"
	"go/parser"
	"go/token"
	"testing"
)

func newSourceFile(t *testing.T, name, src string) *SourceFile {
	t.Helper()

	f := &SourceFile{Path: "/w/" + name, Dir: "/w", Name: name, Content: []byte(src)}

	f.AST, f.Err = parser.ParseFile(token.NewFileSet(), f.Path, f.Content, parser.ParseComments)
	if f.Err != nil {
		t.Fatal(f.Err)
	}

	return f
}

func TestBuildConfig_KeyIgnoresTagOrderAndDuplicates(t *testing.T) {
	a := BuildConfig{GOOS: "linux", GOARCH: "amd64", Cgo: true, Tags: []string{"b", "a", "b"}}

	b := NewBuildConfig("linux", "amd64", true, "a", "b")
	if a.Key() != b.Key() {
		t.Errorf("keys differ: %q vs %q", a.Key(), b.Key())
	}

	got, want := b.Key(), "linux/amd64/cgo/a,b"
	if got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}

	got, want = NewBuildConfig("linux", "amd64", false).Key(), "linux/amd64/nocgo/"
	if got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestBuildConfig_StringIsDeltaFromDefault(t *testing.T) {
	def := DefaultBuildConfig()

	got := def.String()
	if got != "default" {
		t.Errorf("default String() = %q", got)
	}

	cfg := NewBuildConfig("plan9", def.GOARCH, !def.Cgo, "fts5", "gcp_integration")

	got, want := cfg.String(), "GOOS=plan9 cgo="+onOff(!def.Cgo)+" tags=fts5,gcp_integration"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	cfg = NewBuildConfig(def.GOOS, "s390x", def.Cgo)

	got, want = cfg.String(), "GOARCH=s390x"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestDefaultBuildConfig_MirrorsGoBuildDefault(t *testing.T) {
	def := DefaultBuildConfig()
	if def.GOOS != build.Default.GOOS || def.GOARCH != build.Default.GOARCH || def.Cgo != build.Default.CgoEnabled {
		t.Errorf("DefaultBuildConfig() = %+v", def)
	}
}

func TestBuildConfig_MatchesFile(t *testing.T) {
	linux := NewBuildConfig("linux", "amd64", false)

	tests := []struct {
		name string
		cfg  BuildConfig
		file string
		src  string
		want bool
	}{
		{"plain", linux, "a.go", "package p\n", true},
		{"tag absent", linux, "a.go", "//go:build fts5\n\npackage p\n", false},
		{"tag present", NewBuildConfig("linux", "amd64", false, "fts5"), "a.go", "//go:build fts5\n\npackage p\n", true},
		{"negated tag", linux, "a.go", "//go:build !fts5\n\npackage p\n", true},
		{"legacy ignore", linux, "a.go", "// +build ignore\n\npackage p\n", false},
		{"os suffix mismatch", linux, "a_windows.go", "package p\n", false},
		{"os suffix match", NewBuildConfig("windows", "amd64", false), "a_windows.go", "package p\n", true},
		{"unix tag", linux, "a.go", "//go:build unix\n\npackage p\n", true},
		{"ios implies darwin", NewBuildConfig("ios", "arm64", true), "a_darwin.go", "package p\n", true},
		{"underscore prefix", linux, "_a.go", "package p\n", false},
		{"cgo import without cgo", linux, "a.go", "package p\n\nimport \"C\"\n", false},
		{"cgo import with cgo", NewBuildConfig("linux", "amd64", true), "a.go", "package p\n\nimport \"C\"\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.MatchesFile(newSourceFile(t, tt.file, tt.src))
			if got != tt.want {
				t.Errorf("MatchesFile = %v, want %v", got, tt.want)
			}
		})
	}
}
