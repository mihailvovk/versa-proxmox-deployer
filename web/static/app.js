// Versa HeadEnd Deployer - Web UI

'use strict';

// --- State ---
const state = {
    connected: false,
    discovery: null,
    mode: 'standard',    // standard | ha | single
    singleComponent: 'director',
    components: [],      // built from mode + discovery
    sseSource: null,
    imagesLoaded: false,
    configSources: [],   // configured ImageSource entries
    networkConfig: {
        northbound: '',
        directorRouter: '',
        controllerRouter: '',
        controllerWANs: [],
        extraInterfaces: {},     // compType -> [{label, bridge}]
        autoCreate: new Set(),
    },
};

// Interface definitions per component type
// Each entry: [ethIndex, label, configField, required]
// configField maps to state.networkConfig keys
const INTERFACE_DEFS = {
    director: [
        { eth: 0, label: 'Management',  field: 'northbound',     required: true },
        { eth: 1, label: 'Southbound',  field: 'directorRouter', required: true },
    ],
    analytics: [
        { eth: 0, label: 'Management',     field: 'northbound',        required: true },
        { eth: 1, label: 'Southbound',     field: 'directorRouter',    required: true },
        // Extra interfaces added dynamically via analyticsCluster
    ],
    controller: [
        { eth: 0, label: 'Management', field: 'northbound',        required: true },
        { eth: 1, label: 'To Router',  field: 'controllerRouter',  required: true },
        // WAN interfaces added dynamically from controllerWANs
    ],
    router: [
        { eth: 0, label: 'Management',    field: 'northbound',        required: true },
        { eth: 1, label: 'To Director',   field: 'directorRouter',    required: true },
        { eth: 2, label: 'To Controller', field: 'controllerRouter',  required: true },
        // Extra interfaces added dynamically via routerHA
    ],
    concerto: [
        { eth: 0, label: 'Management', field: 'northbound',     required: true },
        { eth: 1, label: 'Southbound', field: 'directorRouter', required: true },
    ],
    flexvnf: [
        { eth: 0, label: 'Management', field: 'northbound',  required: true },
        // WAN/LAN/extra added dynamically from flexvnfInterfaces
    ],
};

// Default VM specs per component
const DEFAULT_SPECS = {
    director:   { cpu: 8,  ram: 16, disk: 100 },
    analytics:  { cpu: 4,  ram: 8,  disk: 200 },
    controller: { cpu: 4,  ram: 8,  disk: 80  },
    router:     { cpu: 4,  ram: 4,  disk: 80  },
    concerto:   { cpu: 4,  ram: 8,  disk: 50  },
    flexvnf:    { cpu: 4,  ram: 4,  disk: 20  },
};

// Component display names
const COMP_NAMES = {
    director: 'Director', analytics: 'Analytics', controller: 'Controller',
    router: 'Router', concerto: 'Concerto', flexvnf: 'FlexVNF',
};

// Standard HE components
const STANDARD_COMPONENTS = ['director', 'analytics', 'controller', 'router', 'concerto'];

// --- Init ---
document.addEventListener('DOMContentLoaded', async () => {
    setupEventListeners();
    generatePrefix();
    await loadConfig();
    checkDeployStatus();
});

function generatePrefix() {
    const bytes = new Uint8Array(4);
    crypto.getRandomValues(bytes);
    const hex = Array.from(bytes).map(b => b.toString(16).padStart(2, '0')).join('');
    document.getElementById('deploy-prefix').value = 'v-' + hex;
}

function setupEventListeners() {
    document.getElementById('connect-form').addEventListener('submit', handleConnect);
    document.getElementById('create-network-btn').addEventListener('click', () => showNetworkModal());
    document.getElementById('create-network-form').addEventListener('submit', handleCreateNetwork);
    document.getElementById('deploy-btn').addEventListener('click', handleDeploy);
    document.getElementById('add-source-btn').addEventListener('click', () => showSourceModal());
    document.getElementById('add-local-btn').addEventListener('click', () => showSourceModal('local'));
    document.getElementById('add-source-form').addEventListener('submit', handleAddSource);
    document.getElementById('rescan-sources-btn').addEventListener('click', handleRescanSources);

    // SSH key file upload
    document.getElementById('ssh-key-file').addEventListener('change', handleKeyUpload);

    // Deployments refresh
    document.getElementById('refresh-deployments-btn').addEventListener('click', loadDeployments);

    // Auto-detect source type as user types
    document.getElementById('source-url').addEventListener('input', (e) => {
        const url = e.target.value.trim();
        const typeEl = document.getElementById('source-detected-type');
        const displayEl = document.getElementById('source-type-display');
        if (url.length > 5) {
            let type = 'http';
            if (url.includes('dropbox.com')) type = 'dropbox';
            else if (url.startsWith('sftp://')) type = 'sftp';
            else if (url.startsWith('/') || url.startsWith('~')) type = 'local';
            typeEl.textContent = `Detected type: ${type}`;
            displayEl.classList.remove('hidden');
        } else {
            displayEl.classList.add('hidden');
        }
    });

    // Mode selector
    document.querySelectorAll('.mode-option').forEach(opt => {
        opt.addEventListener('click', () => {
            document.querySelectorAll('.mode-option').forEach(o => o.classList.remove('selected'));
            opt.classList.add('selected');
            opt.querySelector('input').checked = true;
            state.mode = opt.dataset.mode;
            document.getElementById('single-component-picker').classList.toggle('hidden', state.mode !== 'single');
            rebuildComponents();
        });
    });

    document.querySelectorAll('input[name="single-comp"]').forEach(radio => {
        radio.addEventListener('change', (e) => {
            state.singleComponent = e.target.value;
            rebuildComponents();
        });
    });
}

// --- API helpers ---
async function api(method, path, body) {
    const opts = { method, headers: { 'Content-Type': 'application/json' } };
    if (body) opts.body = JSON.stringify(body);
    const resp = await fetch(path, opts);
    return resp.json();
}

