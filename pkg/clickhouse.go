package pkg

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ClickHouseClient represents a ClickHouse HTTP client
type ClickHouseClient struct {
	protocol   string
	host       string
	port       string
	username   string
	password   string
	httpClient *http.Client
}

// NewClickHouseClient creates a new ClickHouse client
func NewClickHouseClient(protocol, host, port, username, password string) *ClickHouseClient {
	return &ClickHouseClient{
		protocol:   protocol,
		host:       host,
		port:       port,
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

// ExecuteQuery executes a query against ClickHouse server
func (c *ClickHouseClient) ExecuteQuery(query string) (string, error) {
	// Build the URL with readonly setting to prevent write operations
	url := fmt.Sprintf("%s://%s:%s/?readonly=1", c.protocol, c.host, c.port)

	// Create the request
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(query))
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "text/plain")

	// Add Basic Authentication
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	// Execute the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error executing request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("non-OK status: %d, body: %s", resp.StatusCode, body)
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %w", err)
	}

	return string(body), nil
}

// GetConnectionInfo returns connection information for display
func (c *ClickHouseClient) GetConnectionInfo() string {
	return fmt.Sprintf("%s://%s:%s", c.protocol, c.host, c.port)
}
