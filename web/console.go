package web

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mihailvovk/versa-proxmox-deployer/ssh"
)

// ConsoleSession represents an active console connection to a VM.
type ConsoleSession struct {
	ID         string    `json:"id"`
	VMID       int       `json:"vmid"`
	Type       string    `json:"type"` // "serial"
	CreatedAt  time.Time `json:"createdAt"`
	LastActive time.Time `json:"-"`

	mu        sync.Mutex // guards LastActive and session state
	writeMu   sync.Mutex // serializes writes to wsConn (gorilla allows one writer)
	closeOnce sync.Once
	wsConn    *websocket.Conn
	pty       *ssh.PTYSession
	done      chan struct{}
}

// consoleWriteTimeout bounds every WebSocket write so a stalled/half-open client
// can never block a writer indefinitely (which previously wedged the reaper).
const consoleWriteTimeout = 10 * time.Second

// writeJSON writes a console message under writeMu with a write deadline. It does
// NOT touch sess.mu, so the reaper and close path are never blocked by a slow
// client.
func (sess *ConsoleSession) writeJSON(msg consoleMessage) error {
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	sess.wsConn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
	return sess.wsConn.WriteJSON(msg)
}

// writeClose sends a close frame under writeMu with a write deadline.
func (sess *ConsoleSession) writeClose(code int, text string) {
	sess.writeMu.Lock()
	defer sess.writeMu.Unlock()
	sess.wsConn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
	sess.wsConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, text))
}

var (
	consoleSessions sync.Map
	wsUpgrader = websocket.Upgrader{
		// Only accept same-origin WebSocket handshakes to prevent cross-site
		// WebSocket hijacking of the VM serial console. The browser sets Origin
		// to the page's origin (which equals r.Host here); non-browser clients
		// send no Origin and are rejected.
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return false
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			return strings.EqualFold(u.Host, r.Host)
		},
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
	}
)

// consoleMessage is the JSON message format for serial console WebSocket communication.
type consoleMessage struct {
	Type    string `json:"type"`              // "data", "resize", "error"
	Data    string `json:"data,omitempty"`
	Message string `json:"message,omitempty"` // for error type
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
}

