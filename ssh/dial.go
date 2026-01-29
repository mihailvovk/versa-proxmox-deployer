package ssh

import (
	"fmt"
	"net"
)

// DialTCP creates a TCP connection through the SSH tunnel to the specified host and port
// on the remote side. This is used for VNC connections where the VNC port is only
// accessible on localhost of the Proxmox host.
func (c *Client) DialTCP(host string, port int) (net.Conn, error) {
	sshClient, err := c.getClient()
	if err != nil {
		return nil, fmt.Errorf("getting SSH client: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := sshClient.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing %s via SSH tunnel: %w", addr, err)
	}

	return conn, nil
}
