package cli

import (
	"fmt"
	"os"
)

// Version is the current version of gorefactor
const Version = "0.1.0"

// ShowVersion displays the version information and exits
func ShowVersion() {
	fmt.Printf("gorefactor version %s\n", Version)
	os.Exit(0)
}