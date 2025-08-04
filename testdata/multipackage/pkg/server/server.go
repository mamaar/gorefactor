package server

import "testdata/multipackage/internal/common"

// Server represents an HTTP server
type Server struct {
	config common.Config
}

// New creates a new server instance
func New(cfg common.Config) *Server {
	return &Server{
		config: cfg,
	}
}

// Start starts the server
func (s *Server) Start() error {
	// Implementation would go here
	return nil
}

// Stop stops the server
func (s *Server) Stop() error {
	// Implementation would go here
	return nil
}

// GetConfig returns the server configuration
func (s *Server) GetConfig() common.Config {
	return s.config
}