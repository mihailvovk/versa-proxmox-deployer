// Versa HeadEnd Deployer - Deployment and SSE progress

'use strict';

// --- Step 6: Summary & Deploy ---
function updateSummary() {
    const enabled = state.components.filter(c => c.enabled);
    let totalVMs = 0, totalCPU = 0, totalRAM = 0, totalDisk = 0;

    enabled.forEach(c => {
        totalVMs += c.count;
        totalCPU += c.cpu * c.count;
        totalRAM += c.ram * c.count;
        totalDisk += c.disk * c.count;
    });

    const summaryEl = document.getElementById('deploy-summary');
    const compList = enabled.map(c => `${COMP_NAMES[c.type]} x${c.count}`).join(', ');

    summaryEl.innerHTML = `
        <div><strong>Components:</strong> ${esc(compList)}</div>
        <div style="margin-top:8px">
            <span class="summary-stat">${totalVMs} VMs</span>
            <span class="summary-stat">${totalCPU} vCPU</span>
            <span class="summary-stat">${totalRAM} GB RAM</span>
            <span class="summary-stat">${totalDisk} GB Disk</span>
        </div>
    `;
}

async function handleDeploy() {
    const btn = document.getElementById('deploy-btn');
    const progressEl = document.getElementById('deploy-progress');
    const resultEl = document.getElementById('deploy-result');

    btn.disabled = true;
    progressEl.classList.remove('hidden');
    resultEl.classList.add('hidden');

    const prefix = document.getElementById('deploy-prefix').value.trim() || 'versa';
    const storage = document.getElementById('deploy-storage').value;
    const isHA = state.mode === 'ha';

    // Build component configs
    const components = state.components.filter(c => c.enabled).map(c => ({
        Type: c.type,
        Count: c.count,
        CPU: c.cpu,
        RAMGB: c.ram,
        DiskGB: c.disk,
        Node: c.node,
        ISOPath: c.iso,
        Version: '',
    }));

    // Start SSE listener
    startSSE();

    try {
        const result = await api('POST', '/api/deploy', {
            prefix,
            haMode: isHA,
            components,
            storage,
            networks: buildNetworkPayload(),
        });

        if (!result.success && result.error) {
            showDeployResult(false, result.error);
            btn.disabled = false;
        }
    } catch (err) {
        showDeployResult(false, err.message);
        btn.disabled = false;
    }
}

async function checkDeployStatus() {
    try {
        const status = await api('GET', '/api/deploy/status');
        if (!status || !status.active) return;

        // Deployment is in progress — show the deploy section and reconnect SSE
        const progressEl = document.getElementById('deploy-progress');
        const logEl = document.getElementById('progress-log');
        const progressText = document.getElementById('progress-text');
        const btn = document.getElementById('deploy-btn');

        progressEl.classList.remove('hidden');
        btn.disabled = true;

        // Replay logs
        if (status.logs && status.logs.length > 0) {
            logEl.innerHTML = '';
            status.logs.forEach(msg => {
                const line = document.createElement('div');
                line.className = 'log-line';
                line.textContent = msg;
                logEl.appendChild(line);
            });
            logEl.scrollTop = logEl.scrollHeight;
        }

        // Show current stage
        if (status.stage) {
            const pct = status.progress.total > 0
                ? Math.round((status.progress.current / status.progress.total) * 100)
                : 0;
            progressText.textContent = `${status.stage} (${status.progress.current}/${status.progress.total})`;
            document.getElementById('progress-fill').style.width = pct + '%';
        }

        // Reconnect SSE to get live updates
        startSSE();
    } catch (e) {
        // No active deploy
    }
}

function startSSE() {
    if (state.sseSource) {
        state.sseSource.close();
    }

    const logEl = document.getElementById('progress-log');
    logEl.innerHTML = '';

    state.sseSource = new EventSource('/api/deploy/progress');

    state.sseSource.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            handleSSEMessage(data);
        } catch (e) { /* ignore */ }
    };

    state.sseSource.onerror = () => { /* SSE lost */ };
}

function handleSSEMessage(data) {
    const logEl = document.getElementById('progress-log');
    const progressFill = document.getElementById('progress-fill');
    const progressText = document.getElementById('progress-text');

    switch (data.type) {
        case 'log': {
            const line = document.createElement('div');
            line.className = 'log-line';
            line.textContent = data.message;
            logEl.appendChild(line);
            logEl.scrollTop = logEl.scrollHeight;
            break;
        }
        case 'progress': {
            const pct = data.total > 0 ? Math.round((data.current / data.total) * 100) : 0;
            progressFill.style.width = pct + '%';
            progressText.textContent = `${data.stage} (${data.current}/${data.total})`;
            break;
        }
        case 'complete':
            if (state.sseSource) state.sseSource.close();
            showDeployResult(true, null, data.result);
            document.getElementById('deploy-btn').disabled = false;
            break;

        case 'error':
            if (state.sseSource) state.sseSource.close();
            showDeployResult(false, data.message);
            document.getElementById('deploy-btn').disabled = false;
            break;
    }
}

function showDeployResult(success, error, result) {
    const el = document.getElementById('deploy-result');
    el.classList.remove('hidden', 'success', 'error');
    el.classList.add(success ? 'success' : 'error');

    if (success && result) {
        let html = '<strong>Deployment Complete</strong>';
        if (result.VMs && result.VMs.length > 0) {
            html += '<table><thead><tr><th>Name</th><th>VMID</th><th>Node</th><th>Status</th></tr></thead><tbody>';
            result.VMs.forEach(vm => {
                html += `<tr>
                    <td>${esc(vm.Name)}</td>
                    <td>${vm.VMID}</td>
                    <td>${esc(vm.Node)}</td>
                    <td class="tag-online">${esc(vm.Status)}</td>
                </tr>`;
            });
            html += '</tbody></table>';
        }
        if (result.Duration) {
            html += `<div style="margin-top:8px;color:var(--text-muted)">Duration: ${Math.round(result.Duration / 1e9)}s</div>`;
        }
        el.innerHTML = html;
    } else {
        el.innerHTML = `<strong>Deployment Failed</strong><p>${esc(error || 'Unknown error')}</p>`;
    }
}

// --- Network Modal ---
function showNetworkModal() {
    document.getElementById('create-network-modal').classList.remove('hidden');
    document.getElementById('create-network-error').classList.add('hidden');
}

function closeModal() {
    document.getElementById('create-network-modal').classList.add('hidden');
}

async function handleCreateNetwork(e) {
    e.preventDefault();
    const errEl = document.getElementById('create-network-error');
    errEl.classList.add('hidden');

    const name = document.getElementById('net-name').value.trim();
    const node = document.getElementById('net-node').value;
    const vlanAware = document.getElementById('net-vlan-aware').checked;
    const iface = document.getElementById('net-interface').value.trim();

    try {
        const result = await api('POST', '/api/create-network', {
            type: 'bridge',
            name,
            node,
            vlanAware,
            interface: iface,
        });

        if (!result.success) {
            throw new Error(result.error || 'Failed to create network');
        }

        closeModal();
        // Re-poll discovery to pick up the new bridge
        pollDiscovery();
    } catch (err) {
        errEl.textContent = err.message;
        errEl.classList.remove('hidden');
    }
}
