package lsp

import (
	"encoding/json"
	"fmt"
	"go/token"
	"log"
	"path/filepath"

	"github.com/mamaar/gorefactor/pkg/analysis"
	"github.com/mamaar/gorefactor/pkg/refactor"
	"github.com/mamaar/gorefactor/pkg/types"
)

// handleTextDocumentHover provides hover information for symbols
func (s *Server) handleTextDocumentHover(message *Message) (*Message, error) {
	var params TextDocumentPositionParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return s.errorResponse(message.ID, -32602, "Invalid params", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return s.successResponse(message.ID, nil)
	}

	// Convert URI to file path
	filePath := s.uriToPath(params.TextDocument.URI)
	
	// Find symbol at position
	symbol, err := s.findSymbolAtPosition(filePath, params.Position)
	if err != nil {
		log.Printf("Error finding symbol at position: %v", err)
		return s.successResponse(message.ID, nil)
	}

	if symbol == nil {
		return s.successResponse(message.ID, nil)
	}

	// Create hover content
	content := s.createHoverContent(symbol)
	
	hover := &Hover{
		Contents: MarkupContent{
			Kind:  MarkupKindMarkdown,
			Value: content,
		},
	}

	return s.successResponse(message.ID, hover)
}

// handleTextDocumentDefinition provides go-to-definition functionality
func (s *Server) handleTextDocumentDefinition(message *Message) (*Message, error) {
	var params TextDocumentPositionParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return s.errorResponse(message.ID, -32602, "Invalid params", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return s.successResponse(message.ID, nil)
	}

	// Convert URI to file path
	filePath := s.uriToPath(params.TextDocument.URI)
	
	// Find symbol at position
	symbol, err := s.findSymbolAtPosition(filePath, params.Position)
	if err != nil {
		log.Printf("Error finding symbol at position: %v", err)
		return s.successResponse(message.ID, nil)
	}

	if symbol == nil {
		return s.successResponse(message.ID, nil)
	}

	// Convert symbol position to LSP location
	location := &Location{
		URI: s.pathToURI(symbol.File),
		Range: Range{
			Start: s.tokenPosToLSPPosition(symbol.Position),
			End:   s.tokenPosToLSPPosition(symbol.End),
		},
	}

	return s.successResponse(message.ID, location)
}

// handleTextDocumentReferences finds all references to a symbol
func (s *Server) handleTextDocumentReferences(message *Message) (*Message, error) {
	var params ReferenceParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return s.errorResponse(message.ID, -32602, "Invalid params", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return s.successResponse(message.ID, nil)
	}

	// Convert URI to file path
	filePath := s.uriToPath(params.TextDocument.URI)
	
	// Find symbol at position
	symbol, err := s.findSymbolAtPosition(filePath, params.Position)
	if err != nil {
		log.Printf("Error finding symbol at position: %v", err)
		return s.successResponse(message.ID, nil)
	}

	if symbol == nil {
		return s.successResponse(message.ID, []Location{})
	}

	// Find all references
	resolver := analysis.NewSymbolResolver(s.workspace)
	references, err := resolver.FindReferences(symbol)
	if err != nil {
		log.Printf("Error finding references: %v", err)
		return s.successResponse(message.ID, []Location{})
	}

	// Convert to LSP locations
	var locations []Location
	for _, ref := range references {
		// Include declaration if requested
		if params.Context.IncludeDeclaration || ref.Position != symbol.Position {
			locations = append(locations, Location{
				URI: s.pathToURI(ref.File),
				Range: Range{
					Start: s.tokenPosToLSPPosition(ref.Position),
					End:   s.tokenPosToLSPPosition(ref.Position + token.Pos(len(symbol.Name))),
				},
			})
		}
	}

	return s.successResponse(message.ID, locations)
}

