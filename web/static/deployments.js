// Versa HeadEnd Deployer - Deployed instances management

'use strict';

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
            <th style="width:70px"></th>
        </tr></thead>
        <tbody>`;

    allVMs.forEach(vm => {
        const statusClass = vm.Status === 'running' ? 'running' : 'stopped';
        // Extract component type from tags
        const compTag = (vm.Tags || []).find(t => t.startsWith('versa-') && t !== 'versa-deployer' && !t.startsWith('versa-deploy-') && !t.startsWith('versa-ha-'));
        const compType = compTag ? compTag.replace('versa-', '') : '';

        const isRunning = (vm.Status || '').toLowerCase() === 'running';

        html += `<tr data-vmid="${vm.VMID}" data-prefix="${esc(vm.prefix)}" data-name="${esc(vm.Name)}">
            <td><input type="checkbox" class="deploy-vm-check" value="${vm.VMID}"></td>
            <td class="deploy-vmid">${vm.VMID}</td>
            <td>${esc(vm.Name)}</td>
            <td><span class="deployment-prefix-tag">${esc(vm.prefix)}</span></td>
            <td>${esc(compType)}</td>
            <td><span class="vm-status-badge ${statusClass}">${esc(vm.Status)}</span></td>
            <td>${isRunning ? `<button class="btn-console" onclick="openConsole(${vm.VMID}, '${esc(vm.Name).replace(/'/g, "\\'")}')">Console</button>` : ''}</td>
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
