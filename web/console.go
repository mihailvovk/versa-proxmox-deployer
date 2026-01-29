package web

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

	mu        sync.Mutex
	closeOnce sync.Once
	wsConn    *websocket.Conn
	pty       *ssh.PTYSession
	done      chan struct{}
}

var (
	consoleSessions sync.Map
	wsUpgrader      = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
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
	if s.sshClient == nil {
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
	checkResult, _ := s.sshClient.Run(fmt.Sprintf("qm config %d 2>/dev/null | grep -q '^serial0:' && echo yes || echo no", vmid))
	hasSerial := checkResult != nil && strings.TrimSpace(checkResult.Stdout) == "yes"

	// Upgrade to WebSocket
	wsConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("console serial: websocket upgrade failed", "error", err, "vmid", vmid)
		return
	}

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

	command := fmt.Sprintf("qm terminal %d", vmid)

	// Create PTY session
	pty, err := ssh.NewPTYSession(s.sshClient, command, cols, rows)
	if err != nil {
		slog.Error("console serial: PTY creation failed", "error", err, "vmid", vmid, "command", command)
		wsConn.WriteJSON(consoleMessage{Type: "error", Message: fmt.Sprintf("Failed to open terminal: %v", err)})
		wsConn.Close()
		return
	}

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
				writeErr := wsConn.WriteJSON(consoleMessage{Type: "data", Data: string(buf[:n])})
				sess.mu.Unlock()
				if writeErr != nil {
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

		// Close WebSocket with close frame
		sess.mu.Lock()
		if sess.wsConn != nil {
			sess.wsConn.WriteMessage(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
			)
			sess.wsConn.Close()
		}
		sess.mu.Unlock()

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
