package types

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolvePackagePath resolves a user-provided package reference to the key of a
// package that ALREADY EXISTS in the workspace. Every strategy below is a
// lookup against workspace.Packages or workspace.ImportToPath, so a package
// that has yet to be created cannot resolve: the input falls through and is
// returned verbatim, on the assumption that the caller is about to fail with a
// "not found" error naming what the user typed.
//
// That makes this the wrong function for the DESTINATION of a move, a facade,
// or anything else that names a package which does not exist yet — for those,
// falling through hands the caller an unresolved string it will then treat as
// a filesystem path. Use ResolveNewPackagePath instead.
func ResolvePackagePath(workspace *Workspace, userPath string) string {
	// Strategy 1: Try exact match (for absolute paths)
	_, exists := workspace.Packages[userPath]
	if exists {
		return userPath
	}

	// Strategy 2: Try relative to workspace root
	absPath := filepath.Join(workspace.RootPath, userPath)
	_, exists = workspace.Packages[absPath]
	if exists {
		return absPath
	}

	// Strategy 2.5: Try as a Go import path (e.g. "scratch/pkg/widget").
	// workspace_status and most callers report packages by import path, so this
	// is the form users actually have in hand; ImportToPath maps it back to the
	// filesystem path that Packages is keyed by.
	if workspace.ImportToPath != nil {
		fsPath, exists := workspace.ImportToPath[userPath]
		if exists {
			return fsPath
		}
	}

	// Strategy 3: Try as "." for current directory
	if userPath == "." {
		_, exists := workspace.Packages[workspace.RootPath]
		if exists {
			return workspace.RootPath
		}
	}

	// Strategy 4: Try to find by Go package name (only if unique)
	var matchedPath string
	matchCount := 0
	for pkgPath, pkg := range workspace.Packages {
		if pkg.Name != userPath {
			continue
		}
		matchedPath = pkgPath
		matchCount++
		if matchCount > 1 {
			break
		}
	}
	if matchCount == 1 {
		return matchedPath
	}

	// If nothing matches, return the user input (will trigger helpful error message)
	return userPath
}

// ResolveNewPackagePath resolves a user-provided package reference to an
// absolute filesystem path, for a package that need not exist yet. It is the
// destination-side counterpart to ResolvePackagePath: where that one asks
// "which existing package is this?", this one asks "where on disk would this
// package live?", which is answerable for a directory that has never been
// created.
//
// Three input forms are accepted, in this order:
//
//  1. An absolute path, used as-is.
//  2. A module import path ("feltgis/pkg/wms"), whose module prefix is
//     stripped and the remainder joined onto the workspace root.
//  3. Anything else, treated as relative to the workspace root ("pkg/wms").
//
// Import paths are tried before workspace-relative paths because that is the
// form callers actually have in hand — workspace_status and every analysis
// tool report packages by import path. The cost is that a literal top-level
// directory whose name collides with the module path (a directory "feltgis/"
// inside module "feltgis") is unreachable by name; pass an absolute path for
// that case.
//
// The result is always confirmed to lie within workspace.RootPath. A refactor
// writing outside the workspace it was pointed at is a bug in every case, and
// this is the layer that can still tell the difference between a path that
// escaped and one that was meant.
func ResolveNewPackagePath(workspace *Workspace, userPath string) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("package path cannot be empty")
	}
	if workspace == nil || workspace.RootPath == "" {
		return "", fmt.Errorf("cannot resolve %q: workspace has no root path", userPath)
	}

	resolved := ""
	switch {
	case filepath.IsAbs(userPath):
		resolved = filepath.Clean(userPath)

	case isModuleImportPath(workspace, userPath):
		rel := strings.TrimPrefix(userPath, workspace.Module.Path)
		rel = strings.TrimPrefix(rel, "/")
		// The module path alone denotes the root package itself.
		resolved = filepath.Join(workspace.RootPath, filepath.FromSlash(rel))

	default:
		resolved = filepath.Join(workspace.RootPath, filepath.FromSlash(userPath))
	}

	rel, err := filepath.Rel(workspace.RootPath, resolved)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q against workspace root %s: %w", userPath, workspace.RootPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("package path %q resolves to %s, which is outside the workspace root %s",
			userPath, resolved, workspace.RootPath)
	}

	return resolved, nil
}

// isModuleImportPath reports whether userPath is the workspace module's own
// import path or something beneath it.
func isModuleImportPath(workspace *Workspace, userPath string) bool {
	if workspace.Module == nil || workspace.Module.Path == "" {
		return false
	}
	return userPath == workspace.Module.Path ||
		strings.HasPrefix(userPath, workspace.Module.Path+"/")
}
