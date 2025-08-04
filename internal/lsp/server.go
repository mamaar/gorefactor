package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// Server represents the LSP server
type Server struct {
	mu           sync.RWMutex
	workspace    *types.Workspace
	engine       refactor.RefactorEngine
	rootPath     string
	initialized  bool
	capabilities ServerCapabilities
}

// NewServer creates a new LSP server instance
func NewServer() *Server {
	return &Server{
		capabilities: ServerCapabilities{
			CodeActionProvider: &CodeActionOptions{
				CodeActionKinds: []string{
					"refactor.extract",
					"refactor.extract.method",
					"refactor.extract.function",
					"refactor.extract.variable",
					"refactor.extract.constant",
					"refactor.extract.interface",
					"refactor.extract.block",
					"refactor.inline",
					"refactor.inline.method",
					"refactor.inline.function",
					"refactor.inline.variable",
					"refactor.inline.constant",
					"refactor.move",
					"refactor.move.symbol",
					"refactor.rename",
					"refactor.rename.symbol",
					"refactor.change.signature",
					"refactor.safe.delete",
					"refactor.batch",
					"source.organizeImports",
				},
			},
			HoverProvider:      true,
			DefinitionProvider: true,
			ReferencesProvider: true,
			RenameProvider:     true,
			TextDocumentSync: &TextDocumentSyncOptions{
				OpenClose: true,
				Change:    TextDocumentSyncKindIncremental,
				Save: &SaveOptions{
					IncludeText: false,
				},
			},
		},
	}
}

// Start starts the LSP server
func (s *Server) Start(ctx context.Context, port int) error {
	if port == 0 {
		// Use stdio
		return s.ServeStdio(ctx)
	} else {
		// Use TCP
		return s.ServeTCP(ctx, port)
	}
}

// ServeStdio serves the LSP over stdio
func (s *Server) ServeStdio(ctx context.Context) error {
	log.Printf("Starting LSP server on stdio")
	log.Printf("Server capabilities: %+v", s.capabilities)
	return s.serve(ctx, os.Stdin, os.Stdout)
}

// ServeTCP serves the LSP over TCP
func (s *Server) ServeTCP(ctx context.Context, port int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}
	defer listener.Close()

	log.Printf("Starting LSP server on port %d", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}

		go func() {
			defer conn.Close()
			if err := s.serve(ctx, conn, conn); err != nil {
				log.Printf("Error serving connection: %v", err)
			}
		}()
	}
}

// serve handles the LSP protocol over the given reader/writer
func (s *Server) serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	log.Printf("Creating connection for LSP communication")
	connection := NewConnection(reader, writer)
	
	log.Printf("Starting message loop")
	for {
		select {
		case <-ctx.Done():
			log.Printf("Context cancelled, shutting down")
			return ctx.Err()
		default:
		}

		log.Printf("Waiting for message...")
		message, err := connection.ReadMessage()
		if err != nil {
			if err == io.EOF {
				log.Printf("Connection closed (EOF)")
				return nil
			}
			log.Printf("Failed to read message: %v", err)
			return fmt.Errorf("failed to read message: %w", err)
		}

		log.Printf("Received message: method=%s, id=%v", message.Method, message.ID)

		response, err := s.handleMessage(ctx, message)
		if err != nil {
			log.Printf("Error handling message %s: %v", message.Method, err)
			continue
		}

		if response != nil {
			log.Printf("Sending response for method=%s", message.Method)
			if err := connection.WriteMessage(response); err != nil {
				log.Printf("Failed to write response: %v", err)
				return fmt.Errorf("failed to write response: %w", err)
			}
		} else {
			log.Printf("No response for method=%s", message.Method)
		}
	}
}

// handleMessage processes an LSP message and returns a response
func (s *Server) handleMessage(ctx context.Context, message *Message) (*Message, error) {
	switch message.Method {
	case "initialize":
		return s.handleInitialize(message)
	case "initialized":
		return s.handleInitialized(message)
	case "shutdown":
		return s.handleShutdown(message)
	case "exit":
		os.Exit(0)
		return nil, nil
	case "textDocument/didOpen":
		return s.handleTextDocumentDidOpen(message)
	case "textDocument/didChange":
		return s.handleTextDocumentDidChange(message)
	case "textDocument/didSave":
		return s.handleTextDocumentDidSave(message)
	case "textDocument/didClose":
		return s.handleTextDocumentDidClose(message)
	case "textDocument/hover":
		return s.handleTextDocumentHover(message)
	case "textDocument/definition":
		return s.handleTextDocumentDefinition(message)
	case "textDocument/references":
		return s.handleTextDocumentReferences(message)
	case "textDocument/codeAction":
		return s.handleTextDocumentCodeAction(message)
	case "textDocument/rename":
		return s.handleTextDocumentRename(message)
	default:
		log.Printf("Unhandled method: %s", message.Method)
		return nil, nil
	}
}

