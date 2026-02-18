package types

import "path/filepath"

// ResolvePackagePath resolves a user-provided package reference to an actual workspace package key.
func ResolvePackagePath(workspace *Workspace, userPath string) string {
	// Strategy 1: Try exact match (for absolute paths)
	if _, exists := workspace.Packages[userPath]; exists {
		return userPath
	}

	// Strategy 2: Try relative to workspace root
	absPath := filepath.Join(workspace.RootPath, userPath)
	if _, exists := workspace.Packages[absPath]; exists {
		return absPath
	}

	// Strategy 3: Try as "." for current directory
	if userPath == "." {
		if _, exists := workspace.Packages[workspace.RootPath]; exists {
			return workspace.RootPath
		}
	}

	// Strategy 4: Try to find by Go package name (only if unique)
	var matchedPath string
	matchCount := 0
	for pkgPath, pkg := range workspace.Packages {
		if pkg.Name == userPath {
			matchedPath = pkgPath
			matchCount++
			if matchCount > 1 {
				break
			}
		}
	}
	if matchCount == 1 {
		return matchedPath
	}

	// If nothing matches, return the user input (will trigger helpful error message)
	return userPath
}
