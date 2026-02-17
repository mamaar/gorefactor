package refactor

import (
	"os"
	"path/filepath"
	"strings"
)

// discoverWorkspaceModules walks up from rootPath looking for a go.work file,
// then reads each "use" directory's go.mod to collect workspace module paths.
// Returns nil (not error) if no go.work is found.
func discoverWorkspaceModules(rootPath string) ([]string, error) {
	workDir := rootPath
	var goWorkPath string

	// Walk up to find go.work.
	for {
		candidate := filepath.Join(workDir, "go.work")
		if _, err := os.Stat(candidate); err == nil {
			goWorkPath = candidate
			break
		}
		parent := filepath.Dir(workDir)
		if parent == workDir {
			break // reached filesystem root
		}
		workDir = parent
	}

	if goWorkPath == "" {
		return nil, nil
	}

	content, err := os.ReadFile(goWorkPath)
	if err != nil {
		return nil, err
	}

	useDirs := parseGoWorkFile(content)
	workRoot := filepath.Dir(goWorkPath)

	var modules []string
	for _, dir := range useDirs {
		absDir := dir
		if !filepath.IsAbs(dir) {
			absDir = filepath.Join(workRoot, dir)
		}
		modPath := filepath.Join(absDir, "go.mod")
		modContent, err := os.ReadFile(modPath)
		if err != nil {
			continue // skip directories without go.mod
		}
		moduleName := parseModuleName(modContent)
		if moduleName != "" {
			modules = append(modules, moduleName)
		}
	}

	return modules, nil
}

// parseGoWorkFile extracts the list of "use" directory paths from go.work content.
// Supports both single-line `use ./foo` and block `use ( ... )` syntax.
func parseGoWorkFile(content []byte) []string {
	var dirs []string
	lines := strings.Split(string(content), "\n")

	inUseBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip comments and empty lines.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		if inUseBlock {
			if trimmed == ")" {
				inUseBlock = false
				continue
			}
			// Each line in the block is a directory path.
			dir := strings.TrimSpace(trimmed)
			if dir != "" {
				dirs = append(dirs, dir)
			}
			continue
		}

		if strings.HasPrefix(trimmed, "use ") {
			rest := strings.TrimPrefix(trimmed, "use ")
			rest = strings.TrimSpace(rest)
			if rest == "(" {
				inUseBlock = true
				continue
			}
			// Single-line use directive.
			if rest != "" {
				dirs = append(dirs, rest)
			}
		} else if trimmed == "use(" {
			inUseBlock = true
		}
	}

	return dirs
}

// parseModuleName extracts the module path from go.mod content.
func parseModuleName(content []byte) string {
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "module "))
		}
	}
	return ""
}
