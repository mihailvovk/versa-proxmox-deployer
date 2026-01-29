// Versa HeadEnd Deployer - VM Console (Serial)

'use strict';

// Console state
let activeConsole = null; // { vmid, type, ws, terminal, fitAddon }
let consoleReconnectAttempts = 0;
const MAX_RECONNECT_ATTEMPTS = 3;

/**
 * Opens the console modal for a VM.
 */
function openConsole(vmid, vmName) {
    closeConsole(); // Close any existing console

    const modal = document.getElementById('console-modal');
    const titleEl = document.getElementById('console-title');
    titleEl.textContent = `${vmName} (VMID ${vmid})`;

    modal.dataset.vmid = vmid;
    modal.dataset.vmName = vmName;
    modal.classList.remove('hidden');

    const container = document.getElementById('console-terminal-container');
    container.innerHTML = '';
    consoleReconnectAttempts = 0;

    openSerialConsole(vmid, container);
}

/**
 * Opens a serial (xterm.js) console connection.
 */
function openSerialConsole(vmid, container) {
    const statusEl = document.getElementById('console-status');
    statusEl.textContent = 'Connecting...';
    statusEl.className = 'console-status connecting';

    // Create terminal
    const terminal = new Terminal({
        cursorBlink: true,
        fontSize: 14,
        fontFamily: "'SF Mono', 'Menlo', 'Monaco', 'Courier New', monospace",
        theme: {
            background: '#0f1117',
            foreground: '#e0e2ed',
            cursor: '#4f8fff',
            selectionBackground: 'rgba(79, 143, 255, 0.3)',
            black: '#1a1d27',
            red: '#f87171',
            green: '#34d399',
            yellow: '#fbbf24',
            blue: '#4f8fff',
            magenta: '#c084fc',
            cyan: '#22d3ee',
            white: '#e0e2ed',
            brightBlack: '#8b8fa3',
            brightRed: '#fca5a5',
            brightGreen: '#6ee7b7',
            brightYellow: '#fde68a',
            brightBlue: '#93c5fd',
            brightMagenta: '#d8b4fe',
            brightCyan: '#67e8f9',
            brightWhite: '#f8fafc',
        },
        scrollback: 5000,
        convertEol: true,
    });

    const fitAddon = new FitAddon.FitAddon();
    terminal.loadAddon(fitAddon);

    terminal.open(container);
    fitAddon.fit();

    activeConsole = {
        vmid: vmid,
        type: 'serial',
        terminal: terminal,
        fitAddon: fitAddon,
        ws: null,
    };

    // Connect WebSocket
    connectSerialWebSocket(vmid, terminal, fitAddon);

    // Handle terminal resize
    terminal.onResize(({ cols, rows }) => {
        if (activeConsole && activeConsole.ws && activeConsole.ws.readyState === WebSocket.OPEN) {
            activeConsole.ws.send(JSON.stringify({ type: 'resize', cols: cols, rows: rows }));
        }
    });

    // Handle window resize
    const resizeHandler = () => {
        if (activeConsole && activeConsole.fitAddon) {
            activeConsole.fitAddon.fit();
        }
    };
    window.addEventListener('resize', resizeHandler);
    activeConsole._resizeHandler = resizeHandler;

    // Handle keyboard input
    terminal.onData((data) => {
        if (activeConsole && activeConsole.ws && activeConsole.ws.readyState === WebSocket.OPEN) {
            activeConsole.ws.send(JSON.stringify({ type: 'data', data: data }));
        }
    });

    terminal.focus();
}

/**
 * Establishes the WebSocket connection for serial console.
 */
function connectSerialWebSocket(vmid, terminal, fitAddon) {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const dims = fitAddon.proposeDimensions();
    const cols = dims ? dims.cols : 80;
    const rows = dims ? dims.rows : 24;
    const url = `${proto}//${location.host}/api/console/serial?vmid=${vmid}&cols=${cols}&rows=${rows}`;

    const ws = new WebSocket(url);
    if (activeConsole) {
        activeConsole.ws = ws;
    }

    const statusEl = document.getElementById('console-status');

    ws.onopen = () => {
        statusEl.textContent = 'Connected';
        statusEl.className = 'console-status connected';
        consoleReconnectAttempts = 0;
        terminal.focus();
    };

    ws.onmessage = (event) => {
        try {
            const msg = JSON.parse(event.data);
            if (msg.type === 'data') {
                terminal.write(msg.data);
            } else if (msg.type === 'error') {
                terminal.write('\r\n\x1b[31m[Error: ' + msg.message + ']\x1b[0m\r\n');
            }
        } catch {
            // If it's not JSON, write raw data
            terminal.write(event.data);
        }
    };

    ws.onclose = (event) => {
        // Don't reconnect if intentionally closed
        if (activeConsole && activeConsole._intentionalClose) return;

        statusEl.textContent = 'Disconnected';
        statusEl.className = 'console-status disconnected';

        if (!event.wasClean && consoleReconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
            consoleReconnectAttempts++;
            const delay = Math.min(1000 * Math.pow(2, consoleReconnectAttempts - 1), 5000);
            statusEl.textContent = `Reconnecting (${consoleReconnectAttempts}/${MAX_RECONNECT_ATTEMPTS})...`;
            statusEl.className = 'console-status connecting';
            terminal.write(`\r\n\x1b[33m[Reconnecting in ${delay/1000}s...]\x1b[0m\r\n`);
            setTimeout(() => {
                if (activeConsole && activeConsole.vmid === vmid) {
                    connectSerialWebSocket(vmid, terminal, fitAddon);
                }
            }, delay);
        } else if (!event.wasClean) {
            terminal.write('\r\n\x1b[31m[Connection lost. Close and reopen the console to retry.]\x1b[0m\r\n');
        }
    };

    ws.onerror = () => {
        statusEl.textContent = 'Error';
        statusEl.className = 'console-status disconnected';
    };
}

/**
 * Closes the console modal and cleans up all resources.
 */
function closeConsole() {
    const modal = document.getElementById('console-modal');
    modal.classList.add('hidden');

    if (activeConsole) {
        activeConsole._intentionalClose = true;

        // Remove resize handler
        if (activeConsole._resizeHandler) {
            window.removeEventListener('resize', activeConsole._resizeHandler);
        }

        // Close WebSocket cleanly
        if (activeConsole.ws) {
            activeConsole.ws.close(1000, 'user closed');
            activeConsole.ws = null;
        }

        // Dispose terminal
        if (activeConsole.terminal) {
            activeConsole.terminal.dispose();
            activeConsole.terminal = null;
        }

        activeConsole = null;
    }

    consoleReconnectAttempts = 0;

    // Clear container
    const container = document.getElementById('console-terminal-container');
    if (container) container.innerHTML = '';
}

// Close console on Escape key
document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && activeConsole) {
        closeConsole();
    }
});
