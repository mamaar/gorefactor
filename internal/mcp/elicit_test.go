package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newElicitTestPair spins up an in-memory server+client pair (matching the
// pattern used by tests/mcptest.Dial / tests/mcptest.InProcess), connects
// the client with the given elicitation handler, and returns the server's
// *mcpsdk.ServerSession that confirm() should be called against.
//
// If handler is nil, the client does not set ElicitationHandler at all, so
// it never advertises the elicitation capability - this is the graceful
// degradation path confirm must detect.
func newElicitTestPair(ctx context.Context, t *testing.T, handler func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error)) *mcpsdk.ServerSession {
	t.Helper()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "gorefactor-test", Version: "test"}, nil)
	serverT, clientT := mcpsdk.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0"}, &mcpsdk.ClientOptions{
		ElicitationHandler: handler,
	})
	clientSession, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { clientSession.Close() })

	return serverSession
}

func TestConfirm_Accept(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess := newElicitTestPair(ctx, t, func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	})

	ok, err := confirm(ctx, sess, "proceed?")
	if err != nil {
		t.Fatalf("confirm returned error: %v", err)
	}
	if !ok {
		t.Fatalf("confirm returned false, want true")
	}
}

func TestConfirm_Decline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess := newElicitTestPair(ctx, t, func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		return &mcpsdk.ElicitResult{Action: "decline"}, nil
	})

	ok, err := confirm(ctx, sess, "proceed?")
	if err != nil {
		t.Fatalf("confirm returned error: %v", err)
	}
	if ok {
		t.Fatalf("confirm returned true, want false")
	}
}

func TestConfirm_Cancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess := newElicitTestPair(ctx, t, func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		return &mcpsdk.ElicitResult{Action: "cancel"}, nil
	})

	ok, err := confirm(ctx, sess, "proceed?")
	if err != nil {
		t.Fatalf("confirm returned error: %v", err)
	}
	if ok {
		t.Fatalf("confirm returned true, want false")
	}
}

func TestConfirm_ClientUnsupported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// No ElicitationHandler set -> the client never advertises the
	// elicitation capability. This is the graceful-degradation path that
	// keeps non-elicitation-aware clients (and the rest of the test suite)
	// working.
	sess := newElicitTestPair(ctx, t, nil)

	ok, err := confirm(ctx, sess, "proceed?")
	if ok {
		t.Fatalf("confirm returned true, want false")
	}
	if !errors.Is(err, errElicitUnsupported) {
		t.Fatalf("confirm error = %v, want errors.Is(err, errElicitUnsupported)", err)
	}
}

func TestConfirm_NilSession(t *testing.T) {
	ok, err := confirm(context.Background(), nil, "proceed?")
	if ok {
		t.Fatalf("confirm returned true, want false")
	}
	if !errors.Is(err, errElicitUnsupported) {
		t.Fatalf("confirm error = %v, want errors.Is(err, errElicitUnsupported)", err)
	}
}

func TestConfirm_AcceptMissingConfirmKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A missing "confirm" key on an "accept" action should be treated as
	// an implicit yes.
	sess := newElicitTestPair(ctx, t, func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		return &mcpsdk.ElicitResult{Action: "accept"}, nil
	})

	ok, err := confirm(ctx, sess, "proceed?")
	if err != nil {
		t.Fatalf("confirm returned error: %v", err)
	}
	if !ok {
		t.Fatalf("confirm returned false, want true")
	}
}

func TestConfirm_AcceptExplicitFalse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// An explicit false "confirm" value on an "accept" action should be
	// treated as a decline.
	sess := newElicitTestPair(ctx, t, func(context.Context, *mcpsdk.ElicitRequest) (*mcpsdk.ElicitResult, error) {
		return &mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}, nil
	})

	ok, err := confirm(ctx, sess, "proceed?")
	if err != nil {
		t.Fatalf("confirm returned error: %v", err)
	}
	if ok {
		t.Fatalf("confirm returned true, want false")
	}
}
