package types

import "testing"

// TestResolvePackagePath_ImportPath covers the bug where a user (or an MCP
// caller mirroring workspace_status output) supplies a Go import path such as
// "scratch/pkg/widget" rather than a filesystem path. Packages is keyed
// by filesystem path, so without consulting ImportToPath the import path
// never resolves and callers see a spurious "not found in workspace" error
// even though the package is loaded.
func TestResolvePackagePath_ImportPath(t *testing.T) {
	ws := &Workspace{
		RootPath: "/repo/go",
		Packages: map[string]*Package{
			"/repo/go/pkg/widget": {
				Path:       "/repo/go/pkg/widget",
				ImportPath: "scratch/pkg/widget",
				Name:       "widget",
			},
		},
		ImportToPath: map[string]string{
			"scratch/pkg/widget": "/repo/go/pkg/widget",
		},
	}

	got := ResolvePackagePath(ws, "scratch/pkg/widget")
	want := "/repo/go/pkg/widget"
	if got != want {
		t.Fatalf("ResolvePackagePath(%q) = %q, want %q", "scratch/pkg/widget", got, want)
	}
}

// TestResolvePackagePath_FilesystemPathStillWorks ensures the pre-existing
// filesystem-path resolution strategies are unaffected by adding the
// import-path lookup.
func TestResolvePackagePath_FilesystemPathStillWorks(t *testing.T) {
	ws := &Workspace{
		RootPath: "/repo/go",
		Packages: map[string]*Package{
			"/repo/go/pkg/widget": {
				Path:       "/repo/go/pkg/widget",
				ImportPath: "scratch/pkg/widget",
				Name:       "widget",
			},
		},
		ImportToPath: map[string]string{
			"scratch/pkg/widget": "/repo/go/pkg/widget",
		},
	}

	got := ResolvePackagePath(ws, "/repo/go/pkg/widget")
	want := "/repo/go/pkg/widget"
	if got != want {
		t.Fatalf("ResolvePackagePath(%q) = %q, want %q", "/repo/go/pkg/widget", got, want)
	}

	got = ResolvePackagePath(ws, "pkg/widget")
	if got != want {
		t.Fatalf("ResolvePackagePath(relative) = %q, want %q", got, want)
	}
}
