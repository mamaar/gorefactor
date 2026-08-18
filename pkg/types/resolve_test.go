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

// TestResolveNewPackagePath covers the destination-side resolver. Its whole
// reason to exist is that ResolvePackagePath cannot resolve a package that
// does not exist yet, which is every move destination and every facade.
func TestResolveNewPackagePath(t *testing.T) {
	ws := &Workspace{
		RootPath: "/repo/go",
		Module:   &Module{Path: "feltgis"},
		Packages: map[string]*Package{
			"/repo/go/kartadm/wms": {Path: "/repo/go/kartadm/wms", Name: "wms"},
		},
	}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"module import path", "feltgis/pkg/wms", "/repo/go/pkg/wms"},
		{"workspace relative path", "pkg/wms", "/repo/go/pkg/wms"},
		{"absolute path", "/repo/go/pkg/wms", "/repo/go/pkg/wms"},
		{"module path alone is the root package", "feltgis", "/repo/go"},
		// The destination need not exist; that is the point. It also need not
		// be new — an existing package resolves to its own directory, and it is
		// the caller's job to decide whether landing there is allowed.
		{"existing package", "kartadm/wms", "/repo/go/kartadm/wms"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveNewPackagePath(ws, tc.input)
			if err != nil {
				t.Fatalf("ResolveNewPackagePath(%q) returned an error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("ResolveNewPackagePath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestResolveNewPackagePath_RejectsEscapes is the guard that matters: a
// refactor writing outside the workspace it was pointed at is a bug in every
// case, and this is the last layer that can still tell an escape from an
// intent.
func TestResolveNewPackagePath_RejectsEscapes(t *testing.T) {
	ws := &Workspace{
		RootPath: "/repo/go",
		Module:   &Module{Path: "feltgis"},
	}

	for _, input := range []string{
		"../outside",
		"pkg/../../outside",
		"/etc/passwd",
		"",
	} {
		if got, err := ResolveNewPackagePath(ws, input); err == nil {
			t.Errorf("ResolveNewPackagePath(%q) = %q, want an error", input, got)
		}
	}
}

// TestResolveNewPackagePath_ImportPathWins documents the one ambiguity the
// ordering creates: with module "feltgis", the input "feltgis/pkg" is read as
// an import path, so a literal top-level directory named "feltgis" is
// unreachable by that name. An absolute path is the way to reach it.
func TestResolveNewPackagePath_ImportPathWins(t *testing.T) {
	ws := &Workspace{RootPath: "/repo/go", Module: &Module{Path: "feltgis"}}

	got, err := ResolveNewPackagePath(ws, "feltgis/pkg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/repo/go/pkg" {
		t.Fatalf("got %q, want %q (the import path reading)", got, "/repo/go/pkg")
	}

	got, err = ResolveNewPackagePath(ws, "/repo/go/feltgis/pkg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/repo/go/feltgis/pkg" {
		t.Fatalf("got %q, want the literal directory", got)
	}
}
