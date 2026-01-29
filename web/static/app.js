// Versa HeadEnd Deployer - Core state, constants, init, and utilities

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
        interfaceOrder: {},      // compType -> [id, id, ...] for reordering all interfaces
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

// --- State Persistence ---
const STORAGE_KEY = 'versa-deployer-state';

function saveState() {
    const toSave = {
        mode: state.mode,
        singleComponent: state.singleComponent,
        components: state.components,
        networkConfig: {
            northbound: state.networkConfig.northbound,
            directorRouter: state.networkConfig.directorRouter,
            controllerRouter: state.networkConfig.controllerRouter,
            controllerWANs: state.networkConfig.controllerWANs,
            extraInterfaces: state.networkConfig.extraInterfaces,
            interfaceOrder: state.networkConfig.interfaceOrder,
            autoCreate: Array.from(state.networkConfig.autoCreate),
        },
        host: document.getElementById('host').value,
        prefix: document.getElementById('deploy-prefix').value,
    };
    localStorage.setItem(STORAGE_KEY, JSON.stringify(toSave));
}

function clearSavedState() {
    localStorage.removeItem(STORAGE_KEY);
}

function loadSavedState() {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    try {
        const parsed = JSON.parse(raw);
        // Restore autoCreate from array back to Set
        if (parsed.networkConfig && Array.isArray(parsed.networkConfig.autoCreate)) {
            parsed.networkConfig.autoCreate = new Set(parsed.networkConfig.autoCreate);
        }
        return parsed;
    } catch {
        return null;
    }
}

// --- Init ---
document.addEventListener('DOMContentLoaded', async () => {
    setupEventListeners();
    generatePrefix();
    await loadConfig();
    await tryAutoReconnect();
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
            saveState();
        });
    });

    document.querySelectorAll('input[name="single-comp"]').forEach(radio => {
        radio.addEventListener('change', (e) => {
            state.singleComponent = e.target.value;
            rebuildComponents();
            saveState();
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
