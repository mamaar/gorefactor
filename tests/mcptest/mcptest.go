// Package mcptest provides test helpers for invoking gorefactor MCP tools
// with swappable transports: in-process (fast) or subprocess (full binary).
package mcptest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	internalmcp "github.com/mamaar/gorefactor/internal/mcp"
)

// Session wraps an MCP ClientSession with cleanup logic.
type Session struct {
	*mcpsdk.ClientSession
	cancel context.CancelFunc
	state  *internalmcp.MCPServer // non-nil only for in-process
}

// Close tears down the session.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}

	if s.state != nil {
		s.state.Close()
	}
}

// Transport selects how the MCP server is reached.
type Transport interface {
	connect(ctx context.Context) (*Session, error)
}

// Dial connects to an MCP server using the given transport,
// then calls load_workspace with workspacePath.
func Dial(ctx context.Context, tb testing.TB, transport Transport, workspacePath string) *Session {
	tb.Helper()

	sess, err := transport.connect(ctx)
	if err != nil {
		tb.Fatalf("mcptest.Dial: connect: %v", err)
	}
	// Load the workspace via the MCP tool.
	result, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "load_workspace",
		Arguments: map[string]any{"path": workspacePath},
	})
	if err != nil {
		sess.Close()
		tb.Fatalf("mcptest.Dial: load_workspace: %v", err)
	}

	if result.IsError {
		sess.Close()
		tb.Fatalf("mcptest.Dial: load_workspace returned error: %v", result.Content)
	}

	return sess
}

// inProcess is the in-process transport using NewInMemoryTransports.
type inProcess struct {
	clientOpts  *mcpsdk.ClientOptions
	sessionOpts *mcpsdk.ClientSessionOptions
}

// InProcess returns a transport that runs the MCP server in-process. The
// resulting client does not set an ElicitationHandler, so it never
// advertises the elicitation capability.
func InProcess() Transport { return inProcess{} }

// InProcessWithElicitation returns an in-process transport whose client uses
// handler to answer MCP elicitation requests (e.g. the safe_delete
// confirmation dialog). Setting a non-nil handler makes the SDK client
// automatically advertise the elicitation capability during initialize;
// pass a non-nil handler even if the test expects it never to be called, so
// the capability is advertised and the resulting call proves that.
func InProcessWithElicitation(handler func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error)) Transport {
	return inProcess{clientOpts: &mcpsdk.ClientOptions{ElicitationHandler: handler}}
}

// InProcessWithClient returns an in-process transport whose client uses the
// given options, for tests that pin its capabilities or protocol version.
func InProcessWithClient(clientOpts *mcpsdk.ClientOptions, sessionOpts *mcpsdk.ClientSessionOptions) Transport {
	return inProcess{clientOpts: clientOpts, sessionOpts: sessionOpts}
}

func (ip inProcess) connect(ctx context.Context) (*Session, error) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := internalmcp.NewMCPServer(logger)

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "gorefactor", Version: "test"}, nil)
	internalmcp.RegisterAllTools(server, state)

	serverT, clientT := mcpsdk.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(ctx)
	go func() { _ = server.Run(ctx, serverT) }()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0"}, ip.clientOpts)

	session, err := client.Connect(ctx, clientT, ip.sessionOpts)
	if err != nil {
		cancel()
		state.Close()

		return nil, fmt.Errorf("connect: %w", err)
	}

	return &Session{ClientSession: session, cancel: cancel, state: state}, nil
}

// subprocess is the subprocess transport using CommandTransport.
type subprocess struct {
	binPath string
}

// Subprocess returns a transport that shells out to the given binary.
func Subprocess(bin string) Transport { return subprocess{binPath: bin} }

func (sp subprocess) connect(ctx context.Context) (*Session, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, sp.binPath)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0"}, nil)

	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connect: %w", err)
	}

	return &Session{ClientSession: session, cancel: cancel}, nil
}
