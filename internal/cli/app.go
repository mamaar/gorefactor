package cli

import (
	"flag"
	"log"
	"os"

	"github.com/mamaar/gorefactor/pkg/refactor"
)

// App represents the gorefactor application
type App struct {
	flags *Flags
}

// NewApp creates a new application instance
func NewApp() *App {
	return &App{}
}

// Initialize sets up the application with flags and configuration
func (app *App) Initialize() {
	log.SetFlags(0) // Remove timestamp from log output
	ParseFlags(Usage)
	app.flags = GlobalFlags
}

// Run executes the application logic with the provided runner
func (app *App) Run(runner *Runner) {
	// Handle version flag
	if *app.flags.Version {
		ShowVersion()
		return
	}

	// Get command arguments
	args := flag.Args()
	if len(args) < 1 {
		Usage()
		os.Exit(1)
	}

	// Execute the command
	runner.Execute(args[0], args[1:])
}

// CreateEngineWithFlags creates a refactor engine with configuration based on command line flags
func CreateEngineWithFlags() refactor.RefactorEngine {
	config := &refactor.EngineConfig{
		SkipCompilation: *GlobalFlags.SkipCompilation,
		AllowBreaking:   *GlobalFlags.AllowBreaking,
	}
	return refactor.CreateEngineWithConfig(config)
}