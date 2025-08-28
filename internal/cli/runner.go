package cli

import (
	"fmt"
	"os"
)

// CommandFunc represents a command function signature
type CommandFunc func([]string)

// Runner handles command routing and execution
type Runner struct {
	commands map[string]CommandFunc
}

// NewRunner creates a new command runner
func NewRunner() *Runner {
	return &Runner{
		commands: make(map[string]CommandFunc),
	}
}

// RegisterCommand registers a command handler
func (r *Runner) RegisterCommand(name string, fn CommandFunc) {
	r.commands[name] = fn
}

// Execute runs the specified command with arguments
func (r *Runner) Execute(command string, args []string) {
	if fn, ok := r.commands[command]; ok {
		fn(args)
	} else {
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		Usage()
		os.Exit(1)
	}
}

// GetCommands returns the registered commands
func (r *Runner) GetCommands() map[string]CommandFunc {
	return r.commands
}