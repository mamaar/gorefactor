package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/mamaar/gorefactor/internal/lsp"
)

var (
	flagPort    = flag.Int("port", 0, "Port to listen on (0 for stdio)")
	flagDebug   = flag.Bool("debug", false, "Enable debug logging")
	flagLogFile = flag.String("logfile", "", "Log file path (default: /tmp/gorefactor-lsp.log)")
	flagVersion = flag.Bool("version", false, "Show version information")
)

const version = "0.1.0"

func main() {
	flag.Parse()

	if *flagVersion {
		fmt.Printf("gorefactor-lsp version %s\n", version)
		os.Exit(0)
	}

	// Setup file logging
	logFile := *flagLogFile
	if logFile == "" {
		logFile = "/tmp/gorefactor-lsp.log"
	}

	// Ensure log directory exists
	logDir := filepath.Dir(logFile)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create log directory: %v\n", err)
		os.Exit(1)
	}

	// Open log file
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open log file %s: %v\n", logFile, err)
		os.Exit(1)
	}
	defer file.Close()

	// Setup logging to file
	log.SetOutput(file)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Log startup
	log.Printf("=== GoRefactor LSP Server Starting ===")
	log.Printf("Version: %s", version)
	log.Printf("PID: %d", os.Getpid())
	log.Printf("Args: %v", os.Args)
	log.Printf("Port: %d (0=stdio)", *flagPort)
	log.Printf("Debug: %v", *flagDebug)
	log.Printf("Log file: %s", logFile)
	log.Printf("Working directory: %s", func() string {
		wd, _ := os.Getwd()
		return wd
	}())
	log.Printf("Timestamp: %s", time.Now().Format(time.RFC3339))

	// Create and start the LSP server
	server := lsp.NewServer()
	
	log.Printf("LSP server created, starting...")
	
	ctx := context.Background()
	if err := server.Start(ctx, *flagPort); err != nil {
		log.Fatalf("LSP server failed to start: %v", err)
	}
}