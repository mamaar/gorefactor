package cli

import "flag"

// Flags holds all command line flags
type Flags struct {
	Version              *bool
	Workspace            *string
	DryRun               *bool
	Json                 *bool
	Verbose              *bool
	Force                *bool
	Backup               *bool
	PackageOnly          *bool
	CreateTarget         *bool
	SkipCompilation      *bool
	AllowBreaking        *bool
	MinComplexity        *int
	RenameImplementations *bool
}

// GlobalFlags holds the parsed command line flags
var GlobalFlags *Flags

// InitFlags initializes all command line flags
func InitFlags() *Flags {
	return &Flags{
		Version:               flag.Bool("version", false, "Show version information"),
		Workspace:             flag.String("workspace", ".", "Path to workspace root (defaults to current directory)"),
		DryRun:                flag.Bool("dry-run", false, "Preview changes without applying them"),
		Json:                  flag.Bool("json", false, "Output results in JSON format"),
		Verbose:               flag.Bool("verbose", false, "Enable verbose output"),
		Force:                 flag.Bool("force", false, "Force operation even with warnings"),
		Backup:                flag.Bool("backup", true, "Create backup files before making changes"),
		PackageOnly:           flag.Bool("package-only", false, "For rename operations, only rename within the specified package"),
		CreateTarget:          flag.Bool("create-target", true, "Create target package if it doesn't exist"),
		SkipCompilation:       flag.Bool("skip-compilation", false, "Skip compilation validation after refactoring"),
		AllowBreaking:         flag.Bool("allow-breaking", false, "Allow potentially breaking refactorings that may require manual fixes"),
		MinComplexity:         flag.Int("min-complexity", 10, "Minimum complexity threshold for complexity analysis"),
		RenameImplementations: flag.Bool("rename-implementations", false, "When renaming interface methods, also rename all implementations"),
	}
}

// ParseFlags parses command line flags with custom usage
func ParseFlags(usage func()) {
	if GlobalFlags == nil {
		GlobalFlags = InitFlags()
	}
	flag.Usage = usage
	flag.Parse()
}