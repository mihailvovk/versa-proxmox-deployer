package ssh

import (
	"fmt"
	"io"
	"sync"

	gossh "golang.org/x/crypto/ssh"
)

// PTYSession wraps an SSH session with a pseudo-terminal for interactive commands.
type PTYSession struct {
	session   *gossh.Session
	stdin     io.WriteCloser
	stdout    io.Reader
	done      chan struct{}
	closeOnce sync.Once
}

// NewPTYSession creates a new PTY session on the given SSH client, running the specified command.
// The terminal is allocated with the given dimensions and xterm-256color TERM type.
func NewPTYSession(client *Client, command string, cols, rows int) (*PTYSession, error) {
	sshClient, err := client.getClient()
	if err != nil {
		return nil, fmt.Errorf("getting SSH client: %w", err)
	}

	session, err := sshClient.NewSession()
	if err != nil {
		return nil, fmt.Errorf("creating SSH session: %w", err)
	}

	// Request pseudo-terminal
	modes := gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: 14400,
		gossh.TTY_OP_OSPEED: 14400,
	}

	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		session.Close()
		return nil, fmt.Errorf("requesting PTY: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("getting stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("getting stdout pipe: %w", err)
	}

	// Also capture stderr into stdout for the terminal
	session.Stderr = session.Stdout

	if err := session.Start(command); err != nil {
		session.Close()
		return nil, fmt.Errorf("starting command %q: %w", command, err)
	}

	p := &PTYSession{
		session: session,
		stdin:   stdin,
		stdout:  stdout,
		done:    make(chan struct{}),
	}

	// Wait for session exit in background
	go func() {
		session.Wait()
		p.closeOnce.Do(func() {
			close(p.done)
		})
	}()

	return p, nil
}

// Read reads output from the PTY.
func (p *PTYSession) Read(buf []byte) (int, error) {
	return p.stdout.Read(buf)
}

// Write sends input to the PTY.
func (p *PTYSession) Write(data []byte) (int, error) {
	return p.stdin.Write(data)
}

// Resize changes the PTY window size.
func (p *PTYSession) Resize(cols, rows int) error {
	return p.session.WindowChange(rows, cols)
}

// Close terminates the PTY session.
func (p *PTYSession) Close() error {
	var err error
	p.closeOnce.Do(func() {
		close(p.done)
	})
	p.stdin.Close()
	err = p.session.Close()
	return err
}

// Done returns a channel that is closed when the SSH session exits.
func (p *PTYSession) Done() <-chan struct{} {
	return p.done
}
