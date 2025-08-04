package client

import (
	"fmt"
	"testdata/multipackage/internal/common"
)

// Client represents an HTTP client
type Client struct {
	host string
	port int
}

// New creates a new client instance
func New(host string, port int) *Client {
	return &Client{
		host: host,
		port: port,
	}
}

// Connect connects to the server
func (c *Client) Connect() error {
	// Implementation would go here
	return nil
}

// GetURL returns the client URL
func (c *Client) GetURL() string {
	return fmt.Sprintf("http://%s:%d", c.host, c.port)
}

// UseConfig configures the client with a Config object
func (c *Client) UseConfig(cfg common.Config) {
	c.host = cfg.Host
	c.port = cfg.Port
}