// handleConsoleSerial handles WebSocket connections for serial (terminal) console access.
// Requires `serial0: socket` configured on the VM. If no serial device is found,
// sends a clear error message and closes the connection.
func (s *Server) handleConsoleSerial(w http.ResponseWriter, r *http.Request) {
	client := s.getClient()
	if client == nil {
		http.Error(w, "Not connected to Proxmox", http.StatusBadRequest)
		return
	}

	vmidStr := r.URL.Query().Get("vmid")
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil || vmid <= 0 {
		http.Error(w, "Invalid VMID", http.StatusBadRequest)
		return
	}

	// Parse initial terminal size
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	// Check if VM has a serial device configured
	checkResult, _ := client.Run(fmt.Sprintf("qm config %d 2>/dev/null | grep -q '^serial0:' && echo yes || echo no", vmid))
	hasSerial := checkResult != nil && strings.TrimSpace(checkResult.Stdout) == "yes"

	// Upgrade to WebSocket
	wsConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("console serial: websocket upgrade failed", "error", err, "vmid", vmid)
		return
	}
	// Bound all handshake-phase writes (before the ConsoleSession exists) so a
	// half-open client can't block this handler goroutine indefinitely. The pump
	// resets the deadline per write via writeJSON once the session is live.
	wsConn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))

	// If no serial device, send an error and close — don't fall back to qm monitor
	if !hasSerial {
		slog.Info("console serial: VM has no serial device", "vmid", vmid)
		wsConn.WriteJSON(consoleMessage{
			Type: "data",
			Data: "\r\n\x1b[31mThis VM does not have a serial console device configured.\x1b[0m\r\n\r\n" +
				"\x1b[33mTo use the serial console, add a serial port to the VM:\x1b[0m\r\n" +
				"  qm set " + vmidStr + " -serial0 socket\r\n",
		})
		wsConn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "no serial device"),
		)
		wsConn.Close()
		return
	}

	// Send initial status to the terminal
	wsConn.WriteJSON(consoleMessage{
		Type: "data",
		Data: fmt.Sprintf("Connecting to VM %d serial console...\r\n", vmid),
	})

	// Try connecting directly to the QEMU serial socket first (most reliable),
	// then fall back to qm terminal if the socket doesn't exist.
	// On PVE 8.x, qm terminal uses a termproxy wrapper that doesn't pipe
	// output through stdout, so direct socket connection is preferred.
	socketPath := fmt.Sprintf("/var/run/qemu-server/%d.serial0", vmid)
	command := fmt.Sprintf(
		"if [ -S '%s' ]; then socat -,raw,echo=0 UNIX-CONNECT:'%s'; "+
			"elif command -v miniterm >/dev/null 2>&1; then miniterm UNIX:'%s'; "+
			"else qm terminal %d; fi",
		socketPath, socketPath, socketPath, vmid,
	)

	// Create PTY session
	pty, err := ssh.NewPTYSession(client, command, cols, rows)
	if err != nil {
		slog.Error("console serial: PTY creation failed", "error", err, "vmid", vmid, "command", command)
		wsConn.WriteJSON(consoleMessage{
			Type: "data",
			Data: fmt.Sprintf("\r\n\x1b[31mFailed to open terminal: %v\x1b[0m\r\n", err),
		})
		wsConn.WriteJSON(consoleMessage{Type: "error", Message: fmt.Sprintf("Failed to open terminal: %v", err)})
		wsConn.Close()
		return
	}

	wsConn.WriteJSON(consoleMessage{
		Type: "data",
		Data: "Connected. Press Enter to activate console.\r\n",
	})

	sessionID := fmt.Sprintf("serial-%d-%d", vmid, time.Now().UnixNano())
	sess := &ConsoleSession{
		ID:         sessionID,
		VMID:       vmid,
		Type:       "serial",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		wsConn:     wsConn,
		pty:        pty,
		done:       make(chan struct{}),
	}

	consoleSessions.Store(sessionID, sess)
	slog.Info("console: session started", "session", sessionID, "vmid", vmid, "type", "serial")

	// PTY -> WebSocket (stdout)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				sess.mu.Lock()
				sess.LastActive = time.Now()
				sess.mu.Unlock()
				// Bounded write outside sess.mu so a stalled client can't wedge the reaper.
				if writeErr := sess.writeJSON(consoleMessage{Type: "data", Data: string(buf[:n])}); writeErr != nil {
					break
				}
			}
			if err != nil {
				if err != io.EOF {
					slog.Debug("console: PTY read error", "error", err, "session", sessionID)
				}
				break
			}
		}
		closeConsoleSession(sess)
	}()

	// WebSocket -> PTY (stdin)
	go func() {
		for {
			_, msgBytes, err := wsConn.ReadMessage()
			if err != nil {
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					slog.Debug("console: WebSocket read error", "error", err, "session", sessionID)
				}
				break
			}

			sess.mu.Lock()
			sess.LastActive = time.Now()
			sess.mu.Unlock()

			var msg consoleMessage
			if err := json.Unmarshal(msgBytes, &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "data":
				pty.Write([]byte(msg.Data))
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					pty.Resize(msg.Cols, msg.Rows)
				}
			}
		}
		closeConsoleSession(sess)
	}()

	// Wait for PTY session to end
	go func() {
		select {
		case <-pty.Done():
			closeConsoleSession(sess)
		case <-sess.done:
		}
	}()
}

