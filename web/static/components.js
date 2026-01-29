// Versa HeadEnd Deployer - Component table and mode handling

'use strict';

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
    saveState();
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
        saveState();
    }));
    tbody.querySelectorAll('.comp-count').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].count = +e.target.value;
        updateSummary();
        initNetworkConfig();
        renderNetworkConfig();
        saveState();
    }));
    tbody.querySelectorAll('.comp-cpu').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].cpu = +e.target.value;
        updateSummary();
        saveState();
    }));
    tbody.querySelectorAll('.comp-ram').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].ram = +e.target.value;
        updateSummary();
        saveState();
    }));
    tbody.querySelectorAll('.comp-disk').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].disk = +e.target.value;
        updateSummary();
        saveState();
    }));
    tbody.querySelectorAll('.comp-node').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].node = e.target.value;
        saveState();
    }));
    tbody.querySelectorAll('.comp-iso').forEach(el => el.addEventListener('change', (e) => {
        state.components[+e.target.dataset.idx].iso = e.target.value;
        saveState();
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
