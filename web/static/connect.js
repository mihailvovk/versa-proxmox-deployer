// Versa HeadEnd Deployer - Connection handling

'use strict';

// --- Auto-reconnect on page load ---
async function tryAutoReconnect() {
    try {
        const connStatus = await api('GET', '/api/connection/status');
        if (connStatus.connected) {
            const saved = loadSavedState();
            state.connected = true;
            setConnectionStatus('connected', 'Connected: ' + connStatus.host);

            if (saved) {
                // Restore mode
                state.mode = saved.mode || 'standard';
                state.singleComponent = saved.singleComponent || 'director';

                // Restore prefix
                if (saved.prefix) {
                    document.getElementById('deploy-prefix').value = saved.prefix;
                }

                // Update mode selection UI
                document.querySelectorAll('.mode-option').forEach(o => o.classList.remove('selected'));
                const modeOpt = document.querySelector(`.mode-option[data-mode="${state.mode}"]`);
                if (modeOpt) {
                    modeOpt.classList.add('selected');
                    modeOpt.querySelector('input').checked = true;
                }
                document.getElementById('single-component-picker').classList.toggle('hidden', state.mode !== 'single');
            }

            // Show environment step and start parallel loading
            showStep('step-environment');
            loadDeployments();
            await pollDiscovery();

            if (saved) {
                // Re-apply saved components (merge onto discovered data)
                if (saved.components) {
                    state.components = saved.components;
                }
                if (saved.networkConfig) {
                    state.networkConfig = {
                        ...saved.networkConfig,
                        autoCreate: saved.networkConfig.autoCreate instanceof Set
                            ? saved.networkConfig.autoCreate
                            : new Set(saved.networkConfig.autoCreate || []),
                    };
                }

                // Re-render all sections with restored state
                renderComponentsTable();
                renderNetworkConfig();
                updateSummary();
            }

            // Show all steps
            showStep('step-mode');
            showStep('step-components');
            showStep('step-networks');
            showStep('step-deploy');

            // Check for active deployment
            checkDeployStatus();
        } else {
            // Server not connected — clear stale saved state
            clearSavedState();
            checkDeployStatus();
        }
    } catch (e) {
        // Server not reachable, show normal connect screen
        clearSavedState();
    }
}

// --- Step 1: Connect ---
async function handleConnect(e) {
    e.preventDefault();
    const btn = document.getElementById('connect-btn');
    const errEl = document.getElementById('connect-error');
    errEl.classList.add('hidden');

    btn.disabled = true;
    btn.textContent = 'Connecting...';
    setConnectionStatus('connecting', 'Connecting...');

    const host = document.getElementById('host').value.trim();
    const user = document.getElementById('user').value.trim() || 'root';
    const password = document.getElementById('password').value;
    const savePassword = document.getElementById('save-password').checked;

    try {
        const result = await api('POST', '/api/connect', {
            host, user, password, savePassword
        });

        if (!result.success) {
            throw new Error(result.error || 'Connection failed');
        }

        state.connected = true;
        setConnectionStatus('connected', `Connected: ${host}`);
        saveState();
        showStep('step-environment');

        // Fire discovery polling and deployments fetch in parallel
        pollDiscovery();
        loadDeployments();
    } catch (err) {
        errEl.textContent = err.message;
        errEl.classList.remove('hidden');
        setConnectionStatus('disconnected', 'Disconnected');
        clearSavedState();
    } finally {
        btn.disabled = false;
        btn.textContent = 'Connect';
    }
}

function setConnectionStatus(cls, text) {
    const el = document.getElementById('connection-status');
    el.className = 'status ' + cls;
    el.textContent = text;
}

// --- SSH Key Upload ---
async function handleKeyUpload(e) {
    const file = e.target.files[0];
    if (!file) return;

    const statusEl = document.getElementById('ssh-key-status');
    statusEl.textContent = 'Uploading...';
    statusEl.classList.remove('has-key');

    const formData = new FormData();
    formData.append('key', file);

    try {
        const resp = await fetch('/api/upload-key', { method: 'POST', body: formData });
        const result = await resp.json();

        if (!result.success) {
            throw new Error(result.error || 'Upload failed');
        }

        statusEl.textContent = file.name;
        statusEl.classList.add('has-key');
    } catch (err) {
        statusEl.textContent = 'Upload failed: ' + err.message;
        statusEl.classList.remove('has-key');
    }

    // Reset file input so the same file can be re-uploaded
    e.target.value = '';
}