// --- Load saved config ---
async function loadConfig() {
    try {
        const cfg = await api('GET', '/api/config');
        if (cfg.lastProxmoxHost) document.getElementById('host').value = cfg.lastProxmoxHost;
        if (cfg.lastProxmoxUser) document.getElementById('user').value = cfg.lastProxmoxUser;
        if (cfg.imageSources) state.configSources = cfg.imageSources;

        // Show saved password status
        if (cfg.hasPassword) {
            const pwLabel = document.querySelector('label[for="password"]');
            pwLabel.innerHTML = 'Password <span class="password-saved-tag">Saved</span>';
            document.getElementById('password').placeholder = 'Leave empty to use saved';
        }

        // Show saved SSH key status
        if (cfg.lastSSHKeyPath) {
            const statusEl = document.getElementById('ssh-key-status');
            const keyName = cfg.lastSSHKeyPath.split('/').pop();
            statusEl.textContent = keyName;
            statusEl.classList.add('has-key');
        }
    } catch (e) {
        // Config not available
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
        showStep('step-environment');
        pollDiscovery();
    } catch (err) {
        errEl.textContent = err.message;
        errEl.classList.remove('hidden');
        setConnectionStatus('disconnected', 'Disconnected');
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
                loadDeployments();
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

// --- Sources Management ---
async function renderSourcesList() {
    // Fetch current sources from backend
    try {
        const result = await api('GET', '/api/sources');
        if (result.success && result.sources) {
            state.configSources = result.sources;
        }
    } catch (e) {
        // Use local state
    }

    const container = document.getElementById('sources-list');
    const srcs = state.configSources;

    if (!srcs || srcs.length === 0) {
        container.innerHTML = '<div class="source-item"><span class="source-url">No sources configured — add a source above</span></div>';
        return;
    }

    container.innerHTML = '';
    srcs.forEach(src => {
        const item = document.createElement('div');
        item.className = 'source-item';
        item.innerHTML = `
            <span class="source-type">${esc(src.Type || 'auto')}</span>
            <span class="source-name">${esc(src.Name || '')}</span>
            <span class="source-url" title="${esc(src.URL)}">${esc(src.URL)}</span>
            <button class="btn-remove" data-url="${esc(src.URL)}">Remove</button>
        `;
        container.appendChild(item);
    });

    // Bind remove buttons
    container.querySelectorAll('.btn-remove').forEach(btn => {
        btn.addEventListener('click', async (e) => {
            const url = e.target.dataset.url;
            await api('DELETE', '/api/sources', { url });
            renderSourcesList();
        });
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

function showSourceModal(mode) {
    document.getElementById('add-source-modal').classList.remove('hidden');
    document.getElementById('add-source-error').classList.add('hidden');
    document.getElementById('source-url').value = '';
    document.getElementById('source-name').value = '';
    const urlInput = document.getElementById('source-url');
    const titleEl = document.querySelector('#add-source-modal h3');
    const typeDisplay = document.getElementById('source-type-display');
    const detectedType = document.getElementById('source-detected-type');
    if (mode === 'local') {
        titleEl.textContent = 'Add Local Folder';
        urlInput.placeholder = '/path/to/iso/folder';
        detectedType.textContent = 'Detected type: local';
        typeDisplay.classList.remove('hidden');
    } else {
        titleEl.textContent = 'Add Image Source';
        urlInput.placeholder = 'https://dropbox.com/... or sftp://user@host/path';
        typeDisplay.classList.add('hidden');
    }
}

function closeSourceModal() {
    document.getElementById('add-source-modal').classList.add('hidden');
}

async function handleAddSource(e) {
    e.preventDefault();
    const errEl = document.getElementById('add-source-error');
    const btn = document.getElementById('add-source-submit-btn');
    errEl.classList.add('hidden');

    const url = document.getElementById('source-url').value.trim();
    const name = document.getElementById('source-name').value.trim();

    if (!url) return;

    btn.disabled = true;
    btn.textContent = 'Testing...';

    try {
        const result = await api('POST', '/api/sources', { url, name });
        if (!result.success) {
            throw new Error(result.error || 'Failed to add source');
        }

        if (result.sources) state.configSources = result.sources;
        closeSourceModal();
        renderSourcesList();

        // Show scanning state
        state.imagesLoaded = false;
        document.getElementById('images-status').classList.remove('hidden');
        document.getElementById('images-status').className = 'loading';
        document.getElementById('images-status').textContent = 'Scanning image sources...';
        document.getElementById('images-summary').classList.add('hidden');

        // Poll for updated images
        pollForImages();
    } catch (err) {
        errEl.textContent = err.message;
        errEl.classList.remove('hidden');
    } finally {
        btn.disabled = false;
        btn.textContent = 'Add & Test';
    }
}

async function handleRescanSources() {
    const btn = document.getElementById('rescan-sources-btn');
    btn.disabled = true;
    btn.textContent = 'Scanning...';

    state.imagesLoaded = false;
    document.getElementById('images-status').classList.remove('hidden');
    document.getElementById('images-status').className = 'loading';
    document.getElementById('images-status').textContent = 'Scanning image sources...';
    document.getElementById('images-summary').classList.add('hidden');

    try {
        const result = await api('POST', '/api/scan-sources');
        if (result.success && result.images) {
            if (state.discovery) {
                state.discovery.images = result.images;
            }
            state.imagesLoaded = true;
            renderImagesStatus();
            renderComponentsTable();
        } else {
            document.getElementById('images-status').classList.remove('loading');
            document.getElementById('images-status').textContent = result.error || 'Scan failed';
        }
    } catch (err) {
        document.getElementById('images-status').classList.remove('loading');
        document.getElementById('images-status').textContent = 'Scan failed: ' + err.message;
    } finally {
        btn.disabled = false;
        btn.textContent = 'Rescan';
    }
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

// --- Step 3: Mode & Step 4: Components ---
function rebuildComponents() {
    const disc = state.discovery;
    if (!disc) return;

    let compTypes;
    if (state.mode === 'standard') {
        compTypes = STANDARD_COMPONENTS;
    } else if (state.mode === 'ha') {
        compTypes = STANDARD_COMPONENTS;
    } else {
        compTypes = [state.singleComponent];
    }

    const isHA = state.mode === 'ha';

    state.components = compTypes.map(type => {
        let count = 1;
        if (isHA) {
            count = type === 'concerto' ? 3 : 2;
        }
        return {
            type,
            enabled: type !== 'concerto',
            count,
            cpu: DEFAULT_SPECS[type].cpu,
            ram: DEFAULT_SPECS[type].ram,
            disk: DEFAULT_SPECS[type].disk,
            node: getBestNode(disc) || '',
            iso: '',
        };
    });

    renderComponentsTable();
    initNetworkConfig();
    renderNetworkConfig();
    updateSummary();
}

function renderComponentsTable() {
    const disc = state.discovery;
    if (!disc) return;

    const tbody = document.getElementById('components-body');
    tbody.innerHTML = '';

    state.components.forEach((comp, idx) => {
        const tr = document.createElement('tr');

        // Find matching ISOs for this component
        const isos = findISOsForComponent(comp.type);
        const hasISOs = isos.length > 0;

        tr.innerHTML = `
            <td><input type="checkbox" data-idx="${idx}" class="comp-enable" ${comp.enabled ? 'checked' : ''}></td>
            <td>${COMP_NAMES[comp.type] || comp.type}</td>
            <td><input type="number" min="1" max="10" value="${comp.count}" data-idx="${idx}" class="comp-count"></td>
            <td><input type="number" min="1" max="64" value="${comp.cpu}" data-idx="${idx}" class="comp-cpu"></td>
            <td><input type="number" min="1" max="256" value="${comp.ram}" data-idx="${idx}" class="comp-ram"></td>
            <td><input type="number" min="10" max="2000" value="${comp.disk}" data-idx="${idx}" class="comp-disk"></td>
            <td>
                <select data-idx="${idx}" class="comp-node">
                    ${(disc.nodes || []).map(n => `<option value="${esc(n.Name)}" ${n.Name === comp.node ? 'selected' : ''}>${esc(n.Name)}</option>`).join('')}
                </select>
            </td>
            <td>
                <select data-idx="${idx}" class="comp-iso">
                    ${hasISOs
                        ? isos.map((iso, i) => `<option value="${esc(iso.Filename)}" ${i === 0 ? 'selected' : ''}>${esc(iso.Version || iso.Filename)} (${esc(iso.SourceName || '')})</option>`).join('')
                        : '<option value="">Scanning sources...</option>'
                    }
                </select>
            </td>`;
        tbody.appendChild(tr);

        // Auto-select first ISO
        if (hasISOs && !comp.iso) {
            comp.iso = isos[0].Filename;
        }
    });

    // Bind change handlers
    tbody.querySelectorAll('.comp-enable').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].enabled = e.target.checked;
        updateSummary();
        initNetworkConfig();
        renderNetworkConfig();
    }));
    tbody.querySelectorAll('.comp-count').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].count = +e.target.value;
        updateSummary();
        initNetworkConfig();
        renderNetworkConfig();
    }));
    tbody.querySelectorAll('.comp-cpu').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].cpu = +e.target.value;
        updateSummary();
    }));
    tbody.querySelectorAll('.comp-ram').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].ram = +e.target.value;
        updateSummary();
    }));
    tbody.querySelectorAll('.comp-disk').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].disk = +e.target.value;
        updateSummary();
    }));
    tbody.querySelectorAll('.comp-node').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].node = e.target.value;
    }));
    tbody.querySelectorAll('.comp-iso').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].iso = e.target.value;
    }));
}

