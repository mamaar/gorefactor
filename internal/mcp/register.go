package mcp

import mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

// RegisterAllTools wires every gorefactor tool into the MCP server.
func RegisterAllTools(s *mcpsdk.Server, state *MCPServer) {
	// Recover from panics in any tool handler so a single bad refactoring
	// operation returns an error to the caller instead of crashing the
	// server process and disconnecting every client session. See
	// recoveryMiddleware's doc comment for the incident this covers.
	s.AddReceivingMiddleware(recoveryMiddleware(state.logger))
	s.AddReceivingMiddleware(refreshMiddleware(state))

	registerWorkspaceTools(s, state)
	registerMoveTools(s, state)
	registerRenameTools(s, state)
	registerExtractTools(s, state)
	registerInlineTools(s, state)
	registerAnalysisTools(s, state)
	registerImportTools(s, state)
	registerFacadeTools(s, state)
	registerDependencyTools(s, state)
	registerBatchTools(s, state)
	registerChangeSignatureTools(s, state)
	registerContextTools(s, state)
	registerDeleteTools(s, state)
	registerFixTools(s, state)
	registerTouchTools(s, state)
}
