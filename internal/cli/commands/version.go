package commands

import (
	"fmt"

	"github.com/mamaar/gorefactor/internal/cli"
)

// VersionCommand handles the version command
func VersionCommand(args []string) {
	if len(args) > 0 {
		// If any arguments provided, show help
		fmt.Println(`Version Command - Show application version

Usage: gorefactor version

Shows the current version of gorefactor.`)
		return
	}
	
	cli.ShowVersion()
}