function findISOsForComponent(type) {
    if (!state.discovery || !state.discovery.images) return [];
    // FlexVNF ISO is used for controller, router, and flexvnf
    const flexTypes = ['controller', 'router', 'flexvnf'];
    return state.discovery.images.filter(iso => {
        if (flexTypes.includes(type)) {
            return iso.Component === 'flexvnf' || iso.Component === type;
        }
        return iso.Component === type;
    });
}

// --- Step 5: Network Config ---

function getExistingBridges() {
    if (!state.discovery || !state.discovery.networks) return [];
    return state.discovery.networks.map(n => n.Name);
}

function getNextBridgeNum() {
    const bridges = getExistingBridges();
    const existing = bridges.map(b => {
        const m = b.match(/^vmbr(\d+)$/);
        return m ? parseInt(m[1]) : -1;
    }).filter(n => n >= 0);
    // Also account for bridges already proposed in autoCreate
    for (const b of state.networkConfig.autoCreate) {
        const m = b.match(/^vmbr(\d+)$/);
        if (m) existing.push(parseInt(m[1]));
    }
    return existing.length > 0 ? Math.max(...existing) + 1 : 1;
}

function proposeBridge() {
    const num = getNextBridgeNum();
    const name = `vmbr${num}`;
    state.networkConfig.autoCreate.add(name);
    return name;
}

function initNetworkConfig() {
    const disc = state.discovery;
    if (!disc) return;

    const bridges = getExistingBridges();
    const firstBridge = bridges.length > 0 ? bridges[0] : 'vmbr0';
    const enabled = state.components.filter(c => c.enabled);
    const enabledTypes = enabled.map(c => c.type);
    const isHA = state.mode === 'ha';

    // Reset auto-create tracking
    state.networkConfig.autoCreate = new Set();

    // Management always uses first existing bridge
    state.networkConfig.northbound = firstBridge;

    // Collect all interface fields needed by enabled components
    const neededFields = new Set();
    enabled.forEach(comp => {
        const defs = INTERFACE_DEFS[comp.type] || [];
        defs.forEach(d => neededFields.add(d.field));
    });

    // Southbound / Director-Router link — needed by director, analytics, router, concerto
    if (neededFields.has('directorRouter')) {
        state.networkConfig.directorRouter = proposeBridge();
    } else {
        state.networkConfig.directorRouter = '';
    }

    // Controller-Router link — needed by controller, router
    if (neededFields.has('controllerRouter')) {
        state.networkConfig.controllerRouter = proposeBridge();
    } else {
        state.networkConfig.controllerRouter = '';
    }

    // Controller WAN — needed by controller
    if (enabledTypes.includes('controller')) {
        if (state.networkConfig.controllerWANs.length === 0) {
            state.networkConfig.controllerWANs = [firstBridge];
        }
    } else {
        state.networkConfig.controllerWANs = [];
    }

    // Extra interfaces per component type
    if (!state.networkConfig.extraInterfaces) {
        state.networkConfig.extraInterfaces = {};
    }

    // Clear extras for disabled components
    for (const type of Object.keys(state.networkConfig.extraInterfaces)) {
        if (!enabledTypes.includes(type)) {
            delete state.networkConfig.extraInterfaces[type];
        }
    }

    // Router: auto-add one extra interface in HA mode
    if (enabledTypes.includes('router') && isHA) {
        if (!state.networkConfig.extraInterfaces.router || state.networkConfig.extraInterfaces.router.length === 0) {
            state.networkConfig.extraInterfaces.router = [
                { label: 'Interface 3', bridge: proposeBridge() },
            ];
        }
    }

    // FlexVNF: default WAN + LAN
    if (enabledTypes.includes('flexvnf')) {
        if (!state.networkConfig.extraInterfaces.flexvnf || state.networkConfig.extraInterfaces.flexvnf.length === 0) {
            state.networkConfig.extraInterfaces.flexvnf = [
                { label: 'WAN', bridge: firstBridge },
                { label: 'LAN', bridge: firstBridge },
            ];
        }
    }
}

