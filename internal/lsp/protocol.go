package lsp

import "encoding/json"

// Message represents an LSP message
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

// ResponseError represents an LSP error response
type ResponseError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Initialize request/response types
type InitializeParams struct {
	ProcessID             *int                `json:"processId"`
	ClientInfo            *ClientInfo         `json:"clientInfo,omitempty"`
	Locale                string              `json:"locale,omitempty"`
	RootPath              string              `json:"rootPath,omitempty"`
	RootURI               string              `json:"rootUri"`
	InitializationOptions interface{}         `json:"initializationOptions,omitempty"`
	Capabilities          ClientCapabilities  `json:"capabilities"`
	Trace                 string              `json:"trace,omitempty"`
	WorkspaceFolders      []WorkspaceFolder   `json:"workspaceFolders,omitempty"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// Client capabilities
type ClientCapabilities struct {
	Workspace    *WorkspaceCapabilities    `json:"workspace,omitempty"`
	TextDocument *TextDocumentCapabilities `json:"textDocument,omitempty"`
	Window       *WindowCapabilities       `json:"window,omitempty"`
	General      *GeneralCapabilities      `json:"general,omitempty"`
}

type WorkspaceCapabilities struct {
	ApplyEdit              bool                    `json:"applyEdit,omitempty"`
	WorkspaceEdit          *WorkspaceEditCapability `json:"workspaceEdit,omitempty"`
	DidChangeConfiguration *DynamicRegistration     `json:"didChangeConfiguration,omitempty"`
	DidChangeWatchedFiles  *DynamicRegistration     `json:"didChangeWatchedFiles,omitempty"`
	Symbol                 *WorkspaceSymbolCapability `json:"symbol,omitempty"`
	ExecuteCommand         *DynamicRegistration     `json:"executeCommand,omitempty"`
}

type TextDocumentCapabilities struct {
	Synchronization    *TextDocumentSyncCapability  `json:"synchronization,omitempty"`
	Completion         *CompletionCapability        `json:"completion,omitempty"`
	Hover              *HoverCapability             `json:"hover,omitempty"`
	SignatureHelp      *SignatureHelpCapability     `json:"signatureHelp,omitempty"`
	Declaration        *DeclarationCapability       `json:"declaration,omitempty"`
	Definition         *DefinitionCapability        `json:"definition,omitempty"`
	TypeDefinition     *TypeDefinitionCapability    `json:"typeDefinition,omitempty"`
	Implementation     *ImplementationCapability    `json:"implementation,omitempty"`
	References         *ReferenceCapability         `json:"references,omitempty"`
	DocumentHighlight  *DocumentHighlightCapability `json:"documentHighlight,omitempty"`
	DocumentSymbol     *DocumentSymbolCapability    `json:"documentSymbol,omitempty"`
	CodeAction         *CodeActionCapability        `json:"codeAction,omitempty"`
	CodeLens           *CodeLensCapability          `json:"codeLens,omitempty"`
	DocumentLink       *DocumentLinkCapability      `json:"documentLink,omitempty"`
	ColorProvider      *DocumentColorCapability     `json:"colorProvider,omitempty"`
	Formatting         *DocumentFormattingCapability `json:"formatting,omitempty"`
	RangeFormatting    *DocumentRangeFormattingCapability `json:"rangeFormatting,omitempty"`
	OnTypeFormatting   *DocumentOnTypeFormattingCapability `json:"onTypeFormatting,omitempty"`
	Rename             *RenameCapability            `json:"rename,omitempty"`
	PublishDiagnostics *PublishDiagnosticsCapability `json:"publishDiagnostics,omitempty"`
	FoldingRange       *FoldingRangeCapability      `json:"foldingRange,omitempty"`
}

type WindowCapabilities struct {
	WorkDoneProgress bool `json:"workDoneProgress,omitempty"`
}

type GeneralCapabilities struct {
	RegularExpressions *RegularExpressionsCapability `json:"regularExpressions,omitempty"`
	Markdown           *MarkdownCapability           `json:"markdown,omitempty"`
}

// Server capabilities  
type ServerCapabilities struct {
	TextDocumentSync                 *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
	CompletionProvider               *CompletionOptions       `json:"completionProvider,omitempty"`
	HoverProvider                    bool                     `json:"hoverProvider,omitempty"`
	SignatureHelpProvider            *SignatureHelpOptions    `json:"signatureHelpProvider,omitempty"`
	DeclarationProvider              bool                     `json:"declarationProvider,omitempty"`
	DefinitionProvider               bool                     `json:"definitionProvider,omitempty"`
	TypeDefinitionProvider           bool                     `json:"typeDefinitionProvider,omitempty"`
	ImplementationProvider           bool                     `json:"implementationProvider,omitempty"`
	ReferencesProvider               bool                     `json:"referencesProvider,omitempty"`
	DocumentHighlightProvider        bool                     `json:"documentHighlightProvider,omitempty"`
	DocumentSymbolProvider           bool                     `json:"documentSymbolProvider,omitempty"`
	CodeActionProvider               *CodeActionOptions       `json:"codeActionProvider,omitempty"`
	CodeLensProvider                 *CodeLensOptions         `json:"codeLensProvider,omitempty"`
	DocumentLinkProvider             *DocumentLinkOptions     `json:"documentLinkProvider,omitempty"`
	ColorProvider                    bool                     `json:"colorProvider,omitempty"`
	DocumentFormattingProvider       bool                     `json:"documentFormattingProvider,omitempty"`
	DocumentRangeFormattingProvider  bool                     `json:"documentRangeFormattingProvider,omitempty"`
	DocumentOnTypeFormattingProvider *DocumentOnTypeFormattingOptions `json:"documentOnTypeFormattingProvider,omitempty"`
	RenameProvider                   bool                     `json:"renameProvider,omitempty"`
	FoldingRangeProvider             bool                     `json:"foldingRangeProvider,omitempty"`
	ExecuteCommandProvider           *ExecuteCommandOptions   `json:"executeCommandProvider,omitempty"`
	WorkspaceSymbolProvider          bool                     `json:"workspaceSymbolProvider,omitempty"`
	Workspace                        *WorkspaceServerCapabilities `json:"workspace,omitempty"`
}

// Text Document Sync
type TextDocumentSyncOptions struct {
	OpenClose bool                       `json:"openClose,omitempty"`
	Change    TextDocumentSyncKind       `json:"change,omitempty"`
	WillSave  bool                       `json:"willSave,omitempty"`
	WillSaveWaitUntil bool              `json:"willSaveWaitUntil,omitempty"`
	Save      *SaveOptions               `json:"save,omitempty"`
}

type TextDocumentSyncKind int

const (
	TextDocumentSyncKindNone        TextDocumentSyncKind = 0
	TextDocumentSyncKindFull        TextDocumentSyncKind = 1
	TextDocumentSyncKindIncremental TextDocumentSyncKind = 2
)

type SaveOptions struct {
	IncludeText bool `json:"includeText,omitempty"`
}

// Code Actions
type CodeActionOptions struct {
	CodeActionKinds []string `json:"codeActionKinds,omitempty"`
}

// Position and Range
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Text Document Items
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type VersionedTextDocumentIdentifier struct {
	TextDocumentIdentifier
	Version int `json:"version"`
}

type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Text Document Notification Types
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

type TextDocumentContentChangeEvent struct {
	Range       *Range `json:"range,omitempty"`
	RangeLength *int   `json:"rangeLength,omitempty"`
	Text        string `json:"text"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier   `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent  `json:"contentChanges"`
}

type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// Hover
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

const (
	MarkupKindPlainText = "plaintext"
	MarkupKindMarkdown  = "markdown"
)

// References
type ReferenceParams struct {
	TextDocumentPositionParams
	Context ReferenceContext `json:"context"`
}

type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// Code Actions
type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext      `json:"context"`
}

type CodeActionContext struct {
	Diagnostics []interface{} `json:"diagnostics"`
	Only        []string      `json:"only,omitempty"`
}

type CodeAction struct {
	Title       string                 `json:"title"`
	Kind        string                 `json:"kind,omitempty"`
	Diagnostics []interface{}          `json:"diagnostics,omitempty"`
	IsPreferred bool                   `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit         `json:"edit,omitempty"`
	Command     *Command               `json:"command,omitempty"`
	Data        interface{}            `json:"data,omitempty"`
}

