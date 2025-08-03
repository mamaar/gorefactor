package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
)

// Connection handles LSP message reading and writing
type Connection struct {
	reader *bufio.Reader
	writer io.Writer
}

// NewConnection creates a new LSP connection
func NewConnection(reader io.Reader, writer io.Writer) *Connection {
	return &Connection{
		reader: bufio.NewReader(reader),
		writer: writer,
	}
}

// ReadMessage reads an LSP message from the connection
func (c *Connection) ReadMessage() (*Message, error) {
	log.Printf("Reading LSP message...")
	
	// Read headers
	headers := make(map[string]string)
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			log.Printf("Error reading header line: %v", err)
			return nil, err
		}
		
		line = strings.TrimSpace(line)
		if line == "" {
			break // End of headers
		}
		
		log.Printf("Header: %s", line)
		
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		headers[key] = value
	}
	
	// Get content length
	contentLengthStr, exists := headers["Content-Length"]
	if !exists {
		log.Printf("Missing Content-Length header")
		return nil, fmt.Errorf("missing Content-Length header")
	}
	
	contentLength, err := strconv.Atoi(contentLengthStr)
	if err != nil {
		log.Printf("Invalid Content-Length: %v", err)
		return nil, fmt.Errorf("invalid Content-Length: %w", err)
	}
	
	log.Printf("Content-Length: %d", contentLength)
	
	// Read content
	content := make([]byte, contentLength)
	_, err = io.ReadFull(c.reader, content)
	if err != nil {
		log.Printf("Failed to read message content: %v", err)
		return nil, fmt.Errorf("failed to read message content: %w", err)
	}
	
	log.Printf("Message content: %s", string(content))
	
	// Parse JSON
	var message Message
	if err := json.Unmarshal(content, &message); err != nil {
		log.Printf("Failed to parse JSON: %v", err)
		return nil, fmt.Errorf("failed to parse JSON message: %w", err)
	}
	
	log.Printf("Parsed message: method=%s, id=%v", message.Method, message.ID)
	return &message, nil
}

// WriteMessage writes an LSP message to the connection
func (c *Connection) WriteMessage(message *Message) error {
	log.Printf("Writing LSP message: method=%s, id=%v", message.Method, message.ID)
	
	// Marshal to JSON
	content, err := json.Marshal(message)
	if err != nil {
		log.Printf("Failed to marshal message: %v", err)
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	
	log.Printf("Message content: %s", string(content))
	
	// Write headers
	headers := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(content))
	log.Printf("Writing headers: %s", strings.ReplaceAll(headers, "\r\n", "\\r\\n"))
	
	if _, err := c.writer.Write([]byte(headers)); err != nil {
		log.Printf("Failed to write headers: %v", err)
		return fmt.Errorf("failed to write headers: %w", err)
	}
	
	// Write content
	if _, err := c.writer.Write(content); err != nil {
		log.Printf("Failed to write content: %v", err)
		return fmt.Errorf("failed to write content: %w", err)
	}
	
	log.Printf("Successfully wrote message")
	return nil
}