// handleConsoleTest runs diagnostic checks for console connectivity.
// GET /api/console/test?vmid=123
func (s *Server) handleConsoleTest(w http.ResponseWriter, r *http.Request) {
	client := s.getClient()
	if client == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Not connected"})
		return
	}

	vmidStr := r.URL.Query().Get("vmid")
	vmid, err := strconv.Atoi(vmidStr)
	if err != nil || vmid <= 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Invalid VMID"})
		return
	}

	var checks []string
	addCheck := func(msg string) {
		checks = append(checks, msg)
		slog.Info("console test", "vmid", vmid, "check", msg)
	}

	// 1. Check VM exists and is running
	result, err := client.Run(fmt.Sprintf("qm status %d 2>&1", vmid))
	if err != nil || result == nil {
		addCheck(fmt.Sprintf("FAIL: cannot check VM status: %v", err))
	} else {
		addCheck(fmt.Sprintf("VM status: %s", strings.TrimSpace(result.Stdout)))
	}

	// 2. Check serial0 config
	result, err = client.Run(fmt.Sprintf("qm config %d 2>/dev/null | grep serial", vmid))
	if err != nil || result == nil || strings.TrimSpace(result.Stdout) == "" {
		addCheck("FAIL: no serial device in VM config")
	} else {
		addCheck(fmt.Sprintf("Serial config: %s", strings.TrimSpace(result.Stdout)))
	}

	// 3. Check if serial socket exists
	socketPath := fmt.Sprintf("/var/run/qemu-server/%d.serial0", vmid)
	result, err = client.Run(fmt.Sprintf("ls -la '%s' 2>&1", socketPath))
	if err != nil || result == nil || result.ExitCode != 0 {
		out := ""
		if result != nil {
			out = strings.TrimSpace(result.Stdout)
		}
		addCheck(fmt.Sprintf("FAIL: serial socket not found at %s: %s", socketPath, out))
	} else {
		addCheck(fmt.Sprintf("Serial socket: %s", strings.TrimSpace(result.Stdout)))
	}

	// 4. Check what sockets exist for this VM
	if result, _ = client.Run(fmt.Sprintf("ls -la /var/run/qemu-server/%d.* 2>&1", vmid)); result != nil {
		addCheck(fmt.Sprintf("VM sockets: %s", strings.TrimSpace(result.Stdout)))
	}

	// 5. Check if socat is available
	result, _ = client.Run("command -v socat 2>&1")
	if result != nil && result.ExitCode == 0 {
		addCheck(fmt.Sprintf("socat: %s", strings.TrimSpace(result.Stdout)))
	} else {
		addCheck("WARN: socat not found")
	}

	// 6. Check qm terminal availability
	if result, _ = client.Run("command -v qm 2>&1"); result != nil {
		addCheck(fmt.Sprintf("qm: %s", strings.TrimSpace(result.Stdout)))
	}

	// 7. Try a quick qm terminal test (1 second timeout)
	if result, _ = client.Run(fmt.Sprintf("timeout 2 qm terminal %d </dev/null 2>&1 || true", vmid)); result != nil {
		addCheck(fmt.Sprintf("qm terminal test (2s): exit=%d stdout=%q stderr=%q",
			result.ExitCode, strings.TrimSpace(result.Stdout), strings.TrimSpace(result.Stderr)))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"vmid":    vmid,
		"checks":  checks,
	})
}

// handleConsoleSessions returns a list of active console sessions.
func (s *Server) handleConsoleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var sessions []ConsoleSession
	consoleSessions.Range(func(key, value interface{}) bool {
		sess := value.(*ConsoleSession)
		sessions = append(sessions, ConsoleSession{
			ID:        sess.ID,
			VMID:      sess.VMID,
			Type:      sess.Type,
			CreatedAt: sess.CreatedAt,
		})
		return true
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		APIResponse
		Sessions []ConsoleSession `json:"sessions"`
	}{
		APIResponse: APIResponse{Success: true},
		Sessions:    sessions,
	})
}

// closeConsoleSession closes all resources associated with a console session.
// Idempotent via sync.Once.
func closeConsoleSession(sess *ConsoleSession) {
	sess.closeOnce.Do(func() {
		close(sess.done)

		duration := time.Since(sess.CreatedAt).Round(time.Second)
		slog.Info("console: session closed", "session", sess.ID, "vmid", sess.VMID, "type", sess.Type, "duration", duration)

		// Close WebSocket with a bounded close frame, then close the conn. The
		// write is guarded by writeMu (not sess.mu) with a deadline so a dead
		// client can't block teardown.
		if sess.wsConn != nil {
			sess.writeClose(websocket.CloseNormalClosure, "session ended")
			sess.wsConn.Close()
		}

		// Close PTY
		if sess.pty != nil {
			sess.pty.Close()
		}

		// Remove from session map
		consoleSessions.Delete(sess.ID)
	})
}

// startSessionReaper runs a background goroutine that closes idle console sessions.
// Sessions inactive for more than 30 minutes are terminated.
func (s *Server) startSessionReaper() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			now := time.Now()
			consoleSessions.Range(func(key, value interface{}) bool {
				sess := value.(*ConsoleSession)
				sess.mu.Lock()
				idle := now.Sub(sess.LastActive)
				sess.mu.Unlock()

				if idle > 30*time.Minute {
					slog.Info("console: reaping idle session", "session", sess.ID, "vmid", sess.VMID, "idle", idle.Round(time.Second))
					closeConsoleSession(sess)
				}
				return true
			})
		}
	}()
}

// closeAllConsoleSessions closes all active console sessions. Called during server shutdown.
func closeAllConsoleSessions() {
	consoleSessions.Range(func(key, value interface{}) bool {
		sess := value.(*ConsoleSession)
		closeConsoleSession(sess)
		return true
	})
}