// handleTextDocumentCodeAction provides refactoring code actions
func (s *Server) handleTextDocumentCodeAction(message *Message) (*Message, error) {
	log.Printf("Handling code action request")
	var params CodeActionParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		log.Printf("Failed to unmarshal code action params: %v", err)
		return s.errorResponse(message.ID, -32602, "Invalid params", err)
	}

	log.Printf("Code action params: URI=%s, Range=%+v", params.TextDocument.URI, params.Range)

	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		log.Printf("Server not initialized, returning empty actions")
		return s.successResponse(message.ID, []CodeAction{})
	}

	// Convert URI to file path
	filePath := s.uriToPath(params.TextDocument.URI)
	log.Printf("Converted URI to file path: %s", filePath)
	
	var actions []CodeAction

	// Generate refactoring actions based on context
	log.Printf("Generating extract actions...")
	extractActions := s.generateExtractActions(filePath, params.Range)
	log.Printf("Generated %d extract actions", len(extractActions))
	actions = append(actions, extractActions...)
	
	log.Printf("Generating inline actions...")
	inlineActions := s.generateInlineActions(filePath, params.Range)
	log.Printf("Generated %d inline actions", len(inlineActions))
	actions = append(actions, inlineActions...)
	
	log.Printf("Generating move actions...")
	moveActions := s.generateMoveActions(filePath, params.Range)
	log.Printf("Generated %d move actions", len(moveActions))
	actions = append(actions, moveActions...)
	
	log.Printf("Generating rename actions...")
	renameActions := s.generateRenameActions(filePath, params.Range)
	log.Printf("Generated %d rename actions", len(renameActions))
	actions = append(actions, renameActions...)

	log.Printf("Total generated %d code actions", len(actions))
	for i, action := range actions {
		log.Printf("  Action %d: %s (%s)", i, action.Title, action.Kind)
	}

	return s.successResponse(message.ID, actions)
}

