package main

import (
	"context"
	"log"
	"log/slog"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	internalmcp "github.com/mamaar/gorefactor/internal/mcp"
)

func main() {
	// Create simple file logger
	logFile, err := os.OpenFile("/tmp/gorefactor.log",
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	logger.Info("MCP server starting", "version", "1.0.0")

	s := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "gorefactor",
		Version: "1.0.0",
	}, nil)

	state := internalmcp.NewMCPServer(logger)

	internalmcp.RegisterAllTools(s, state)

	logger.Info("MCP server ready")
	err = s.Run(context.Background(), &mcpsdk.StdioTransport{})
	logger.Info("MCP server shutting down")
	state.Close()
	_ = logFile.Close()
	if err != nil {
		log.Fatal(err)
	}
}
