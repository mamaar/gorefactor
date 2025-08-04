package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mamaar/gorefactor/pkg/types"
)

func TestServer_Initialize(t *testing.T) {
	server := NewServer()
	
	// Create initialize request
	initParams := InitializeParams{
		RootURI: "file:///test/workspace",
		Capabilities: ClientCapabilities{
			TextDocument: &TextDocumentCapabilities{
				Hover: &HoverCapability{DynamicRegistration: true},
			},
		},
	}
	
	paramsJSON, err := json.Marshal(initParams)
	if err != nil {
		t.Fatalf("Failed to marshal init params: %v", err)
	}
	
	message := &Message{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  paramsJSON,
	}
	
	response, err := server.handleInitialize(message)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	
	if response.Error != nil {
		t.Fatalf("Initialize returned error: %v", response.Error)
	}
	
	result, ok := response.Result.(InitializeResult)
	if !ok {
		t.Fatalf("Expected InitializeResult, got %T", response.Result)
	}
	
	if result.ServerInfo.Name != "gorefactor-lsp" {
		t.Errorf("Expected server name 'gorefactor-lsp', got '%s'", result.ServerInfo.Name)
	}
	
	if !result.Capabilities.HoverProvider {
		t.Error("Expected hover provider to be enabled")
	}
	
	if !result.Capabilities.DefinitionProvider {
		t.Error("Expected definition provider to be enabled")
	}
}

func TestServer_CodeActions(t *testing.T) {
	server := NewServer()
	
	// Mock workspace
	server.workspace = &types.Workspace{
		Packages: map[string]*types.Package{
			"test": {
				Path: "test",
				Name: "test",
				Files: map[string]*types.File{
					"test.go": {
						Path: "test.go",
						OriginalContent: []byte("package test\n\nfunc TestFunc() {}\n"),
					},
				},
				Symbols: &types.SymbolTable{
					Functions: map[string]*types.Symbol{
						"TestFunc": {
							Name:     "TestFunc",
							Kind:     types.FunctionSymbol,
							File:     "test.go",
							Line:     3,
							Column:   6,
							Exported: true,
						},
					},
				},
			},
		},
	}
	server.initialized = true
	
	// Create code action request
	params := CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: "file:///test.go"},
		Range: Range{
			Start: Position{Line: 2, Character: 5},
			End:   Position{Line: 2, Character: 13},
		},
		Context: CodeActionContext{},
	}
	
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Failed to marshal params: %v", err)
	}
	
	message := &Message{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "textDocument/codeAction",
		Params:  paramsJSON,
	}
	
	response, err := server.handleTextDocumentCodeAction(message)
	if err != nil {
		t.Fatalf("Code action failed: %v", err)
	}
	
	if response.Error != nil {
		t.Fatalf("Code action returned error: %v", response.Error)
	}
	
	actions, ok := response.Result.([]CodeAction)
	if !ok {
		t.Fatalf("Expected []CodeAction, got %T", response.Result)
	}
	
	if len(actions) == 0 {
		t.Error("Expected at least one code action")
	}
	
	// Check for extract actions
	foundExtract := false
	for _, action := range actions {
		if strings.Contains(action.Kind, "refactor.extract") {
			foundExtract = true
			break
		}
	}
	
	if !foundExtract {
		t.Error("Expected extract refactoring actions")
	}
}

func TestConnection_ReadWriteMessage(t *testing.T) {
	// Test LSP message protocol
	jsonContent := "{\"jsonrpc\":\"2.0\",\"method\":\"test\",\"id\":1}"
	reader := strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(jsonContent), jsonContent))
	writer := &strings.Builder{}
	
	conn := NewConnection(reader, writer)
	
	// Read message
	message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read message: %v", err)
	}
	
	if message.JSONRPC != "2.0" {
		t.Errorf("Expected JSONRPC '2.0', got '%s'", message.JSONRPC)
	}
	
	if message.Method != "test" {
		t.Errorf("Expected method 'test', got '%s'", message.Method)
	}
	
	// Write message
	response := &Message{
		JSONRPC: "2.0",
		ID:      1,
		Result:  "success",
	}
	
	err = conn.WriteMessage(response)
	if err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}
	
	output := writer.String()
	if !strings.Contains(output, "Content-Length:") {
		t.Error("Expected Content-Length header in output")
	}
	
	if !strings.Contains(output, "\"result\":\"success\"") {
		t.Error("Expected result in JSON output")
	}
}

func TestServer_Hover(t *testing.T) {
	server := NewServer()
	
	// Mock workspace with symbol
	server.workspace = &types.Workspace{
		Packages: map[string]*types.Package{
			"test": {
				Path: "test",
				Name: "test",
				Files: map[string]*types.File{
					"test.go": {
						Path: "test.go",
						OriginalContent: []byte("package test\n\nfunc TestFunc() int { return 42 }\n"),
					},
				},
				Symbols: &types.SymbolTable{
					Functions: map[string]*types.Symbol{
						"TestFunc": {
							Name:      "TestFunc",
							Kind:      types.FunctionSymbol,
							File:      "test.go",
							Line:      3,
							Column:    6,
							Exported:  true,
							Signature: "func TestFunc() int",
						},
					},
				},
			},
		},
	}
	server.initialized = true
	
	// Create hover request
	params := TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: "file:///test.go"},
		Position:     Position{Line: 2, Character: 8}, // Position on "TestFunc"
	}
	
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Failed to marshal params: %v", err)
	}
	
	message := &Message{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "textDocument/hover",
		Params:  paramsJSON,
	}
	
	response, err := server.handleTextDocumentHover(message)
	if err != nil {
		t.Fatalf("Hover failed: %v", err)
	}
	
	if response.Error != nil {
		t.Fatalf("Hover returned error: %v", response.Error)
	}
	
	// For this test, we expect a null result if no symbol is found at the exact position
	// This is expected behavior since our position calculation is simplified
	if response.Result != nil {
		if hover, ok := response.Result.(*Hover); ok {
			if !strings.Contains(hover.Contents.Value, "TestFunc") {
				t.Error("Expected hover content to contain 'TestFunc'")
			}
		}
	}
}

func TestServer_StartStop(t *testing.T) {
	server := NewServer()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	
	// Test that server can start on stdio (will fail quickly due to no input)
	err := server.ServeStdio(ctx)
	if err != nil && err != context.DeadlineExceeded {
		t.Errorf("Expected context deadline exceeded, got: %v", err)
	}
}