type Command struct {
	Title     string        `json:"title"`
	Command   string        `json:"command"`
	Arguments []interface{} `json:"arguments,omitempty"`
}

type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []interface{}         `json:"documentChanges,omitempty"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// Rename
type RenameParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

// Workspace types
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type WorkspaceServerCapabilities struct {
	WorkspaceFolders *WorkspaceFoldersServerCapabilities `json:"workspaceFolders,omitempty"`
}

type WorkspaceFoldersServerCapabilities struct {
	Supported           bool   `json:"supported,omitempty"`
	ChangeNotifications string `json:"changeNotifications,omitempty"`
}

// Placeholder types for capabilities (simplified)
type DynamicRegistration struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type WorkspaceEditCapability struct {
	DocumentChanges bool `json:"documentChanges,omitempty"`
}

type WorkspaceSymbolCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type TextDocumentSyncCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type CompletionCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type HoverCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type SignatureHelpCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DeclarationCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DefinitionCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type TypeDefinitionCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type ImplementationCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type ReferenceCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DocumentHighlightCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DocumentSymbolCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type CodeActionCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type CodeLensCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DocumentLinkCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DocumentColorCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DocumentFormattingCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DocumentRangeFormattingCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type DocumentOnTypeFormattingCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type RenameCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type PublishDiagnosticsCapability struct {
	RelatedInformation bool `json:"relatedInformation,omitempty"`
}

type FoldingRangeCapability struct {
	DynamicRegistration bool `json:"dynamicRegistration,omitempty"`
}

type RegularExpressionsCapability struct {
	Engine  string `json:"engine"`
	Version string `json:"version,omitempty"`
}

type MarkdownCapability struct {
	Parser  string   `json:"parser"`
	Version string   `json:"version,omitempty"`
	AllowedTags []string `json:"allowedTags,omitempty"`
}

// Server-side option types (simplified)
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type SignatureHelpOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type CodeLensOptions struct {
	ResolveProvider bool `json:"resolveProvider,omitempty"`
}

type DocumentLinkOptions struct {
	ResolveProvider bool `json:"resolveProvider,omitempty"`
}

type DocumentOnTypeFormattingOptions struct {
	FirstTriggerCharacter string   `json:"firstTriggerCharacter"`
	MoreTriggerCharacter  []string `json:"moreTriggerCharacter,omitempty"`
}

type ExecuteCommandOptions struct {
	Commands []string `json:"commands"`
}