function escSvg(str) {
    if (!str) return '';
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function renderTopologyDiagram() {
    const container = document.getElementById('topology-diagram');
    const nc = state.networkConfig;
    const enabled = state.components.filter(c => c.enabled);

    if (enabled.length === 0) {
        container.innerHTML = '<h3 class="net-section-title">Network Topology</h3><div class="topo-empty">Select components to see topology</div>';
        return;
    }

    const enabledTypes = enabled.map(c => c.type);
    const isHA = state.mode === 'ha' && enabled.some(c => c.count > 1);

    // Component colors
    const COLORS = {
        director: '#4f8fff', analytics: '#34d399', controller: '#fbbf24',
        router: '#a78bfa', concerto: '#f472b6', flexvnf: '#22d3ee',
    };

    // Build tier list: alternating buses and VM groups
    const tiers = [];

    // Management bus (always, shared across all groups)
    tiers.push({ kind: 'bus', label: 'Management', bridge: nc.northbound, shared: true });

    // Head-end VMs (Director, Analytics, Concerto)
    const headEnd = enabled.filter(c => ['director', 'analytics', 'concerto'].includes(c.type));
    if (headEnd.length > 0) {
        tiers.push({ kind: 'vms', components: headEnd });
    }

    // Southbound bus (shared)
    if (nc.directorRouter && enabledTypes.some(t => ['director', 'analytics', 'router', 'concerto'].includes(t))) {
        tiers.push({ kind: 'bus', label: 'Southbound', bridge: nc.directorRouter, shared: true });
    }

    // Router
    const routers = enabled.filter(c => c.type === 'router');
    if (routers.length > 0) {
        tiers.push({ kind: 'vms', components: routers });
    }

    // Controller-Router bus — per-group (isolated) in HA, shared otherwise
    if (nc.controllerRouter && enabledTypes.some(t => ['controller', 'router'].includes(t))) {
        tiers.push({ kind: 'bus', label: 'Ctrl \u2194 Rtr', bridge: nc.controllerRouter, shared: !isHA });
    }

    // Controller
    const controllers = enabled.filter(c => c.type === 'controller');
    if (controllers.length > 0) {
        tiers.push({ kind: 'vms', components: controllers });
    }

    // Controller WAN buses (shared)
    nc.controllerWANs.forEach((bridge, i) => {
        tiers.push({ kind: 'bus', label: 'WAN ' + (i + 1), bridge: bridge, shared: true, ownerType: 'controller' });
    });

    // Controller extra interfaces
    const ctrlExtras = (nc.extraInterfaces || {}).controller || [];
    ctrlExtras.forEach(iface => {
        tiers.push({ kind: 'bus', label: 'Ctrl ' + iface.label, bridge: iface.bridge, shared: true, ownerType: 'controller' });
    });

    // FlexVNF
    const flexvnfs = enabled.filter(c => c.type === 'flexvnf');
    if (flexvnfs.length > 0) {
        tiers.push({ kind: 'vms', components: flexvnfs });
    }

    // FlexVNF extra interfaces (WAN, LAN, etc.)
    const vnfExtras = (nc.extraInterfaces || {}).flexvnf || [];
    vnfExtras.forEach(iface => {
        tiers.push({ kind: 'bus', label: 'VNF ' + iface.label, bridge: iface.bridge, shared: true, ownerType: 'flexvnf' });
    });

    // Router extra interfaces
    const rtrExtras = (nc.extraInterfaces || {}).router || [];
    rtrExtras.forEach(iface => {
        tiers.push({ kind: 'bus', label: 'Rtr ' + iface.label, bridge: iface.bridge, shared: true, ownerType: 'router' });
    });

    // Director/Analytics/Concerto extra interfaces
    ['director', 'analytics', 'concerto'].forEach(ct => {
        const exts = (nc.extraInterfaces || {})[ct] || [];
        if (exts.length === 0 || !enabledTypes.includes(ct)) return;
        const prefix = COMP_NAMES[ct].substring(0, 3);
        exts.forEach(iface => {
            tiers.push({ kind: 'bus', label: prefix + ' ' + iface.label, bridge: iface.bridge, shared: true, ownerType: ct });
        });
    });

    // --- Layout constants ---
    const groupCount = isHA ? 2 : 1;
    const GROUP_GAP = 20;
    const PAD = 16;
    // Dynamic width: base + extra per max boxes in any tier
    const baseW = isHA ? 480 : 400;
    // Count max boxes per group to scale width
    let peakBoxes = 1;
    tiers.filter(t => t.kind === 'vms').forEach(t => {
        t.components.forEach(c => {
            const perGroup = isHA && c.count > 1 ? Math.ceil(c.count / 2) : c.count;
            if (perGroup > peakBoxes) peakBoxes = perGroup;
        });
    });
    // Extra right margin for management spine lines that route outside the bus bars
    const mgmtSpineMargin = 30;
    const W = Math.max(baseW, PAD * 2 + (isHA ? GROUP_GAP : 0) + groupCount * (peakBoxes * 86 + (peakBoxes - 1) * 8 + 20)) + mgmtSpineMargin;
    const CW = W - PAD * 2;
    const groupW = isHA ? (CW - GROUP_GAP) / 2 : CW;
    const BUS_H = 24;
    const VM_H = 32;
    const GAP = 18;
    const VM_GAP = 8;
    const VM_W_MAX = 80;

    // Group X offsets
    const groupXArr = isHA
        ? [PAD, PAD + groupW + GROUP_GAP]
        : [PAD];

    // Get VM boxes for a tier within a specific group
    function getGroupBoxes(tier, group) {
        const boxes = [];
        tier.components.forEach(comp => {
            if (isHA && comp.count > 1) {
                const perGroup = Math.ceil(comp.count / 2);
                const start = group * perGroup;
                const end = Math.min(start + perGroup, comp.count);
                for (let i = start; i < end; i++) {
                    boxes.push({
                        type: comp.type,
                        label: COMP_NAMES[comp.type] + ' ' + (i + 1),
                    });
                }
            } else if (isHA && comp.count === 1) {
                if (group === 0) {
                    boxes.push({ type: comp.type, label: COMP_NAMES[comp.type] });
                }
            } else {
                for (let i = 0; i < comp.count; i++) {
                    const label = comp.count > 1
                        ? COMP_NAMES[comp.type] + ' ' + (i + 1)
                        : COMP_NAMES[comp.type];
                    boxes.push({ type: comp.type, label });
                }
            }
        });
        return boxes;
    }

    function positionBoxes(boxes, gx, gw, vmW) {
        if (boxes.length === 0) return [];
        const totalW = boxes.length * vmW + (boxes.length - 1) * VM_GAP;
        const startX = gx + (gw - totalW) / 2;
        return boxes.map((box, i) => ({
            ...box,
            x: startX + i * (vmW + VM_GAP),
            cx: startX + i * (vmW + VM_GAP) + vmW / 2,
        }));
    }

    // Calculate VM_W based on max boxes per group
    let maxPerGroup = 1;
    tiers.filter(t => t.kind === 'vms').forEach(t => {
        for (let g = 0; g < groupCount; g++) {
            const n = getGroupBoxes(t, g).length;
            if (n > maxPerGroup) maxPerGroup = n;
        }
    });
    const VM_W = Math.min(VM_W_MAX, Math.floor((groupW - (maxPerGroup - 1) * VM_GAP) / maxPerGroup));
    const vmFontSize = VM_W >= 60 ? 10 : 9;

    // Calculate Y positions
    let yPos = PAD;
    tiers.forEach(tier => {
        tier.y = yPos;
        tier.height = tier.kind === 'bus' ? BUS_H : VM_H;
        yPos += tier.height + GAP;
    });
    const H = yPos + PAD - GAP;

    // Build SVG
    let svg = '<svg width="' + W + '" height="' + H + '" xmlns="http://www.w3.org/2000/svg" style="display:block">';
    svg += '<rect width="' + W + '" height="' + H + '" fill="#181b28" rx="6" stroke="#2e3248" stroke-width="1"/>';

    // --- Management lines for non-adjacent VMs ---
    // Path: UP from VM top-right → RIGHT in the gap → UP along right margin to Mgmt bus
    // This avoids crossing over bus bars and other VM boxes
    const mgmtBus = tiers[0];
    const mgmtBridge = nc.northbound || '';
    for (let g = 0; g < groupCount; g++) {
        const gx = groupXArr[g];
        tiers.forEach((tier, ti) => {
            if (tier.kind !== 'vms') return;
            // Skip tier directly below Management (already has solid connectors)
            if (ti === 1 && tiers[0].kind === 'bus') return;

            const boxes = getGroupBoxes(tier, g);
            const positions = positionBoxes(boxes, gx, groupW, VM_W);

            positions.forEach((box, bi) => {
                const startX = box.x + VM_W - 5 - bi * 3;  // top-right of VM box, staggered
                const turnY = tier.y - Math.floor(GAP / 2); // midpoint of gap above VM
                const spineX = W - PAD + 6 + g * 8 + bi * 6; // well past the right edge of buses
                const busBottom = mgmtBus.y + mgmtBus.height;

                // Path: UP from VM top into gap → RIGHT to margin → UP to mgmt bus
                svg += '<path d="M' + startX + ',' + tier.y + ' V' + turnY + ' H' + spineX + ' V' + busBottom + '" fill="none" stroke="#5eadff" stroke-width="1.5" stroke-dasharray="4,3" opacity="0.6"/>';

                // Small dot at the mgmt bus connection point
                svg += '<circle cx="' + spineX + '" cy="' + busBottom + '" r="2.5" fill="#5eadff" opacity="0.8"/>';

                // Bridge label near VM top-right
                svg += '<text x="' + (box.x + VM_W + 3) + '" y="' + (tier.y - 2) + '" fill="#7dc4ff" font-size="8" font-family="SF Mono,Menlo,monospace" text-anchor="start" opacity="0.85">' + escSvg(mgmtBridge) + '</text>';
            });
        });
    }

    // --- Regular connector lines (solid, behind boxes) ---
    for (let g = 0; g < groupCount; g++) {
        tiers.forEach((tier, ti) => {
            if (tier.kind !== 'vms') return;

            const busAbove = (ti > 0 && tiers[ti - 1].kind === 'bus') ? tiers[ti - 1] : null;
            const busBelow = (ti < tiers.length - 1 && tiers[ti + 1].kind === 'bus') ? tiers[ti + 1] : null;

            const boxes = getGroupBoxes(tier, g);
            const positions = positionBoxes(boxes, groupXArr[g], groupW, VM_W);

            positions.forEach(box => {
                if (busAbove) {
                    svg += '<line x1="' + box.cx + '" y1="' + (busAbove.y + busAbove.height) + '" x2="' + box.cx + '" y2="' + tier.y + '" stroke="#505878" stroke-width="1.5"/>';
                }
                if (busBelow) {
                    svg += '<line x1="' + box.cx + '" y1="' + (tier.y + VM_H) + '" x2="' + box.cx + '" y2="' + busBelow.y + '" stroke="#505878" stroke-width="1.5"/>';
                }
            });
        });
    }

    // --- Extra interface connectors (non-adjacent buses owned by a component) ---
    // For each VM tier, find all buses with matching ownerType that aren't directly adjacent
    for (let g = 0; g < groupCount; g++) {
        tiers.forEach((tier, ti) => {
            if (tier.kind !== 'vms') return;

            // Collect all component types in this VM tier
            const tierTypes = new Set();
            tier.components.forEach(c => tierTypes.add(c.type));

            // Find owned buses that are NOT directly adjacent (already drawn above)
            const busAboveIdx = (ti > 0 && tiers[ti - 1].kind === 'bus') ? ti - 1 : -1;
            const busBelowIdx = (ti < tiers.length - 1 && tiers[ti + 1].kind === 'bus') ? ti + 1 : -1;

            tiers.forEach((busTier, bi) => {
                if (busTier.kind !== 'bus') return;
                if (!busTier.ownerType || !tierTypes.has(busTier.ownerType)) return;
                if (bi === busAboveIdx || bi === busBelowIdx) return; // already drawn

                const boxes = getGroupBoxes(tier, g);
                const positions = positionBoxes(boxes, groupXArr[g], groupW, VM_W);

                // Only draw for boxes matching the ownerType
                positions.forEach((box, bxi) => {
                    if (box.type !== busTier.ownerType) return;
                    const offset = 6 + bxi * 3;
                    const lineX = box.cx + offset;

                    if (bi > ti) {
                        // Bus is below: line from VM bottom down to bus top
                        svg += '<line x1="' + lineX + '" y1="' + (tier.y + VM_H) + '" x2="' + lineX + '" y2="' + busTier.y + '" stroke="#505878" stroke-width="1.5"/>';
                    } else {
                        // Bus is above: line from VM top up to bus bottom
                        svg += '<line x1="' + lineX + '" y1="' + tier.y + '" x2="' + lineX + '" y2="' + (busTier.y + busTier.height) + '" stroke="#505878" stroke-width="1.5"/>';
                    }
                });
            });
        });
    }

    // --- Bus bars ---
    tiers.forEach(tier => {
        if (tier.kind !== 'bus') return;

        if (tier.shared || !isHA) {
            // Full-width shared bus
            svg += '<rect x="' + PAD + '" y="' + tier.y + '" width="' + CW + '" height="' + BUS_H + '" rx="3" fill="#2a2f4a" stroke="#3a4068" stroke-width="0.5"/>';
            svg += '<text x="' + (PAD + 8) + '" y="' + (tier.y + BUS_H / 2 + 1) + '" fill="#a0a4be" font-size="10" font-weight="500" font-family="-apple-system,BlinkMacSystemFont,sans-serif" dominant-baseline="middle">' + escSvg(tier.label) + '</text>';
            if (tier.bridge) {
                svg += '<text x="' + (PAD + CW - 8) + '" y="' + (tier.y + BUS_H / 2 + 1) + '" fill="#7dc4ff" font-size="10" font-family="SF Mono,Menlo,monospace" dominant-baseline="middle" text-anchor="end">' + escSvg(tier.bridge) + '</text>';
            }
        } else {
            // Per-group isolated bus (e.g., Ctrl↔Router in HA)
            for (let g = 0; g < groupCount; g++) {
                const gx = groupXArr[g];
                svg += '<rect x="' + gx + '" y="' + tier.y + '" width="' + groupW + '" height="' + BUS_H + '" rx="3" fill="#2a2f4a" stroke="#3a4068" stroke-width="0.5"/>';
                svg += '<text x="' + (gx + 6) + '" y="' + (tier.y + BUS_H / 2 + 1) + '" fill="#a0a4be" font-size="9" font-weight="500" font-family="-apple-system,BlinkMacSystemFont,sans-serif" dominant-baseline="middle">' + escSvg(tier.label) + '</text>';
                if (tier.bridge) {
                    svg += '<text x="' + (gx + groupW - 6) + '" y="' + (tier.y + BUS_H / 2 + 1) + '" fill="#7dc4ff" font-size="9" font-family="SF Mono,Menlo,monospace" dominant-baseline="middle" text-anchor="end">' + escSvg(tier.bridge) + '</text>';
                }
            }
        }
    });

    // --- VM boxes ---
    for (let g = 0; g < groupCount; g++) {
        tiers.forEach(tier => {
            if (tier.kind !== 'vms') return;

            const boxes = getGroupBoxes(tier, g);
            const positions = positionBoxes(boxes, groupXArr[g], groupW, VM_W);

            positions.forEach(box => {
                const color = COLORS[box.type] || '#888';
                svg += '<rect x="' + box.x + '" y="' + tier.y + '" width="' + VM_W + '" height="' + VM_H + '" rx="4" fill="' + color + '25" stroke="' + color + '" stroke-width="1.5"/>';
                svg += '<text x="' + (box.x + VM_W / 2) + '" y="' + (tier.y + VM_H / 2 + 1) + '" fill="' + color + '" font-size="' + vmFontSize + '" font-weight="600" font-family="-apple-system,BlinkMacSystemFont,sans-serif" text-anchor="middle" dominant-baseline="middle">' + escSvg(box.label) + '</text>';
            });
        });
    }

    // --- HA group labels ---
    if (isHA) {
        for (let g = 0; g < groupCount; g++) {
            const gx = groupXArr[g];
            const labelX = gx + groupW / 2;
            svg += '<text x="' + labelX + '" y="' + (H - 4) + '" fill="#6b7094" font-size="9" font-family="-apple-system,BlinkMacSystemFont,sans-serif" text-anchor="middle">Group ' + (g + 1) + '</text>';
        }
    }

    svg += '</svg>';

    container.innerHTML = '<h3 class="net-section-title">Network Topology</h3>' + svg;
}

function renderNetworkConfig() {
    const disc = state.discovery;
    if (!disc) return;

    renderTopologyDiagram();
    renderBridgeAssignments();
    renderInstancePreview();
}

function buildBridgeDropdown(selectedValue, fieldKey, opts = {}) {
    const bridges = getExistingBridges();
    const nc = state.networkConfig;
    const isAutoCreate = nc.autoCreate.has(selectedValue);
    const allowNone = opts.allowNone || false;

    let html = '';

    // "none" option for optional fields
    if (allowNone) {
        html += `<option value="" ${!selectedValue ? 'selected' : ''}>— none —</option>`;
    }

    // If the selected value is an auto-create bridge, show it first
    if (isAutoCreate) {
        html += `<option value="${esc(selectedValue)}" class="new-bridge-opt" selected>New: ${esc(selectedValue)}</option>`;
    }

    // Existing bridges
    if (isAutoCreate || allowNone) {
        html += `<option disabled>── existing ──</option>`;
    }
    bridges.forEach(b => {
        const sel = (!isAutoCreate && b === selectedValue) ? 'selected' : '';
        html += `<option value="${esc(b)}" ${sel}>${esc(b)}</option>`;
    });

    html += `<option value="__new__">+ Create custom bridge</option>`;

    return `<select data-field="${esc(fieldKey)}" class="net-bridge">${html}</select>`;
}

function renderBridgeAssignments() {
    const container = document.getElementById('bridge-assignments');
    const nc = state.networkConfig;
    const enabled = state.components.filter(c => c.enabled);
    const enabledTypes = enabled.map(c => c.type);

    const rows = [];

    // Management (always shown)
    rows.push({
        label: 'Management (Northbound)',
        field: 'northbound',
        value: nc.northbound,
        desc: 'All instances',
        optional: false,
    });

    // Southbound Link
    if (nc.directorRouter) {
        const users = ['Director', 'Analytics', 'Router', 'Concerto'].filter(n =>
            enabledTypes.includes(n.toLowerCase())
        );
        rows.push({
            label: 'Southbound Link',
            field: 'directorRouter',
            value: nc.directorRouter,
            desc: users.join(', '),
            optional: false,
        });
    }

    // Controller-Router Link
    if (nc.controllerRouter) {
        rows.push({
            label: 'Controller-Router Link',
            field: 'controllerRouter',
            value: nc.controllerRouter,
            desc: 'Controller, Router',
            optional: false,
        });
    }

    // Controller WAN bridges
    if (enabledTypes.includes('controller')) {
        nc.controllerWANs.forEach((bridge, i) => {
            const wanNum = i + 1;
            rows.push({
                label: `Controller WAN ${wanNum}`,
                field: `controllerWAN_${i}`,
                value: bridge,
                desc: 'Controller' + (enabledTypes.includes('flexvnf') ? ', FlexVNF' : ''),
                optional: false,
                removable: i > 0,
                wanIndex: i,
                canMoveUp: i > 0,
                canMoveDown: i < nc.controllerWANs.length - 1,
            });
        });

        // Add WAN button (max 3)
        if (nc.controllerWANs.length < 3) {
            rows.push({ addWAN: true });
        }
    }

    // Extra interfaces per component type
    const extras = nc.extraInterfaces || {};
    for (const compType of enabledTypes) {
        const compExtras = extras[compType] || [];
        const baseDefs = INTERFACE_DEFS[compType] || [];
        const baseCount = baseDefs.length;
        const compName = COMP_NAMES[compType] || compType;

        compExtras.forEach((iface, i) => {
            rows.push({
                label: `${compName} ${iface.label}`,
                field: `extra_${compType}_${i}`,
                value: iface.bridge,
                desc: compName,
                optional: false,
                removable: true,
                canMoveUp: i > 0,
                canMoveDown: i < compExtras.length - 1,
            });
        });
    }

    let html = '<h3 class="net-section-title">Bridge Assignments</h3>';

    rows.forEach(r => {
        if (r.addWAN) {
            html += `<div class="network-row network-row-add">
                <button class="btn btn-secondary btn-small" id="add-wan-btn">+ Add WAN</button>
            </div>`;
            return;
        }

        const badge = nc.autoCreate.has(r.value)
            ? '<span class="net-badge net-badge-new">auto-create</span>'
            : '';

        const removeBtn = r.removable
            ? `<button class="btn-remove-iface" data-field="${esc(r.field)}">&times;</button>`
            : '';

        let moveButtons = '';
        if (r.canMoveUp || r.canMoveDown) {
            moveButtons = '<span class="iface-reorder">';
            moveButtons += r.canMoveUp
                ? `<button class="btn-move-iface" data-field="${esc(r.field)}" data-dir="up" title="Move up">&#9650;</button>`
                : '<span class="btn-move-placeholder"></span>';
            moveButtons += r.canMoveDown
                ? `<button class="btn-move-iface" data-field="${esc(r.field)}" data-dir="down" title="Move down">&#9660;</button>`
                : '<span class="btn-move-placeholder"></span>';
            moveButtons += '</span>';
        }

        html += `<div class="network-row">
            <label>${esc(r.label)}</label>
            ${buildBridgeDropdown(r.value, r.field, { allowNone: r.optional })}
            ${badge}
            ${moveButtons}
            ${removeBtn}
            <span class="net-desc">${esc(r.desc)}</span>
        </div>`;
    });

    container.innerHTML = html;

    // Bind bridge dropdown change events
    container.querySelectorAll('.net-bridge').forEach(sel => {
        sel.addEventListener('change', () => {
            const field = sel.dataset.field;
            const value = sel.value;

            if (value === '__new__') {
                expandInlineCreate(sel, field);
                return;
            }

            setNetworkFieldValue(field, value);
            renderNetworkConfig();
        });
    });

    // Add WAN button
    const addWanBtn = container.querySelector('#add-wan-btn');
    if (addWanBtn) {
        addWanBtn.addEventListener('click', () => {
            const firstBridge = getExistingBridges()[0] || 'vmbr0';
            state.networkConfig.controllerWANs.push(firstBridge);
            renderNetworkConfig();
        });
    }

    // Remove buttons
    container.querySelectorAll('.btn-remove-iface').forEach(btn => {
        btn.addEventListener('click', () => {
            const field = btn.dataset.field;
            if (field.startsWith('controllerWAN_')) {
                const idx = parseInt(field.split('_')[1]);
                state.networkConfig.controllerWANs.splice(idx, 1);
            } else if (field.startsWith('extra_')) {
                const parts = field.split('_');
                const compType = parts[1];
                const idx = parseInt(parts[2]);
                const arr = state.networkConfig.extraInterfaces[compType];
                if (arr) arr.splice(idx, 1);
            }
            renderNetworkConfig();
        });
    });

    // Move up/down buttons
    container.querySelectorAll('.btn-move-iface').forEach(btn => {
        btn.addEventListener('click', () => {
            const field = btn.dataset.field;
            const dir = btn.dataset.dir;

            if (field.startsWith('controllerWAN_')) {
                const idx = parseInt(field.split('_')[1]);
                const arr = state.networkConfig.controllerWANs;
                const swapIdx = dir === 'up' ? idx - 1 : idx + 1;
                if (swapIdx >= 0 && swapIdx < arr.length) {
                    [arr[idx], arr[swapIdx]] = [arr[swapIdx], arr[idx]];
                }
            } else if (field.startsWith('extra_')) {
                const parts = field.split('_');
                const compType = parts[1];
                const idx = parseInt(parts[2]);
                const arr = state.networkConfig.extraInterfaces[compType];
                if (!arr) return;
                const swapIdx = dir === 'up' ? idx - 1 : idx + 1;
                if (swapIdx >= 0 && swapIdx < arr.length) {
                    [arr[idx], arr[swapIdx]] = [arr[swapIdx], arr[idx]];
                }
            }
            renderNetworkConfig();
        });
    });
}

function expandInlineCreate(selectEl, field) {
    const row = selectEl.closest('.network-row');
    const nextNum = getNextBridgeNum();
    const proposedName = `vmbr${nextNum}`;

    // Replace the dropdown with inline bridge name + VLAN fields
    const wrapper = document.createElement('div');
    wrapper.className = 'inline-create';
    wrapper.innerHTML = `
        <input type="text" class="inline-bridge-name" value="${esc(proposedName)}" placeholder="vmbr#">
        <input type="number" class="inline-vlan" value="${nextNum}" min="0" max="4094" placeholder="VLAN">
        <button class="btn btn-primary btn-small inline-confirm">OK</button>
        <button class="btn btn-secondary btn-small inline-cancel">Cancel</button>
    `;

    selectEl.replaceWith(wrapper);

    const nameInput = wrapper.querySelector('.inline-bridge-name');
    const vlanInput = wrapper.querySelector('.inline-vlan');

    nameInput.focus();
    nameInput.select();

    wrapper.querySelector('.inline-confirm').addEventListener('click', () => {
        const bridgeName = nameInput.value.trim();
        if (!bridgeName) return;

        // Add to autoCreate set and set the value
        state.networkConfig.autoCreate.add(bridgeName);
        setNetworkFieldValue(field, bridgeName);
        renderNetworkConfig();
    });

    wrapper.querySelector('.inline-cancel').addEventListener('click', () => {
        // Restore previous value and re-render
        renderNetworkConfig();
    });

    // Enter key confirms
    nameInput.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); wrapper.querySelector('.inline-confirm').click(); }
        if (e.key === 'Escape') { wrapper.querySelector('.inline-cancel').click(); }
    });
    vlanInput.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') { e.preventDefault(); wrapper.querySelector('.inline-confirm').click(); }
        if (e.key === 'Escape') { wrapper.querySelector('.inline-cancel').click(); }
    });
}

