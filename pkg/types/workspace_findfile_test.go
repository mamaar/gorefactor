package types

import (
	"strings"
	"testing"
)

func TestFindFile_BareNameMustBeUnique(t *testing.T) {
	file := func(path string) *File { return &File{Path: path} }
	ws := &Workspace{
		RootPath: "/ws",
		Packages: map[string]*Package{
			"/ws/a": {Files: map[string]*File{"x.go": file("/ws/a/x.go")}},
			"/ws/b": {
				Files:     map[string]*File{"x.go": file("/ws/b/x.go"), "y.go": file("/ws/b/y.go")},
				TestFiles: map[string]*File{"y_test.go": file("/ws/b/y_test.go")},
			},
		},
	}

	_, _, err := ws.FindFile("x.go")
	if err == nil || !strings.Contains(err.Error(), "ambiguous source file x.go") ||
		!strings.Contains(err.Error(), "/ws/a/x.go") || !strings.Contains(err.Error(), "/ws/b/x.go") {
		t.Errorf("bare x.go: want ambiguity error naming both files, got %v", err)
	}
	for _, tc := range []struct{ path, want string }{
		{"y.go", "/ws/b/y.go"},
		{"y_test.go", "/ws/b/y_test.go"},
		{"a/x.go", "/ws/a/x.go"},
		{"/ws/b/x.go", "/ws/b/x.go"},
	} {
		f, _, err := ws.FindFile(tc.path)
		if err != nil || f == nil || f.Path != tc.want {
			t.Errorf("FindFile(%q) = %v, %v; want %s", tc.path, f, err, tc.want)
		}
	}
	f, _, err := ws.FindFile("nope.go")
	if f != nil || err != nil {
		t.Errorf("missing file: got %v, %v", f, err)
	}
}
