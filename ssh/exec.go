package ssh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ExecResult holds the result of a command execution
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes a command and returns the result
func (c *Client) Run(cmd string) (*ExecResult, error) {
	return c.RunWithTimeout(cmd, c.timeout)
}

// RunWithTimeout executes a command with a specific timeout
func (c *Client) RunWithTimeout(cmd string, timeout time.Duration) (*ExecResult, error) {
	session, err := c.newSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Run the command
	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmd)
	}()

	select {
	case err := <-done:
		result := &ExecResult{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
		}

		if err != nil {
			// Try to extract exit code
			if exitErr, ok := err.(*ExitError); ok {
				result.ExitCode = exitErr.ExitStatus()
			} else {
				result.ExitCode = 1
			}
		}

		return result, nil

	case <-ctx.Done():
		return nil, fmt.Errorf("command timed out after %v", timeout)
	}
}

// RunJSON executes a command and parses the JSON output
func (c *Client) RunJSON(cmd string, v interface{}) error {
	result, err := c.Run(cmd)
	if err != nil {
		return err
	}

	if result.ExitCode != 0 {
		return fmt.Errorf("command failed (exit %d): %s", result.ExitCode, result.Stderr)
	}

	if err := json.Unmarshal([]byte(result.Stdout), v); err != nil {
		return fmt.Errorf("parsing JSON output: %w (output: %s)", err, result.Stdout)
	}

	return nil
}

// RunLines executes a command and returns stdout as lines
func (c *Client) RunLines(cmd string) ([]string, error) {
	result, err := c.Run(cmd)
	if err != nil {
		return nil, err
	}

	if result.ExitCode != 0 {
		return nil, fmt.Errorf("command failed (exit %d): %s", result.ExitCode, result.Stderr)
	}

	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	// Filter empty lines
	var filtered []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, line)
		}
	}

	return filtered, nil
}

// RunQuiet executes a command and returns only error on failure
func (c *Client) RunQuiet(cmd string) error {
	result, err := c.Run(cmd)
	if err != nil {
		return err
	}

	if result.ExitCode != 0 {
		errMsg := strings.TrimSpace(result.Stderr)
		if errMsg == "" {
			errMsg = strings.TrimSpace(result.Stdout)
		}
		return fmt.Errorf("command failed (exit %d): %s", result.ExitCode, errMsg)
	}

	return nil
}

// Upload copies a local file to the remote host via SCP
func (c *Client) Upload(localPath, remotePath string, progress func(written, total int64)) error {
	session, err := c.newSession()
	if err != nil {
		return err
	}
	defer session.Close()

	// Open local file
	f, err := openFile(localPath)
	if err != nil {
		return fmt.Errorf("opening local file: %w", err)
	}
	defer f.Close()

	// Get file info
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("getting file info: %w", err)
	}

	// Set up SCP
	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()

		// Send file header
		fmt.Fprintf(w, "C0644 %d %s\n", fi.Size(), getBasename(remotePath))

		// Send file content with progress
		if progress != nil {
			pw := &progressWriter{w: w, total: fi.Size(), callback: progress}
			io.Copy(pw, f)
		} else {
			io.Copy(w, f)
		}

		// Send end marker
		fmt.Fprint(w, "\x00")
	}()

	// Run SCP receive
	output, err := session.CombinedOutput(fmt.Sprintf("scp -t %s", remotePath))
	if err != nil {
		return fmt.Errorf("SCP failed: %w (output: %s)", err, string(output))
	}

	return nil
}

// UploadBytes uploads bytes to a remote file
func (c *Client) UploadBytes(data []byte, remotePath string) error {
	session, err := c.newSession()
	if err != nil {
		return err
	}
	defer session.Close()

	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()

		fmt.Fprintf(w, "C0644 %d %s\n", len(data), getBasename(remotePath))
		w.Write(data)
		fmt.Fprint(w, "\x00")
	}()

	output, err := session.CombinedOutput(fmt.Sprintf("scp -t %s", remotePath))
	if err != nil {
		return fmt.Errorf("SCP failed: %w (output: %s)", err, string(output))
	}

	return nil
}

// Download copies a remote file to local path via SCP
func (c *Client) Download(remotePath, localPath string) error {
	session, err := c.newSession()
	if err != nil {
		return err
	}
	defer session.Close()

	var stdout bytes.Buffer
	session.Stdout = &stdout

	err = session.Run(fmt.Sprintf("cat %s", remotePath))
	if err != nil {
		return fmt.Errorf("reading remote file: %w", err)
	}

	return writeFile(localPath, stdout.Bytes())
}

// ExitError wraps SSH exit errors
type ExitError struct {
	exitStatus int
	msg        string
}

func (e *ExitError) Error() string {
	return e.msg
}

func (e *ExitError) ExitStatus() int {
	return e.exitStatus
}

// progressWriter wraps a writer with progress callback
type progressWriter struct {
	w        io.Writer
	total    int64
	written  int64
	callback func(written, total int64)
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.written += int64(n)
	if pw.callback != nil {
		pw.callback(pw.written, pw.total)
	}
	return n, err
}

// Helper functions that need os package (will be in separate file for testing)
var openFile = func(path string) (interface {
	io.Reader
	io.Closer
	Stat() (interface{ Size() int64 }, error)
}, error) {
	return nil, fmt.Errorf("openFile not implemented")
}

var writeFile = func(path string, data []byte) error {
	return fmt.Errorf("writeFile not implemented")
}

func getBasename(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