function getNetworkFieldValue(field) {
    const nc = state.networkConfig;
    if (field.startsWith('controllerWAN_')) {
        return nc.controllerWANs[parseInt(field.split('_')[1])] || '';
    }
    if (field.startsWith('flexvnf_')) {
        const idx = parseInt(field.split('_')[1]);
        return nc.flexvnfInterfaces[idx] ? nc.flexvnfInterfaces[idx].bridge : '';
    }
    return nc[field] || '';
}

function setNetworkFieldValue(field, value) {
    const nc = state.networkConfig;
    if (field.startsWith('controllerWAN_')) {
        const idx = parseInt(field.split('_')[1]);
        nc.controllerWANs[idx] = value;
    } else if (field.startsWith('extra_')) {
        // extra_{compType}_{index}
        const parts = field.split('_');
        const compType = parts[1];
        const idx = parseInt(parts[2]);
        if (nc.extraInterfaces[compType] && nc.extraInterfaces[compType][idx]) {
            nc.extraInterfaces[compType][idx].bridge = value;
        }
    } else {
        nc[field] = value;
    }
}

function renderInstancePreview() {
    const container = document.getElementById('instance-preview');
    const nc = state.networkConfig;
    const enabled = state.components.filter(c => c.enabled);
    const prefix = document.getElementById('deploy-prefix').value.trim() || 'versa';

    if (enabled.length === 0) {
        container.innerHTML = '';
        return;
    }

    let html = '<h3 class="net-section-title">Instance Preview</h3>';
    html += '<div class="instance-preview-grid">';

    enabled.forEach(comp => {
        for (let i = 1; i <= comp.count; i++) {
            const vmName = comp.count > 1
                ? `${prefix}-${comp.type}-${i}`
                : `${prefix}-${comp.type}`;
            const interfaces = resolveInterfaces(comp.type);
            const compLabel = COMP_NAMES[comp.type] || comp.type;

            html += `<div class="instance-card">`;
            html += `<div class="instance-card-header">`;
            html += `<span class="instance-card-name">${esc(vmName)}</span>`;
            html += `<span class="instance-card-type">${esc(compLabel)}</span>`;
            html += `</div>`;
            html += `<table class="instance-iface-table">`;
            html += `<thead><tr><th>eth#</th><th>Purpose</th><th>Bridge</th><th></th><th></th></tr></thead>`;
            html += `<tbody>`;

            interfaces.forEach(iface => {
                const bridge = iface.bridge || '';
                const isAuto = nc.autoCreate.has(bridge);
                const badgeHtml = isAuto
                    ? '<span class="net-badge net-badge-new">new</span>'
                    : '';
                const bridgeDisplay = bridge || '<span class="text-muted">—</span>';

                // Move arrows for non-fixed interfaces
                let arrowsHtml = '';
                if (!iface.fixed && iface.moveGroup) {
                    const canUp = iface.moveIdx > 0;
                    const canDown = iface.moveIdx < iface.moveMax;
                    const dataAttrs = `data-move-group="${esc(iface.moveGroup)}" data-move-idx="${iface.moveIdx}" data-move-comp="${esc(iface.moveCompType || '')}"`;
                    arrowsHtml = '<span class="iface-reorder-inline">';
                    arrowsHtml += canUp
                        ? `<button class="btn-move-preview" ${dataAttrs} data-dir="up" title="Move up">&#9650;</button>`
                        : '';
                    arrowsHtml += canDown
                        ? `<button class="btn-move-preview" ${dataAttrs} data-dir="down" title="Move down">&#9660;</button>`
                        : '';
                    arrowsHtml += '</span>';
                }

                html += `<tr>
                    <td class="iface-eth">eth${iface.eth}</td>
                    <td class="iface-label">${esc(iface.label)}</td>
                    <td class="iface-bridge">${bridge ? esc(bridge) : bridgeDisplay}</td>
                    <td class="iface-badge">${badgeHtml}</td>
                    <td class="iface-actions">${arrowsHtml}</td>
                </tr>`;
            });

            html += `</tbody></table>`;

            // Add interface buttons
            if (comp.type === 'controller' && nc.controllerWANs.length < 3) {
                html += `<button class="btn btn-secondary btn-small instance-add-iface add-controller-wan">+ Add WAN</button>`;
            }
            html += `<button class="btn btn-secondary btn-small instance-add-iface add-extra-iface" data-comp="${esc(comp.type)}">+ Add Interface</button>`;

            html += `</div>`;
        }
    });

    html += '</div>';
    container.innerHTML = html;

    // Controller add WAN buttons
    container.querySelectorAll('.add-controller-wan').forEach(btn => {
        btn.addEventListener('click', () => {
            const firstBridge = getExistingBridges()[0] || 'vmbr0';
            state.networkConfig.controllerWANs.push(firstBridge);
            renderNetworkConfig();
        });
    });

    // Generic add interface button (all component types)
    container.querySelectorAll('.add-extra-iface').forEach(btn => {
        btn.addEventListener('click', () => {
            const compType = btn.dataset.comp;
            const nc = state.networkConfig;
            if (!nc.extraInterfaces[compType]) {
                nc.extraInterfaces[compType] = [];
            }
            const baseDefs = INTERFACE_DEFS[compType] || [];
            const wanCount = compType === 'controller' ? nc.controllerWANs.length : 0;
            const ethNum = baseDefs.length + wanCount + nc.extraInterfaces[compType].length;
            const firstBridge = getExistingBridges()[0] || 'vmbr0';
            nc.extraInterfaces[compType].push({
                label: `Interface ${ethNum}`,
                bridge: firstBridge,
            });
            renderNetworkConfig();
        });
    });

    // Move up/down buttons in instance preview
    container.querySelectorAll('.btn-move-preview').forEach(btn => {
        btn.addEventListener('click', () => {
            const group = btn.dataset.moveGroup;
            const idx = parseInt(btn.dataset.moveIdx);
            const dir = btn.dataset.dir;
            const swapIdx = dir === 'up' ? idx - 1 : idx + 1;

            if (group === 'wan') {
                const arr = state.networkConfig.controllerWANs;
                if (swapIdx >= 0 && swapIdx < arr.length) {
                    [arr[idx], arr[swapIdx]] = [arr[swapIdx], arr[idx]];
                }
            } else if (group === 'extra') {
                const compType = btn.dataset.moveComp;
                const arr = state.networkConfig.extraInterfaces[compType];
                if (arr && swapIdx >= 0 && swapIdx < arr.length) {
                    [arr[idx], arr[swapIdx]] = [arr[swapIdx], arr[idx]];
                }
            }
            renderNetworkConfig();
        });
    });
}

