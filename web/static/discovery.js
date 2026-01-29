// Versa HeadEnd Deployer - Discovery polling and rendering

'use strict';

// --- Step 2: Discovery ---
async function pollDiscovery() {
    const loadingEl = document.getElementById('discovery-loading');
    const errorEl = document.getElementById('discovery-error');
    const resultsEl = document.getElementById('discovery-results');

    loadingEl.classList.remove('hidden');
    errorEl.classList.add('hidden');
    resultsEl.classList.add('hidden');

    // Poll until discovery completes (nodes loaded)
    const maxAttempts = 30;
    let nodesLoaded = false;
    for (let i = 0; i < maxAttempts; i++) {
        await sleep(1000);
        try {
            const disc = await api('GET', '/api/discovery');
            if (!disc.connected) continue;

            if (disc.error) {
                loadingEl.classList.add('hidden');
                errorEl.textContent = disc.error;
                errorEl.classList.remove('hidden');
                return;
            }

            if (disc.nodes && disc.nodes.length > 0 && !nodesLoaded) {
                nodesLoaded = true;
                state.discovery = disc;
                loadingEl.classList.add('hidden');
                resultsEl.classList.remove('hidden');
                renderDiscovery(disc);
                showStep('step-mode');
                rebuildComponents();
                showStep('step-components');
                showStep('step-networks');
                showStep('step-deploy');
                updateSummary();
                renderSourcesList();
            }

            // Keep polling for images (they load asynchronously)
            if (nodesLoaded && disc.images && disc.images.length > 0 && !state.imagesLoaded) {
                state.imagesLoaded = true;
                state.discovery.images = disc.images;
                renderImagesStatus();
                renderComponentsTable(); // Re-render to populate ISO dropdowns
            }

            // If nodes loaded and we've waited enough for images, stop
            if (nodesLoaded && (state.imagesLoaded || i > 15)) {
                if (!state.imagesLoaded) {
                    renderImagesStatus(); // Show "no images found" state
                }
                return;
            }
        } catch (e) {
            // Retry
        }
    }

    if (!nodesLoaded) {
        loadingEl.classList.add('hidden');
        errorEl.textContent = 'Discovery timed out';
        errorEl.classList.remove('hidden');
    }
}

