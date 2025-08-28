package main

import (
	"github.com/mamaar/gorefactor/internal/cli"
	"github.com/mamaar/gorefactor/internal/cli/commands"
)

func main() {
	// Create and initialize the application
	app := cli.NewApp()
	app.Initialize()

	// Register all commands with the runner
	registerCommands()

	// Run the application with the runner
	app.Run(runner)
}

// Temporary wrapper for the runner - will be cleaned up in Phase 2
var runner *cli.Runner

func init() {
	runner = cli.NewRunner()
}

// registerCommands registers all command handlers using the new command modules
func registerCommands() {
	// Simple commands
	runner.RegisterCommand("version", commands.VersionCommand)
	runner.RegisterCommand("delete", commands.DeleteCommand)
	
	// Medium complexity commands
	runner.RegisterCommand("analyze", commands.AnalyzeCommand)
	runner.RegisterCommand("complexity", commands.ComplexityCommand)
	runner.RegisterCommand("unused", commands.UnusedCommand)
	
	// Complex commands  
	runner.RegisterCommand("extract", commands.ExtractCommand)
	runner.RegisterCommand("inline", commands.InlineCommand)
	runner.RegisterCommand("rename", commands.RenameCommand)
	
	// Most complex commands (symbol and package movement)
	runner.RegisterCommand("move", commands.MoveCommand)
	runner.RegisterCommand("move-package", commands.MovePackageCommand)
	runner.RegisterCommand("move-dir", commands.MoveDirCommand)
	runner.RegisterCommand("move-packages", commands.MovePackagesCommand)
	
	// Facade commands
	runner.RegisterCommand("create-facade", commands.CreateFacadeCommand)
	runner.RegisterCommand("generate-facades", commands.GenerateFacadesCommand)
	runner.RegisterCommand("update-facades", commands.UpdateFacadesCommand)
	
	// Import alias commands
	runner.RegisterCommand("clean-aliases", commands.CleanAliasesCommand)
	runner.RegisterCommand("standardize-imports", commands.StandardizeImportsCommand)
	runner.RegisterCommand("resolve-alias-conflicts", commands.ResolveAliasConflictsCommand)
	runner.RegisterCommand("convert-aliases", commands.ConvertAliasesCommand)
	
	// Dependency analysis commands
	runner.RegisterCommand("move-by-dependencies", commands.MoveByDependenciesCommand)
	runner.RegisterCommand("organize-by-layers", commands.OrganizeByLayersCommand)
	runner.RegisterCommand("fix-cycles", commands.FixCyclesCommand)
	runner.RegisterCommand("analyze-dependencies", commands.AnalyzeDependenciesCommand)
	
	// Batch operation commands
	runner.RegisterCommand("batch", commands.BatchCommand)
	runner.RegisterCommand("plan", commands.PlanCommand)
	runner.RegisterCommand("execute", commands.ExecuteCommand)
	runner.RegisterCommand("rollback", commands.RollbackCommand)
	runner.RegisterCommand("change", commands.ChangeCommand)
	
	// Help command
	runner.RegisterCommand("help", commands.HelpCommand)
}