function resolveInterfaces(compType) {
    const nc = state.networkConfig;
    const baseDefs = INTERFACE_DEFS[compType] || [];
    const result = [];

    baseDefs.forEach(def => {
        const bridge = nc[def.field] || '';
        if (!def.required && !bridge) return;
        result.push({
            eth: def.eth,
            label: def.label,
            bridge: bridge,
            fixed: true, // base interfaces can't be moved
        });
    });

    // Add dynamic WAN interfaces for controllers
    if (compType === 'controller') {
        nc.controllerWANs.forEach((bridge, i) => {
            result.push({
                eth: result.length,
                label: `WAN ${i + 1}`,
                bridge: bridge,
                moveGroup: 'wan',
                moveIdx: i,
                moveMax: nc.controllerWANs.length - 1,
            });
        });
    }

    // Add extra interfaces (all component types)
    const extras = (nc.extraInterfaces || {})[compType] || [];
    extras.forEach((iface, i) => {
        result.push({
            eth: result.length,
            label: iface.label,
            bridge: iface.bridge,
            moveGroup: 'extra',
            moveCompType: compType,
            moveIdx: i,
            moveMax: extras.length - 1,
        });
    });

    return result;
}

function buildNetworkPayload() {
    const nc = state.networkConfig;
    const extras = nc.extraInterfaces || {};

    // Map first extra interface for analytics/router back to legacy backend fields
    const analyticsExtras = extras.analytics || [];
    const routerExtras = extras.router || [];

    return {
        NorthboundBridge: nc.northbound,
        DirectorRouterBridge: nc.directorRouter,
        ControllerRouterBridge: nc.controllerRouter,
        ControllerWANBridges: nc.controllerWANs.length > 0 ? nc.controllerWANs : [],
        AnalyticsClusterBridge: analyticsExtras.length > 0 ? analyticsExtras[0].bridge : '',
        RouterHABridge: routerExtras.length > 0 ? routerExtras[0].bridge : '',
    };
}

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

