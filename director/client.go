package director

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client handles communication with Versa Director REST API
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// ClientConfig holds configuration for the Director client
type ClientConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	Insecure bool // Skip TLS verification
	Timeout  time.Duration
}

// NewClient creates a new Director API client
func NewClient(cfg ClientConfig) *Client {
	if cfg.Port == 0 {
		cfg.Port = 9182 // Default Director API port
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.Insecure,
		},
	}

	return &Client{
		baseURL:  fmt.Sprintf("https://%s:%d", cfg.Host, cfg.Port),
		username: cfg.Username,
		password: cfg.Password,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
	}
}

// doRequest performs an authenticated API request using HTTP Basic auth.
func (c *Client) doRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.username, c.password)

	return c.httpClient.Do(req)
}

// get performs a GET request and unmarshals the response
func (c *Client) get(path string, result interface{}) error {
	resp, err := c.doRequest("GET", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

// IsConnected checks if the client can reach the Director
func (c *Client) IsConnected() bool {
	resp, err := c.doRequest("GET", "/api/v1/system/status", nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// GetVersion returns the Director version
func (c *Client) GetVersion() (string, error) {
	var result struct {
		Version string `json:"version"`
	}

	if err := c.get("/api/v1/system/version", &result); err != nil {
		return "", err
	}

	return result.Version, nil
}

// Close closes the client connection. Basic auth is stateless, so there is no
// session to tear down.
func (c *Client) Close() error {
	return nil
}

// DirectorInfo holds information about the Director
type DirectorInfo struct {
	Hostname   string
	Version    string
	Status     string
	Uptime     string
	HAStatus   string
	HAPeer     string
}

// GetDirectorInfo returns information about the Director
func (c *Client) GetDirectorInfo() (*DirectorInfo, error) {
	var result struct {
		Hostname string `json:"hostname"`
		Version  string `json:"version"`
		Status   string `json:"status"`
		Uptime   int64  `json:"uptimeSeconds"`
		HA       struct {
			Enabled bool   `json:"enabled"`
			Role    string `json:"role"`
			Peer    string `json:"peerAddress"`
			State   string `json:"state"`
		} `json:"highAvailability"`
	}

	if err := c.get("/api/v1/system/info", &result); err != nil {
		// Try alternative endpoint
		if err2 := c.get("/vnms/system/status", &result); err2 != nil {
			return nil, fmt.Errorf("director info unavailable: primary endpoint: %v; fallback endpoint: %w", err, err2)
		}
	}

	info := &DirectorInfo{
		Hostname: result.Hostname,
		Version:  result.Version,
		Status:   result.Status,
		Uptime:   formatUptime(result.Uptime),
	}

	if result.HA.Enabled {
		info.HAStatus = result.HA.State
		info.HAPeer = result.HA.Peer
	}

	return info, nil
}

// formatUptime formats uptime in seconds to human-readable string
func formatUptime(seconds int64) string {
	if seconds <= 0 {
		return "unknown"
	}

	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
