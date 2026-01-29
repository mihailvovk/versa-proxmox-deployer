// Versa HeadEnd Deployer - Network configuration, topology diagram, bridge assignments

'use strict';

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

    // Interface order per component type
    if (!state.networkConfig.interfaceOrder) {
        state.networkConfig.interfaceOrder = {};
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
    const mgmtBus = tiers[0];
    const mgmtBridge = nc.northbound || '';
    for (let g = 0; g < groupCount; g++) {
        const gx = groupXArr[g];
        tiers.forEach((tier, ti) => {
            if (tier.kind !== 'vms') return;
            if (ti === 1 && tiers[0].kind === 'bus') return;

            const boxes = getGroupBoxes(tier, g);
            const positions = positionBoxes(boxes, gx, groupW, VM_W);

            positions.forEach((box, bi) => {
                const startX = box.x + VM_W - 5 - bi * 3;
                const turnY = tier.y - Math.floor(GAP / 2);
                const spineX = W - PAD + 6 + g * 8 + bi * 6;
                const busBottom = mgmtBus.y + mgmtBus.height;

                svg += '<path d="M' + startX + ',' + tier.y + ' V' + turnY + ' H' + spineX + ' V' + busBottom + '" fill="none" stroke="#5eadff" stroke-width="1.5" stroke-dasharray="4,3" opacity="0.6"/>';
                svg += '<circle cx="' + spineX + '" cy="' + busBottom + '" r="2.5" fill="#5eadff" opacity="0.8"/>';
                svg += '<text x="' + (box.x + VM_W + 3) + '" y="' + (tier.y - 2) + '" fill="#7dc4ff" font-size="8" font-family="SF Mono,Menlo,monospace" text-anchor="start" opacity="0.85">' + escSvg(mgmtBridge) + '</text>';
            });
        });
    }

    // --- Regular connector lines ---
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

    // --- Extra interface connectors ---
    for (let g = 0; g < groupCount; g++) {
        tiers.forEach((tier, ti) => {
            if (tier.kind !== 'vms') return;

            const tierTypes = new Set();
            tier.components.forEach(c => tierTypes.add(c.type));

            const busAboveIdx = (ti > 0 && tiers[ti - 1].kind === 'bus') ? ti - 1 : -1;
            const busBelowIdx = (ti < tiers.length - 1 && tiers[ti + 1].kind === 'bus') ? ti + 1 : -1;

            tiers.forEach((busTier, bi) => {
                if (busTier.kind !== 'bus') return;
                if (!busTier.ownerType || !tierTypes.has(busTier.ownerType)) return;
                if (bi === busAboveIdx || bi === busBelowIdx) return;

                const boxes = getGroupBoxes(tier, g);
                const positions = positionBoxes(boxes, groupXArr[g], groupW, VM_W);

                positions.forEach((box, bxi) => {
                    if (box.type !== busTier.ownerType) return;
                    const offset = 6 + bxi * 3;
                    const lineX = box.cx + offset;

                    if (bi > ti) {
                        svg += '<line x1="' + lineX + '" y1="' + (tier.y + VM_H) + '" x2="' + lineX + '" y2="' + busTier.y + '" stroke="#505878" stroke-width="1.5"/>';
                    } else {
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
            svg += '<rect x="' + PAD + '" y="' + tier.y + '" width="' + CW + '" height="' + BUS_H + '" rx="3" fill="#2a2f4a" stroke="#3a4068" stroke-width="0.5"/>';
            svg += '<text x="' + (PAD + 8) + '" y="' + (tier.y + BUS_H / 2 + 1) + '" fill="#a0a4be" font-size="10" font-weight="500" font-family="-apple-system,BlinkMacSystemFont,sans-serif" dominant-baseline="middle">' + escSvg(tier.label) + '</text>';
            if (tier.bridge) {
                svg += '<text x="' + (PAD + CW - 8) + '" y="' + (tier.y + BUS_H / 2 + 1) + '" fill="#7dc4ff" font-size="10" font-family="SF Mono,Menlo,monospace" dominant-baseline="middle" text-anchor="end">' + escSvg(tier.bridge) + '</text>';
            }
        } else {
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

    if (allowNone) {
        html += `<option value="" ${!selectedValue ? 'selected' : ''}>— none —</option>`;
    }

    if (isAutoCreate) {
        html += `<option value="${esc(selectedValue)}" class="new-bridge-opt" selected>New: ${esc(selectedValue)}</option>`;
    }

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

    rows.push({
        label: 'Management (Northbound)',
        field: 'northbound',
        value: nc.northbound,
        desc: 'All instances',
        optional: false,
    });

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

    if (nc.controllerRouter) {
        rows.push({
            label: 'Controller-Router Link',
            field: 'controllerRouter',
            value: nc.controllerRouter,
            desc: 'Controller, Router',
            optional: false,
        });
    }

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

        if (nc.controllerWANs.length < 3) {
            rows.push({ addWAN: true });
        }
    }

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
            saveState();
        });
    });

    // Add WAN button
    const addWanBtn = container.querySelector('#add-wan-btn');
    if (addWanBtn) {
        addWanBtn.addEventListener('click', () => {
            const firstBridge = getExistingBridges()[0] || 'vmbr0';
            state.networkConfig.controllerWANs.push(firstBridge);
            renderNetworkConfig();
            saveState();
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
            saveState();
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
            saveState();
        });
    });
}

function expandInlineCreate(selectEl, field) {
    const row = selectEl.closest('.network-row');
    const nextNum = getNextBridgeNum();
    const proposedName = `vmbr${nextNum}`;

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

        state.networkConfig.autoCreate.add(bridgeName);
        setNetworkFieldValue(field, bridgeName);
        renderNetworkConfig();
        saveState();
    });

    wrapper.querySelector('.inline-cancel').addEventListener('click', () => {
        renderNetworkConfig();
    });

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

                let arrowsHtml = '';
                if (interfaces.length > 1) {
                    const canUp = iface.moveIdx > 0;
                    const canDown = iface.moveIdx < iface.moveMax;
                    const dataAttrs = `data-move-comp="${esc(iface.moveCompType || comp.type)}" data-move-idx="${iface.moveIdx}"`;
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
            saveState();
        });
    });

    // Generic add interface button
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
            saveState();
        });
    });

    // Move up/down buttons in instance preview — reorders all interfaces
    container.querySelectorAll('.btn-move-preview').forEach(btn => {
        btn.addEventListener('click', () => {
            const compType = btn.dataset.moveComp;
            const idx = parseInt(btn.dataset.moveIdx);
            const dir = btn.dataset.dir;
            const swapIdx = dir === 'up' ? idx - 1 : idx + 1;

            // Get current resolved order as IDs
            const interfaces = resolveInterfaces(compType);
            if (swapIdx < 0 || swapIdx >= interfaces.length) return;

            const order = interfaces.map(i => i.id);
            [order[idx], order[swapIdx]] = [order[swapIdx], order[idx]];
            state.networkConfig.interfaceOrder[compType] = order;

            renderNetworkConfig();
            saveState();
        });
    });
}