function getBestNode(disc) {
    if (!disc.nodes || disc.nodes.length === 0) return '';
    // Pick the online node with the most free RAM, then most free CPU
    const online = disc.nodes.filter(n => n.Status === 'online');
    const candidates = online.length > 0 ? online : disc.nodes;
    candidates.sort((a, b) => {
        const freeRamA = a.RAMGB - a.RAMUsedGB, freeRamB = b.RAMGB - b.RAMUsedGB;
        if (freeRamB !== freeRamA) return freeRamB - freeRamA;
        return (b.CPUCores - b.CPUUsed) - (a.CPUCores - a.CPUUsed);
    });
    return candidates[0].Name;
}

// --- Helpers ---
function showStep(id) {
    document.getElementById(id).classList.remove('hidden');
}

function esc(str) {
    if (!str) return '';
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
}

// --- Deployed Instances ---

async function loadDeployments() {
    const loadingEl = document.getElementById('deployments-loading');
    const emptyEl = document.getElementById('deployments-empty');
    const listEl = document.getElementById('deployments-list');

    loadingEl.classList.remove('hidden');
    emptyEl.classList.add('hidden');
    listEl.innerHTML = '';

    try {
        const result = await api('GET', '/api/deployments');
        loadingEl.classList.add('hidden');

        if (!result.success) {
            listEl.innerHTML = `<div class="error-msg">${esc(result.error || 'Failed to load')}</div>`;
            return;
        }

        const deployments = result.deployments || {};
        const prefixes = Object.keys(deployments).sort();

        // Flatten all VMs into one list, sorted by prefix then VMID
        const allVMs = [];
        for (const prefix of prefixes) {
            const group = deployments[prefix];
            for (const vm of (group.vms || [])) {
                allVMs.push({ ...vm, prefix });
            }
        }

        if (allVMs.length === 0) {
            emptyEl.classList.remove('hidden');
            return;
        }

        allVMs.sort((a, b) => a.prefix.localeCompare(b.prefix) || a.VMID - b.VMID);

        renderDeploymentTable(listEl, allVMs);
    } catch (err) {
        loadingEl.classList.add('hidden');
        listEl.innerHTML = `<div class="error-msg">Failed to load: ${esc(err.message)}</div>`;
    }
}