// handleTextDocumentRename performs symbol renaming
func (s *Server) handleTextDocumentRename(message *Message) (*Message, error) {
	var params RenameParams
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return s.errorResponse(message.ID, -32602, "Invalid params", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.initialized {
		return s.successResponse(message.ID, nil)
	}

	// Convert URI to file path
	filePath := s.uriToPath(params.TextDocument.URI)
	
	// Find symbol at position
	symbol, err := s.findSymbolAtPosition(filePath, params.Position)
	if err != nil {
		log.Printf("Error finding symbol at position: %v", err)
		return s.errorResponse(message.ID, -32603, "Failed to find symbol", err)
	}

	if symbol == nil {
		return s.errorResponse(message.ID, -32603, "No symbol found at position", nil)
	}

	// Create rename operation
	operation := &refactor.RenameSymbolOperation{
		Request: types.RenameSymbolRequest{
			SymbolName: symbol.Name,
			NewName:    params.NewName,
			Scope:      types.WorkspaceScope,
		},
	}

	// Execute the operation
	plan, err := operation.Execute(s.workspace)
	if err != nil {
		return s.errorResponse(message.ID, -32603, "Rename failed", err.Error())
	}

	// Convert to workspace edit
	workspaceEdit := s.planToWorkspaceEdit(plan)

	return s.successResponse(message.ID, workspaceEdit)
}

// Helper methods

func (s *Server) findSymbolAtPosition(filePath string, position Position) (*types.Symbol, error) {
	// Find the package containing this file
	var targetPackage *types.Package
	for _, pkg := range s.workspace.Packages {
		if _, exists := pkg.Files[filePath]; exists {
			targetPackage = pkg
			break
		}
	}

	if targetPackage == nil {
		return nil, fmt.Errorf("file not found in workspace: %s", filePath)
	}

	// Convert LSP position to token position (simplified)
	tokenPos := s.lspPositionToTokenPos(filePath, position)
	
	// Find symbol at position using resolver
	_ = analysis.NewSymbolResolver(s.workspace)
	
	// Search through all symbols in the package
	if targetPackage.Symbols != nil {
		// Check functions
		for _, symbol := range targetPackage.Symbols.Functions {
			if symbol.File == filePath && s.positionInRange(tokenPos, symbol.Position, symbol.End) {
				return symbol, nil
			}
		}
		
		// Check types
		for _, symbol := range targetPackage.Symbols.Types {
			if symbol.File == filePath && s.positionInRange(tokenPos, symbol.Position, symbol.End) {
				return symbol, nil
			}
		}
		
		// Check variables
		for _, symbol := range targetPackage.Symbols.Variables {
			if symbol.File == filePath && s.positionInRange(tokenPos, symbol.Position, symbol.End) {
				return symbol, nil
			}
		}
		
		// Check constants
		for _, symbol := range targetPackage.Symbols.Constants {
			if symbol.File == filePath && s.positionInRange(tokenPos, symbol.Position, symbol.End) {
				return symbol, nil
			}
		}
		
		// Check methods
		for _, methods := range targetPackage.Symbols.Methods {
			for _, symbol := range methods {
				if symbol.File == filePath && s.positionInRange(tokenPos, symbol.Position, symbol.End) {
					return symbol, nil
				}
			}
		}
	}

	return nil, nil
}

func (s *Server) createHoverContent(symbol *types.Symbol) string {
	content := fmt.Sprintf("**%s**\n\n", symbol.Name)
	content += fmt.Sprintf("Kind: %s\n", s.symbolKindToString(symbol.Kind))
	content += fmt.Sprintf("Package: %s\n", symbol.Package)
	
	if symbol.Exported {
		content += "Exported: Yes\n"
	} else {
		content += "Exported: No\n"
	}
	
	content += fmt.Sprintf("File: %s:%d\n", filepath.Base(symbol.File), symbol.Line)
	
	// Add signature for functions
	if symbol.Kind == types.FunctionSymbol && symbol.Signature != "" {
		content += fmt.Sprintf("\n```go\n%s\n```", symbol.Signature)
	}
	
	return content
}

func (s *Server) symbolKindToString(kind types.SymbolKind) string {
	switch kind {
	case types.FunctionSymbol:
		return "Function"
	case types.MethodSymbol:
		return "Method"
	case types.TypeSymbol:
		return "Type"
	case types.VariableSymbol:
		return "Variable"
	case types.ConstantSymbol:
		return "Constant"
	case types.InterfaceSymbol:
		return "Interface"
	case types.StructFieldSymbol:
		return "Field"
	case types.PackageSymbol:
		return "Package"
	default:
		return "Unknown"
	}
}

func (s *Server) tokenPosToLSPPosition(pos token.Pos) Position {
	// This is a simplified conversion
	// In a real implementation, we would use the FileSet to get accurate line/column
	return Position{
		Line:      int(pos) / 100, // Simplified
		Character: int(pos) % 100, // Simplified
	}
}

func (s *Server) lspPositionToTokenPos(filePath string, position Position) token.Pos {
	// This is a simplified conversion
	// In a real implementation, we would use the FileSet to get accurate token position
	return token.Pos(position.Line*100 + position.Character)
}

func (s *Server) positionInRange(pos, start, end token.Pos) bool {
	return pos >= start && pos <= end
}

func (s *Server) planToWorkspaceEdit(plan *types.RefactoringPlan) *WorkspaceEdit {
	changes := make(map[string][]TextEdit)
	
	for _, change := range plan.Changes {
		uri := s.pathToURI(change.File)
		
		textEdit := TextEdit{
			Range: Range{
				Start: s.tokenPosToLSPPosition(token.Pos(change.Start)),
				End:   s.tokenPosToLSPPosition(token.Pos(change.End)),
			},
			NewText: change.NewText,
		}
		
		changes[uri] = append(changes[uri], textEdit)
	}
	
	return &WorkspaceEdit{
		Changes: changes,
	}
}