function resolveInterfaces(compType) {
    const nc = state.networkConfig;
    const baseDefs = INTERFACE_DEFS[compType] || [];

    // Build all interfaces with stable IDs
    const all = [];

    baseDefs.forEach((def, i) => {
        const bridge = nc[def.field] || '';
        if (!def.required && !bridge) return;
        all.push({
            id: `base:${i}`,
            label: def.label,
            bridge: bridge,
        });
    });

    if (compType === 'controller') {
        nc.controllerWANs.forEach((bridge, i) => {
            all.push({
                id: `wan:${i}`,
                label: `WAN ${i + 1}`,
                bridge: bridge,
            });
        });
    }

    const extras = (nc.extraInterfaces || {})[compType] || [];
    extras.forEach((iface, i) => {
        all.push({
            id: `extra:${i}`,
            label: iface.label,
            bridge: iface.bridge,
        });
    });

    // Apply stored order if it exists
    const order = nc.interfaceOrder[compType];
    let ordered;
    if (order && order.length > 0) {
        const byId = {};
        all.forEach(iface => { byId[iface.id] = iface; });
        // Ordered items first, then any new items not yet in the order
        ordered = [];
        order.forEach(id => {
            if (byId[id]) {
                ordered.push(byId[id]);
                delete byId[id];
            }
        });
        // Append any remaining (newly added interfaces)
        all.forEach(iface => {
            if (byId[iface.id]) {
                ordered.push(iface);
            }
        });
    } else {
        ordered = all;
    }

    // Assign eth numbers and move metadata
    return ordered.map((iface, i) => ({
        ...iface,
        eth: i,
        moveIdx: i,
        moveMax: ordered.length - 1,
        moveCompType: compType,
    }));
}

function buildNetworkPayload() {
    const nc = state.networkConfig;
    const extras = nc.extraInterfaces || {};

    const analyticsExtras = extras.analytics || [];
    const routerExtras = extras.router || [];

    return {
        NorthboundBridge: nc.northbound,
        DirectorRouterBridge: nc.directorRouter,
        ControllerRouterBridge: nc.controllerRouter,
        ControllerWANBridges: nc.controllerWANs.length > 0 ? nc.controllerWANs : [],
        AnalyticsClusterBridge: analyticsExtras.length > 0 ? analyticsExtras[0].bridge : '',
        RouterHABridge: routerExtras.length > 0 ? routerExtras[0].bridge : '',
        InterfaceOrder: nc.interfaceOrder || {},
    };
}
