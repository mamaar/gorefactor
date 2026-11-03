package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConfirmAccepted(t *testing.T) {
	for name, tt := range map[string]struct {
		answer mcpsdk.InputResponse
		want   bool
	}{
		"accept with true":         {&mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, true},
		"accept without the key":   {&mcpsdk.ElicitResult{Action: "accept"}, true},
		"accept with false":        {&mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}, false},
		"accept with a string":     {&mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": "false"}}, false},
		"accept with a null":       {&mcpsdk.ElicitResult{Action: "accept", Content: map[string]any{"confirm": nil}}, false},
		"decline":                  {&mcpsdk.ElicitResult{Action: "decline"}, false},
		"cancel":                   {&mcpsdk.ElicitResult{Action: "cancel"}, false},
		"unrecognized action":      {&mcpsdk.ElicitResult{Action: "maybe"}, false},
		"response of another type": {&mcpsdk.ListRootsResult{}, false}, //nolint:staticcheck // every non-elicit InputResponse type is deprecated
	} {
		t.Run(name, func(t *testing.T) {
			got := confirmAccepted(tt.answer)
			if got != tt.want {
				t.Errorf("confirmAccepted = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanConfirm(t *testing.T) {
	for name, tt := range map[string]struct {
		caps *mcpsdk.ClientCapabilities
		want bool
	}{
		"no elicitation": {&mcpsdk.ClientCapabilities{}, false},
		"neither mode":   {&mcpsdk.ClientCapabilities{Elicitation: &mcpsdk.ElicitationCapabilities{}}, true},
		"form":           {&mcpsdk.ClientCapabilities{Elicitation: &mcpsdk.ElicitationCapabilities{Form: &mcpsdk.FormElicitationCapabilities{}}}, true},
		"form and url": {&mcpsdk.ClientCapabilities{Elicitation: &mcpsdk.ElicitationCapabilities{
			Form: &mcpsdk.FormElicitationCapabilities{}, URL: &mcpsdk.URLElicitationCapabilities{},
		}}, true},
		"url only": {&mcpsdk.ClientCapabilities{Elicitation: &mcpsdk.ElicitationCapabilities{URL: &mcpsdk.URLElicitationCapabilities{}}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "gorefactor-test", Version: "test"}, nil)
			serverT, clientT := mcpsdk.NewInMemoryTransports()

			serverSession, err := server.Connect(ctx, serverT, nil)
			if err != nil {
				t.Fatalf("server.Connect: %v", err)
			}

			t.Cleanup(func() { _ = serverSession.Close() })

			// A "2025-11-25" client states its capabilities in the handshake,
			// which is what the session holds before any request arrives.
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "1.0"}, &mcpsdk.ClientOptions{Capabilities: tt.caps})

			clientSession, err := client.Connect(ctx, clientT, &mcpsdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
			if err != nil {
				t.Fatalf("client.Connect: %v", err)
			}

			t.Cleanup(func() { _ = clientSession.Close() })

			got := canConfirm(serverSession)
			if got != tt.want {
				t.Errorf("canConfirm = %v, want %v", got, tt.want)
			}
		})
	}
}