function renderDiscovery(disc) {
    // Version & cluster info
    document.getElementById('pve-version').textContent = `Proxmox VE ${disc.version}`;
    const clusterEl = document.getElementById('cluster-info');
    if (disc.isCluster) {
        clusterEl.textContent = `Cluster: ${disc.clusterName}`;
    } else {
        clusterEl.textContent = 'Standalone';
    }

    // Nodes table
    const nodesBody = document.querySelector('#nodes-table tbody');
    nodesBody.innerHTML = '';
    (disc.nodes || []).forEach(n => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${esc(n.Name)}</td>
            <td class="tag-${n.Status}">${n.Status}</td>
            <td>${n.CPUCores}C</td>
            <td>${n.RAMUsedGB}/${n.RAMGB}GB</td>
            <td>${n.RunningVMs}</td>`;
        nodesBody.appendChild(tr);
    });

    // Storage table
    const storBody = document.querySelector('#storage-table tbody');
    storBody.innerHTML = '';
    (disc.storage || []).forEach(s => {
        if (!s.Active) return;
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${esc(s.Name)}</td>
            <td>${esc(s.Type)}</td>
            <td>${s.AvailableGB}GB</td>
            <td>${s.TotalGB}GB</td>`;
        storBody.appendChild(tr);
    });

    // Networks table
    const netBody = document.querySelector('#networks-table tbody');
    netBody.innerHTML = '';
    (disc.networks || []).forEach(n => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td>${esc(n.Name)}</td>
            <td>${esc(n.Interface || '-')}</td>
            <td>${esc(n.CIDR || '-')}</td>
            <td class="${n.VLANAware ? 'tag-yes' : 'tag-no'}">${n.VLANAware ? 'Yes' : 'No'}</td>`;
        netBody.appendChild(tr);
    });

    // Populate storage dropdown, sorted by most free space first
    const storageSel = document.getElementById('deploy-storage');
    storageSel.innerHTML = '';
    const activeStorage = (disc.storage || []).filter(s => s.Active).sort((a, b) => b.AvailableGB - a.AvailableGB);
    activeStorage.forEach((s, i) => {
        const opt = document.createElement('option');
        opt.value = s.Name;
        opt.textContent = `${s.Name} (${s.AvailableGB}GB free)`;
        if (i === 0) opt.selected = true;
        storageSel.appendChild(opt);
    });

    // Populate node dropdown for network creation modal
    const nodeSel = document.getElementById('net-node');
    nodeSel.innerHTML = '';
    (disc.nodes || []).forEach(n => {
        const opt = document.createElement('option');
        opt.value = n.Name;
        opt.textContent = n.Name;
        nodeSel.appendChild(opt);
    });
}

function renderImagesStatus() {
    const statusEl = document.getElementById('images-status');
    const summaryEl = document.getElementById('images-summary');
    const images = state.discovery ? state.discovery.images : null;

    if (!images || images.length === 0) {
        statusEl.classList.remove('hidden');
        statusEl.classList.remove('loading');
        statusEl.textContent = 'No ISOs found. Add an image source above.';
        summaryEl.classList.add('hidden');
        return;
    }

    statusEl.classList.add('hidden');
    summaryEl.classList.remove('hidden');

    // Group by component
    const grouped = {};
    images.forEach(iso => {
        const comp = iso.Component || 'unknown';
        if (!grouped[comp]) grouped[comp] = [];
        grouped[comp].push(iso);
    });

    // Sort each group by version descending
    for (const comp of Object.keys(grouped)) {
        grouped[comp].sort((a, b) => (b.Version || '').localeCompare(a.Version || ''));
    }

    let html = `<div class="images-header"><strong>${images.length} ISOs found</strong></div>`;
    html += '<div class="images-table-wrap">';
    html += '<table class="images-table"><thead><tr><th>Component</th><th>Version</th><th>Size</th><th>Source</th><th>MD5</th></tr></thead><tbody>';

    const compOrder = ['director', 'analytics', 'controller', 'flexvnf', 'concerto', 'router'];
    const sortedKeys = Object.keys(grouped).sort((a, b) => {
        const ia = compOrder.indexOf(a), ib = compOrder.indexOf(b);
        return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib);
    });

    for (const comp of sortedKeys) {
        const isos = grouped[comp];
        const name = COMP_NAMES[comp] || comp;
        isos.forEach((iso, i) => {
            const size = iso.Size > 0 ? formatSize(iso.Size) : '-';
            const md5 = iso.HasMD5File ? '<span class="tag-yes">yes</span>' : '<span class="tag-no">no</span>';
            html += `<tr>`;
            html += i === 0
                ? `<td class="iso-comp-cell" rowspan="${isos.length}">${esc(name)} <span class="iso-comp-count">(${isos.length})</span></td>`
                : '';
            html += `<td>${esc(iso.Version || iso.Filename)}</td>`;
            html += `<td>${size}</td>`;
            html += `<td>${esc(iso.SourceName || '-')}</td>`;
            html += `<td>${md5}</td>`;
            html += `</tr>`;
        });
    }

    html += '</tbody></table></div>';
    summaryEl.innerHTML = html;
}

function formatSize(bytes) {
    if (bytes >= 1073741824) return (bytes / 1073741824).toFixed(1) + ' GB';
    if (bytes >= 1048576) return (bytes / 1048576).toFixed(0) + ' MB';
    if (bytes >= 1024) return (bytes / 1024).toFixed(0) + ' KB';
    return bytes + ' B';
}

async function pollForImages() {
    for (let i = 0; i < 60; i++) {
        await sleep(2000);
        try {
            const disc = await api('GET', '/api/discovery');
            if (disc.images && disc.images.length > 0) {
                if (state.discovery) state.discovery.images = disc.images;
                state.imagesLoaded = true;
                renderImagesStatus();
                renderComponentsTable();
                return;
            }
        } catch (e) { /* retry */ }
    }
    // Timed out
    renderImagesStatus();
}
