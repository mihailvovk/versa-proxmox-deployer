// Versa HeadEnd Deployer - Image sources management

'use strict';

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
    srcs.forEach((src, idx) => {
        const item = document.createElement('div');
        item.className = 'source-item';
        item.innerHTML = `
            <span class="source-type">${esc(src.Type || 'auto')}</span>
            <span class="source-name">${esc(src.Name || '')}</span>
            <span class="source-url" title="${esc(src.URL)}">${esc(src.URL)}</span>
        `;
        const removeBtn = document.createElement('button');
        removeBtn.className = 'btn-remove';
        removeBtn.textContent = 'Remove';
        removeBtn.addEventListener('click', async () => {
            const label = src.Name || src.URL;
            if (!confirm('Remove source: ' + label + '?')) return;
            removeBtn.disabled = true;
            removeBtn.textContent = 'Removing...';
            const result = await api('DELETE', '/api/sources', { url: src.URL, index: idx });
            if (result.sources) {
                state.configSources = result.sources;
            }
            renderSourcesList();
        });
        item.appendChild(removeBtn);
        container.appendChild(item);
    });
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
        urlInput.placeholder = 'https://... or s3://bucket/prefix or sftp://user@host/path';
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