func (s *Server) handleInitialize(message *Message) (*Message, error) {
	log.Printf("Handling initialize request")
	var params InitializeParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		log.Printf("Failed to unmarshal initialize params: %v", err)
		return s.errorResponse(message.ID, -32602, "Invalid params", err)
	}

	log.Printf("Initialize params: RootURI=%s, RootPath=%s", params.RootURI, params.RootPath)

	s.mu.Lock()
	s.rootPath = params.RootURI
	if s.rootPath == "" {
		s.rootPath = params.RootPath
	}
	// Remove file:// prefix if present
	if len(s.rootPath) > 7 && s.rootPath[:7] == "file://" {
		s.rootPath = s.rootPath[7:]
	}
	log.Printf("Set root path to: %s", s.rootPath)
	s.mu.Unlock()

	result := InitializeResult{
		Capabilities: s.capabilities,
		ServerInfo: &ServerInfo{
			Name:    "gorefactor-lsp",
			Version: "0.1.0",
		},
	}

	log.Printf("Sending initialize response with capabilities: %+v", s.capabilities.CodeActionProvider)
	return s.successResponse(message.ID, result)
}

func (s *Server) handleInitialized(message *Message) (*Message, error) {
	log.Printf("Handling initialized notification")
	s.mu.Lock()
	defer s.mu.Unlock()

	// Initialize workspace
	if s.rootPath != "" {
		log.Printf("Loading workspace from: %s", s.rootPath)
		engine := refactor.CreateEngine()
		
		workspace, err := engine.LoadWorkspace(s.rootPath)
		if err != nil {
			log.Printf("Failed to load workspace: %v", err)
		} else {
			s.workspace = workspace
			s.engine = engine
			s.initialized = true
			log.Printf("Successfully initialized workspace at: %s", s.rootPath)
			log.Printf("Workspace packages: %d", len(workspace.Packages))
			for pkgPath := range workspace.Packages {
				log.Printf("  Package: %s", pkgPath)
			}
		}
	} else {
		log.Printf("No root path set, skipping workspace initialization")
	}

	return nil, nil
}

func (s *Server) handleShutdown(message *Message) (*Message, error) {
	s.mu.Lock()
	s.initialized = false
	s.workspace = nil
	s.engine = nil
	s.mu.Unlock()

	return s.successResponse(message.ID, nil)
}

func (s *Server) handleTextDocumentDidOpen(message *Message) (*Message, error) {
	var params DidOpenTextDocumentParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return nil, err
	}

	log.Printf("Document opened: %s", params.TextDocument.URI)
	// TODO: Update workspace with new document content
	return nil, nil
}

func (s *Server) handleTextDocumentDidChange(message *Message) (*Message, error) {
	var params DidChangeTextDocumentParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return nil, err
	}

	log.Printf("Document changed: %s", params.TextDocument.URI)
	// TODO: Update workspace with document changes
	return nil, nil
}

func (s *Server) handleTextDocumentDidSave(message *Message) (*Message, error) {
	var params DidSaveTextDocumentParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return nil, err
	}

	log.Printf("Document saved: %s", params.TextDocument.URI)
	// TODO: Refresh workspace analysis for saved document
	return nil, nil
}

func (s *Server) handleTextDocumentDidClose(message *Message) (*Message, error) {
	var params DidCloseTextDocumentParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return nil, err
	}

	log.Printf("Document closed: %s", params.TextDocument.URI)
	return nil, nil
}

func (s *Server) successResponse(id interface{}, result interface{}) (*Message, error) {
	response := &Message{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	return response, nil
}

func (s *Server) errorResponse(id interface{}, code int, message string, data interface{}) (*Message, error) {
	response := &Message{
		JSONRPC: "2.0",
		ID:      id,
		Error: &ResponseError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	return response, nil
}

// Helper function to convert URI to file path
func (s *Server) uriToPath(uri string) string {
	if len(uri) > 7 && uri[:7] == "file://" {
		return uri[7:]
	}
	return uri
}

// Helper function to convert file path to URI
func (s *Server) pathToURI(path string) string {
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}
	return "file://" + path
}