function renderDeploymentTable(container, allVMs) {
    const el = document.createElement('div');
    el.className = 'deployment-table-wrap';

    // Action bar
    let html = `<div class="deployment-actions">
        <label class="deploy-select-all-label"><input type="checkbox" class="deploy-select-all"> Select all</label>
        <button class="btn btn-small btn-warning deploy-action-stop" disabled>Stop Selected</button>
        <button class="btn btn-small btn-danger deploy-action-delete" disabled>Delete Selected</button>
        <span class="deploy-selection-count text-muted"></span>
    </div>`;

    // Table
    html += `<table class="deployment-vm-table">
        <thead><tr>
            <th style="width:30px"></th>
            <th>VMID</th>
            <th>Name</th>
            <th>Deployment</th>
            <th>Component</th>
            <th>Status</th>
        </tr></thead>
        <tbody>`;

    allVMs.forEach(vm => {
        const statusClass = vm.Status === 'running' ? 'running' : 'stopped';
        // Extract component type from tags
        const compTag = (vm.Tags || []).find(t => t.startsWith('versa-') && t !== 'versa-deployer' && !t.startsWith('versa-deploy-') && !t.startsWith('versa-ha-'));
        const compType = compTag ? compTag.replace('versa-', '') : '';

        html += `<tr data-vmid="${vm.VMID}" data-prefix="${esc(vm.prefix)}" data-name="${esc(vm.Name)}">
            <td><input type="checkbox" class="deploy-vm-check" value="${vm.VMID}"></td>
            <td class="deploy-vmid">${vm.VMID}</td>
            <td>${esc(vm.Name)}</td>
            <td><span class="deployment-prefix-tag">${esc(vm.prefix)}</span></td>
            <td>${esc(compType)}</td>
            <td><span class="vm-status-badge ${statusClass}">${esc(vm.Status)}</span></td>
        </tr>`;
    });

    html += `</tbody></table>`;

    el.innerHTML = html;
    container.appendChild(el);

    // --- Event bindings ---

    const selectAll = el.querySelector('.deploy-select-all');
    const checkboxes = el.querySelectorAll('.deploy-vm-check');
    const stopBtn = el.querySelector('.deploy-action-stop');
    const deleteBtn = el.querySelector('.deploy-action-delete');
    const countEl = el.querySelector('.deploy-selection-count');

    function getSelected() {
        const selected = [];
        checkboxes.forEach(cb => {
            if (cb.checked) {
                const row = cb.closest('tr');
                selected.push({
                    vmid: parseInt(cb.value),
                    prefix: row.dataset.prefix,
                    name: row.dataset.name,
                });
            }
        });
        return selected;
    }

    function updateButtons() {
        const selected = getSelected();
        const count = selected.length;
        stopBtn.disabled = count === 0;
        deleteBtn.disabled = count === 0;
        countEl.textContent = count > 0 ? `${count} selected` : '';
    }

    selectAll.addEventListener('change', () => {
        checkboxes.forEach(cb => { cb.checked = selectAll.checked; });
        updateButtons();
    });

    checkboxes.forEach(cb => {
        cb.addEventListener('change', () => {
            if (!cb.checked) selectAll.checked = false;
            else if (getSelected().length === checkboxes.length) selectAll.checked = true;
            updateButtons();
        });
    });

    // Stop selected
    stopBtn.addEventListener('click', async () => {
        const selected = getSelected();
        if (selected.length === 0) return;

        stopBtn.disabled = true;
        stopBtn.textContent = 'Stopping...';

        try {
            await api('POST', '/api/deployments/stop', {
                vmids: selected.map(s => s.vmid),
                prefix: '',
            });
            loadDeployments();
        } catch (err) {
            alert('Stop failed: ' + err.message);
            stopBtn.textContent = 'Stop Selected';
            stopBtn.disabled = false;
        }
    });

    // Delete selected
    deleteBtn.addEventListener('click', async () => {
        const selected = getSelected();
        if (selected.length === 0) return;

        const names = selected.map(s => s.name).join(', ');
        if (!confirm(`Delete ${selected.length === 1 ? selected[0].name : selected.length + ' VMs'}?\n\n${names}`)) {
            return;
        }

        deleteBtn.disabled = true;
        deleteBtn.textContent = 'Deleting...';

        try {
            const result = await api('POST', '/api/deployments/delete', {
                vmids: selected.map(s => s.vmid),
                prefix: '',
            });

            if (!result.success) {
                alert('Delete failed: ' + (result.error || 'Unknown error'));
                deleteBtn.textContent = 'Delete Selected';
                deleteBtn.disabled = false;
                return;
            }

            const failures = (result.results || []).filter(r => !r.success);
            if (failures.length > 0) {
                alert('Some VMs failed to delete:\n' + failures.map(f => `${f.name}: ${f.error}`).join('\n'));
            }

            loadDeployments();
        } catch (err) {
            alert('Delete failed: ' + err.message);
            deleteBtn.textContent = 'Delete Selected';
            deleteBtn.disabled = false;
        }
    });
}
