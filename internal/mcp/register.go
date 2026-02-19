package mcp

import mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

// RegisterAllTools wires every gorefactor tool into the MCP server.
func RegisterAllTools(s *mcpsdk.Server, state *MCPServer) {
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
}
