package ssh

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client wraps an SSH connection with convenience methods
type Client struct {
	host      string
	user      string
	client    *ssh.Client
	config    *ssh.ClientConfig
	mu        sync.Mutex
	timeout   time.Duration
	stopKeep  chan struct{} // signal to stop keepalive goroutine
}

// ClientOptions configures the SSH client
type ClientOptions struct {
	Host           string
	User           string
	Password       string
	KeyPath        string
	KeyPassphrase  string
	Timeout        time.Duration
	HostKeyCheck   bool
}

// NewClient creates a new SSH client with the given options
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	if opts.User == "" {
		opts.User = "root"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}

	// Build authentication methods
	var authMethods []ssh.AuthMethod

	// Try key-based auth first if key path provided
	if opts.KeyPath != "" {
		keyAuth, err := KeyAuth(opts.KeyPath, opts.KeyPassphrase)
		if err != nil {
			return nil, fmt.Errorf("loading SSH key: %w", err)
		}
		authMethods = append(authMethods, keyAuth)
	}

	// Add password auth if provided
	if opts.Password != "" {
		authMethods = append(authMethods, ssh.Password(opts.Password))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication method provided (need password or SSH key)")
	}

	// Host key callback — TOFU (Trust On First Use)
	var hostKeyCallback ssh.HostKeyCallback
	if opts.HostKeyCheck {
		cb, err := tofuHostKeyCallback()
		if err != nil {
			return nil, fmt.Errorf("setting up host key verification: %w", err)
		}
		hostKeyCallback = cb
	} else {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	}

	config := &ssh.ClientConfig{
		User:            opts.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         opts.Timeout,
	}

	return &Client{
		host:    opts.Host,
		user:    opts.User,
		config:  config,
		timeout: opts.Timeout,
	}, nil
}

// Connect establishes the SSH connection and starts keepalive
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil {
		return nil // Already connected
	}

	client, err := c.dialSSH()
	if err != nil {
		return err
	}

	c.client = client
	c.startKeepalive()
	return nil
}

// dialSSH establishes the raw SSH connection (caller must hold no lock)
func (c *Client) dialSSH() (*ssh.Client, error) {
	host := c.host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "22")
	}

	client, err := ssh.Dial("tcp", host, c.config)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", c.host, err)
	}
	return client, nil
}

// startKeepalive sends periodic keepalive requests to prevent SSH timeout.
// Must be called with c.client already set. Stops when c.stopKeep is closed
// or when a keepalive fails (which triggers c.client = nil for auto-reconnect).
func (c *Client) startKeepalive() {
	// Stop any existing keepalive goroutine
	if c.stopKeep != nil {
		close(c.stopKeep)
	}
	c.stopKeep = make(chan struct{})
	stop := c.stopKeep

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				c.mu.Lock()
				client := c.client
				c.mu.Unlock()

				if client == nil {
					return
				}

				// SendRequest with wantReply=true acts as a ping
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil {
					// Connection is dead — mark as nil so next command auto-reconnects
					c.mu.Lock()
					c.client = nil
					c.mu.Unlock()
					return
				}
			}
		}
	}()
}

// Close closes the SSH connection and stops keepalive
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopKeep != nil {
		close(c.stopKeep)
		c.stopKeep = nil
	}

	if c.client != nil {
		err := c.client.Close()
		c.client = nil
		return err
	}
	return nil
}

// IsConnected returns true if the client is connected
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client != nil
}

// Reconnect closes and reopens the connection
func (c *Client) Reconnect() error {
	c.Close()
	return c.Connect()
}

// Host returns the target host
func (c *Client) Host() string {
	return c.host
}

// User returns the SSH user
func (c *Client) User() string {
	return c.user
}

// getClient returns the underlying SSH client, reconnecting if necessary
func (c *Client) getClient() (*ssh.Client, error) {
	c.mu.Lock()

	if c.client != nil {
		cl := c.client
		c.mu.Unlock()
		return cl, nil
	}
	c.mu.Unlock()

	// Reconnect outside the lock (dialSSH does I/O)
	client, err := c.dialSSH()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.client = client
	c.startKeepalive()
	c.mu.Unlock()

	return client, nil
}

// newSession creates a new SSH session
func (c *Client) newSession() (*ssh.Session, error) {
	client, err := c.getClient()
	if err != nil {
		return nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		// Connection might be stale, try reconnecting
		c.mu.Lock()
		c.client = nil
		c.mu.Unlock()

		client, err = c.getClient()
		if err != nil {
			return nil, err
		}

		session, err = client.NewSession()
		if err != nil {
			return nil, fmt.Errorf("creating session: %w", err)
		}
	}

	return session, nil
}

// knownHostsDir returns ~/.versa-deployer
func knownHostsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".versa-deployer")
}

// knownHostsPath returns the path to the known_hosts file.
func knownHostsPath() string {
	return filepath.Join(knownHostsDir(), "known_hosts")
}

// tofuHostKeyCallback returns a TOFU (Trust On First Use) host key callback.
// On first connection to a host, the key is accepted and written to the known_hosts file.
// On subsequent connections, the key is verified against the stored key.
// If the key has changed, an error is returned warning about a possible MITM attack.
func tofuHostKeyCallback() (ssh.HostKeyCallback, error) {
	khPath := knownHostsPath()

	// Ensure directory and file exist
	if err := os.MkdirAll(knownHostsDir(), 0700); err != nil {
		return nil, fmt.Errorf("creating known_hosts directory: %w", err)
	}

	// Create the file if it doesn't exist
	if _, err := os.Stat(khPath); os.IsNotExist(err) {
		f, err := os.OpenFile(khPath, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("creating known_hosts file: %w", err)
		}
		f.Close()
	}

	// Try to load existing known hosts
	existingCb, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("loading known_hosts: %w", err)
	}

	// Wrap in TOFU logic
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := existingCb(hostname, remote, key)
		if err == nil {
			// Known host, key matches
			return nil
		}

		// Check if this is a "key not found" error (unknown host) vs "key changed" (MITM)
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			// Unknown host — trust on first use, append to known_hosts
			f, appendErr := os.OpenFile(khPath, os.O_APPEND|os.O_WRONLY, 0600)
			if appendErr != nil {
				return fmt.Errorf("failed to save host key: %w", appendErr)
			}
			defer f.Close()

			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			if _, writeErr := fmt.Fprintln(f, line); writeErr != nil {
				return fmt.Errorf("failed to write host key: %w", writeErr)
			}

			return nil
		}

		// Key has changed — possible MITM
		return fmt.Errorf("WARNING: host key for %s has changed! This could indicate a MITM attack. "+
			"If you trust this host, remove the old entry from %s and reconnect. Original error: %w",
			hostname, khPath, err)
	}, nil
}
