/**
 * AllRelay Dashboard - Frontend Application
 * Handles UI interactions, API communication, and WebSocket real-time updates
 */

// API endpoints
const API = {
    phones: '/api/phones',
    scan: '/api/phones/scan',
    connect: '/api/connect',
    disconnect: '/api/disconnect',
    status: '/api/status',
    toggleStream: '/api/streams/toggle',
    metrics: '/api/streams/metrics'
};

// State management
const state = {
    phones: [],
    connected: false,
    currentPhone: null,
    streams: [
        { name: 'screen', port: 5000, active: false, icon: '📺', fps: 0, bitrate: 0, latency: 0, bytes: 0, frames: 0 },
        { name: 'camera', port: 5001, active: false, icon: '📷', fps: 0, bitrate: 0, latency: 0, bytes: 0, frames: 0 },
        { name: 'mic', port: 5002, active: false, icon: '🎤', fps: 0, bitrate: 0, latency: 0, bytes: 0, frames: 0 },
        { name: 'speaker', port: 5003, active: false, icon: '🔊', fps: 0, bitrate: 0, latency: 0, bytes: 0, frames: 0 },
        { name: 'control', port: 5004, active: false, icon: '🎮', fps: 0, bitrate: 0, latency: 0, bytes: 0, frames: 0 }
    ],
    ws: null,
    wsReconnectTimer: null
};

// DOM Elements
const elements = {};

// Initialize the application
function init() {
    // Cache DOM elements
    elements.connectionStatus = document.getElementById('connectionStatus');
    elements.statusDot = elements.connectionStatus.querySelector('.status-dot');
    elements.statusText = elements.connectionStatus.querySelector('.status-text');
    elements.phoneIP = document.getElementById('phoneIP');
    elements.connectBtn = document.getElementById('connectBtn');
    elements.scanBtn = document.getElementById('scanBtn');
    elements.phoneList = document.getElementById('phoneList');
    elements.streamsGrid = document.getElementById('streamsGrid');
    elements.enableAllBtn = document.getElementById('enableAllBtn');
    elements.disableAllBtn = document.getElementById('disableAllBtn');

    // Bind event listeners
    elements.connectBtn.addEventListener('click', handleConnect);
    elements.scanBtn.addEventListener('click', handleScan);
    elements.enableAllBtn.addEventListener('click', () => toggleAllStreams(true));
    elements.disableAllBtn.addEventListener('click', () => toggleAllStreams(false));

    // Stream toggle via event delegation (robust with dynamic content)
    elements.streamsGrid.addEventListener('change', (e) => {
        const toggle = e.target.closest('.stream-toggle-input');
        if (toggle) {
            const streamName = toggle.dataset.stream;
            handleToggleStream(streamName, toggle.checked);
        }
    });

    // Enter key on input
    elements.phoneIP.addEventListener('keypress', (e) => {
        if (e.key === 'Enter') handleConnect();
    });

    // Initial render
    renderStreams();
    renderPhoneList();

    // Load initial status
    loadStatus();

    // Connect WebSocket
    connectWebSocket();

    // Initialize screen viewer
    initScreenViewer();

    // Poll for status updates (fallback if WebSocket fails)
    setInterval(loadStatus, 10000);
}

// ============================================
// WebSocket Functions
// ============================================

function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/ws`;
    
    try {
        state.ws = new WebSocket(wsUrl);
        
        state.ws.onopen = () => {
            console.log('WebSocket connected');
            // Clear reconnect timer
            if (state.wsReconnectTimer) {
                clearTimeout(state.wsReconnectTimer);
                state.wsReconnectTimer = null;
            }
        };
        
        state.ws.onmessage = (event) => {
            if (event.data instanceof ArrayBuffer || event.data instanceof Blob) {
                handleBinaryMessage(event.data);
                return;
            }
            try {
                const msg = JSON.parse(event.data);
                handleWebSocketMessage(msg);
            } catch (error) {
                console.error('Failed to parse WebSocket message:', error);
            }
        };

        state.ws.binaryType = 'arraybuffer';
        
        state.ws.onclose = () => {
            console.log('WebSocket disconnected');
            // Attempt to reconnect after 3 seconds
            state.wsReconnectTimer = setTimeout(connectWebSocket, 3000);
        };
        
        state.ws.onerror = (error) => {
            console.error('WebSocket error:', error);
        };
    } catch (error) {
        console.error('Failed to create WebSocket:', error);
        // Fallback to polling
    }
}

function handleWebSocketMessage(msg) {
    switch (msg.type) {
        case 'status':
            updateConnectionStatus(msg.data);
            break;
            
        case 'stream_update':
            updateStreamFromWS(msg.data);
            break;
            
        case 'pong':
            // Heartbeat response
            break;

        case 'control_ack':
            // Control message acknowledged
            break;
            
        default:
            console.log('Unknown WebSocket message type:', msg.type);
    }
}

function handleBinaryMessage(data) {
    // Binary frames are H.264 NAL units for screen streaming
    if (data instanceof Blob) {
        data.arrayBuffer().then(buf => handleBinaryFrame(new Uint8Array(buf)));
    } else {
        handleBinaryFrame(new Uint8Array(data));
    }
}

function updateStreamFromWS(streamData) {
    const stream = state.streams.find(s => s.name === streamData.name);
    if (stream) {
        Object.assign(stream, streamData);
        renderStreams();
        updateStatusDisplay();

        // Show/hide screen viewer
        if (stream.name === 'screen') {
            showScreenViewer(stream.active && state.connected);
        }
    }
}

function sendWebSocketMessage(type, data = {}) {
    if (state.ws && state.ws.readyState === WebSocket.OPEN) {
        state.ws.send(JSON.stringify({ type, data }));
    }
}

// ============================================
// API Functions
// ============================================

async function apiCall(endpoint, method = 'GET', data = null) {
    const options = {
        method,
        headers: {
            'Content-Type': 'application/json'
        }
    };

    if (data) {
        options.body = JSON.stringify(data);
    }

    try {
        const response = await fetch(endpoint, options);
        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }
        return await response.json();
    } catch (error) {
        console.error('API call failed:', error);
        throw error;
    }
}

async function loadStatus() {
    try {
        const status = await apiCall(API.status);
        updateConnectionStatus(status);
    } catch (error) {
        console.error('Failed to load status:', error);
    }
}

async function handleConnect() {
    const ip = elements.phoneIP.value.trim();
    
    if (!ip) {
        showError('Please enter a phone IP address');
        return;
    }

    // Validate IP format
    const ipRegex = /^(\d{1,3}\.){3}\d{1,3}$/;
    if (!ipRegex.test(ip)) {
        showError('Invalid IP address format');
        return;
    }

    elements.connectBtn.disabled = true;
    elements.connectBtn.textContent = 'Connecting...';

    try {
        await apiCall(API.connect, 'POST', { ip, port: 5000 });
        state.connected = true;
        state.currentPhone = { ip, name: `Phone (${ip})` };
        updateConnectionUI();
        showSuccess(`Connected to ${ip}`);
    } catch (error) {
        showError('Failed to connect. Is the phone running AllRelay server?');
    } finally {
        elements.connectBtn.disabled = false;
        elements.connectBtn.textContent = 'Connect';
    }
}

async function handleDisconnect() {
    try {
        await apiCall(API.disconnect, 'POST');
        state.connected = false;
        state.currentPhone = null;
        updateConnectionUI();
        showSuccess('Disconnected');
    } catch (error) {
        showError('Failed to disconnect');
    }
}

async function handleScan() {
    elements.scanBtn.disabled = true;
    elements.scanBtn.innerHTML = '<span class="loading">Scanning...</span>';

    try {
        const phones = await apiCall(API.scan);
        state.phones = phones;
        renderPhoneList();
        
        if (phones.length === 0) {
            showInfo('No phones found. Make sure AllRelay server is running on your phone.');
        } else {
            showSuccess(`Found ${phones.length} phone(s)`);
        }
    } catch (error) {
        showError('Failed to scan network');
    } finally {
        elements.scanBtn.disabled = false;
        elements.scanBtn.innerHTML = `
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <circle cx="11" cy="11" r="8"/>
                <path d="M21 21l-4.35-4.35"/>
            </svg>
            Scan Network
        `;
    }
}

async function handleToggleStream(streamName, active) {
    try {
        await apiCall(API.toggleStream, 'POST', { stream: streamName, active });
        
        // Update local state
        const stream = state.streams.find(s => s.name === streamName);
        if (stream) {
            stream.active = active;
        }
        
        renderStreams();
        updateStatusDisplay();
        
        showSuccess(`${streamName} ${active ? 'enabled' : 'disabled'}`);
    } catch (error) {
        showError(`Failed to toggle ${streamName}`);
        // Revert UI on error
        renderStreams();
    }
}

function toggleAllStreams(active) {
    state.streams.forEach(stream => {
        handleToggleStream(stream.name, active);
    });
}

// ============================================
// UI Rendering
// ============================================

function renderStreams() {
    elements.streamsGrid.innerHTML = state.streams.map(stream => `
        <div class="stream-card ${stream.active ? 'active' : 'inactive'}" data-stream="${stream.name}">
            <div class="stream-header">
                <div class="stream-icon">
                    <span>${stream.icon}</span>
                </div>
                <label class="stream-toggle">
                    <input type="checkbox" class="stream-toggle-input" 
                           data-stream="${stream.name}"
                           ${stream.active ? 'checked' : ''}>
                    <span class="toggle-slider"></span>
                </label>
            </div>
            <div class="stream-name">${stream.name.charAt(0).toUpperCase() + stream.name.slice(1)}</div>
            <div class="stream-port">Port ${stream.port}</div>
            ${stream.active ? renderStreamMetrics(stream) : ''}
        </div>
    `).join('');
}

function renderStreamMetrics(stream) {
    const metrics = [];
    
    if (stream.fps > 0) {
        metrics.push(`<div class="metric"><span class="metric-value">${stream.fps}</span><span class="metric-label">FPS</span></div>`);
    }
    
    if (stream.bitrate > 0) {
        let bitrateStr;
        if (stream.bitrate >= 1_000_000) {
            bitrateStr = `${(stream.bitrate / 1_000_000).toFixed(1)} Mbps`;
        } else if (stream.bitrate >= 1_000) {
            bitrateStr = `${(stream.bitrate / 1_000).toFixed(1)} kbps`;
        } else {
            bitrateStr = `${stream.bitrate} bps`;
        }
        metrics.push(`<div class="metric"><span class="metric-value">${bitrateStr}</span><span class="metric-label">Bitrate</span></div>`);
    }
    
    if (stream.latency > 0) {
        metrics.push(`<div class="metric"><span class="metric-value">${stream.latency}ms</span><span class="metric-label">Latency</span></div>`);
    }
    
    if (metrics.length === 0) {
        return '<div class="stream-metrics"><div class="metric"><span class="metric-value">Active</span></div></div>';
    }
    
    return `<div class="stream-metrics">${metrics.join('')}</div>`;
}

function renderPhoneList() {
    if (state.phones.length === 0) {
        elements.phoneList.innerHTML = `
            <div class="empty-state">
                <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                    <rect x="5" y="2" width="14" height="20" rx="2" ry="2"/>
                    <line x1="12" y1="18" x2="12.01" y2="18"/>
                </svg>
                <p>No phones discovered yet</p>
                <p class="text-muted">Click "Scan Network" to find phones on your network</p>
            </div>
        `;
        return;
    }

    elements.phoneList.innerHTML = state.phones.map(phone => `
        <div class="phone-item ${phone.connected ? 'connected' : ''}" data-phone-id="${phone.id}">
            <div class="phone-info">
                <div class="phone-name">${escapeHtml(phone.name)}</div>
                <div class="phone-ip">${escapeHtml(phone.ip)}</div>
            </div>
            <div class="phone-actions">
                <button class="btn btn-sm ${phone.connected ? 'btn-danger' : 'btn-primary'}" 
                        onclick="${phone.connected ? 'handleDisconnect()' : `connectToPhone('${phone.ip}')`}">
                    ${phone.connected ? 'Disconnect' : 'Connect'}
                </button>
            </div>
        </div>
    `).join('');
}

function updateConnectionUI() {
    // Update status indicator
    elements.statusDot.className = `status-dot ${state.connected ? 'connected' : 'disconnected'}`;
    elements.statusText.textContent = state.connected ? 'Connected' : 'Disconnected';

    // Update connect button
    if (state.connected) {
        elements.connectBtn.textContent = 'Disconnect';
        elements.connectBtn.className = 'btn btn-danger';
        elements.connectBtn.onclick = handleDisconnect;
        elements.phoneIP.disabled = true;
    } else {
        elements.connectBtn.textContent = 'Connect';
        elements.connectBtn.className = 'btn btn-primary';
        elements.connectBtn.onclick = handleConnect;
        elements.phoneIP.disabled = false;
        elements.phoneIP.value = '';
    }

    // Re-render phone list to update button states
    renderPhoneList();
}

function updateConnectionStatus(status) {
    if (status.connected !== state.connected) {
        state.connected = status.connected;
        state.currentPhone = status.phone;
        updateConnectionUI();
    }

    if (status.streams) {
        status.streams.forEach(s => {
            const stream = state.streams.find(st => st.name === s.name);
            if (stream) {
                Object.assign(stream, s);
            }
        });
        renderStreams();
    }

    updateStatusDisplay();
}

function updateStatusDisplay() {
    const statusCards = {
        screen: { value: 'screenStatus', meta: 'screenMeta' },
        camera: { value: 'cameraStatus', meta: 'cameraMeta' },
        mic: { value: 'micStatus', meta: 'micMeta' },
        speaker: { value: 'speakerStatus', meta: 'speakerMeta' }
    };

    state.streams.forEach(stream => {
        const card = statusCards[stream.name];
        if (card) {
            const valueEl = document.getElementById(card.value);
            const metaEl = document.getElementById(card.meta);
            
            if (valueEl) {
                valueEl.textContent = stream.active ? 'Active' : 'Inactive';
                valueEl.className = `status-value ${stream.active ? 'active' : 'inactive'}`;
            }
            
            if (metaEl && stream.active) {
                if (stream.fps > 0) {
                    metaEl.textContent = `${stream.fps} FPS`;
                } else if (stream.bitrate > 0) {
                    metaEl.textContent = `${stream.bitrate} kbps`;
                } else {
                    metaEl.textContent = 'Streaming';
                }
            } else if (metaEl) {
                metaEl.textContent = '--';
            }
        }
    });
}

// ============================================
// Utility Functions
// ============================================

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

function showSuccess(message) {
    showToast(message, 'success');
}

function showError(message) {
    showToast(message, 'error');
}

function showInfo(message) {
    showToast(message, 'info');
}

function showToast(message, type = 'info') {
    // Create toast element
    const toast = document.createElement('div');
    toast.className = `toast toast-${type}`;
    toast.textContent = message;
    
    // Style the toast
    Object.assign(toast.style, {
        position: 'fixed',
        bottom: '20px',
        right: '20px',
        padding: '12px 24px',
        background: type === 'success' ? 'var(--color-success)' : 
                   type === 'error' ? 'var(--color-danger)' : 'var(--color-primary)',
        color: 'white',
        borderRadius: 'var(--radius-md)',
        boxShadow: 'var(--shadow-lg)',
        zIndex: '1000',
        fontSize: 'var(--font-size-sm)',
        fontFamily: 'var(--font-family)',
        transition: 'opacity 0.3s, transform 0.3s',
        opacity: '0',
        transform: 'translateY(20px)'
    });
    
    document.body.appendChild(toast);
    
    // Animate in
    requestAnimationFrame(() => {
        toast.style.opacity = '1';
        toast.style.transform = 'translateY(0)';
    });
    
    // Remove after 3 seconds
    setTimeout(() => {
        toast.style.opacity = '0';
        toast.style.transform = 'translateY(20px)';
        setTimeout(() => toast.remove(), 300);
    }, 3000);
}

function connectToPhone(ip) {
    elements.phoneIP.value = ip;
    handleConnect();
}

// ============================================
// Initialize on DOM ready
// ============================================
document.addEventListener('DOMContentLoaded', init);

// ============================================
// Screen Viewer (WebCodecs)
// ============================================

let screenDecoder = null;
let screenCanvasEl = null;
let screenCtx = null;
let screenFrameCount = 0;
let screenLastFpsTime = Date.now();
let screenFpsCounter = 0;
let screenActive = false;
let screenConfigured = false;
let screenConfig = { sps: [], pps: [] };

function initScreenViewer() {
    screenCanvasEl = document.getElementById('screenCanvas');
    screenCtx = screenCanvasEl.getContext('2d');

    // Mouse events for control
    screenCanvasEl.addEventListener('mousedown', (e) => {
        if (!screenActive) return;
        const rect = screenCanvasEl.getBoundingClientRect();
        sendControlMessage({ type: 'touch', action: 'down', x: (e.clientX - rect.left) / rect.width, y: (e.clientY - rect.top) / rect.height, pointer_id: 0 });
    });
    screenCanvasEl.addEventListener('mouseup', (e) => {
        if (!screenActive) return;
        const rect = screenCanvasEl.getBoundingClientRect();
        sendControlMessage({ type: 'touch', action: 'up', x: (e.clientX - rect.left) / rect.width, y: (e.clientY - rect.top) / rect.height, pointer_id: 0 });
    });
    screenCanvasEl.addEventListener('mousemove', (e) => {
        if (!screenActive || !e.buttons) return;
        const rect = screenCanvasEl.getBoundingClientRect();
        sendControlMessage({ type: 'touch', action: 'move', x: (e.clientX - rect.left) / rect.width, y: (e.clientY - rect.top) / rect.height, pointer_id: 0 });
    });

    // Keyboard events
    document.addEventListener('keydown', (e) => {
        if (!screenActive) return;
        sendControlMessage({ type: 'key', action: 'down', keycode: e.keyCode, meta_state: 0 });
    });
    document.addEventListener('keyup', (e) => {
        if (!screenActive) return;
        sendControlMessage({ type: 'key', action: 'up', keycode: e.keyCode, meta_state: 0 });
    });
}

function sendControlMessage(data) {
    if (state.ws && state.ws.readyState === WebSocket.OPEN) {
        state.ws.send(JSON.stringify({ type: 'control', data }));
    }
}

function ensureScreenDecoder() {
    if (screenDecoder) return screenDecoder;

    screenDecoder = new VideoDecoder({
        output(frame) {
            screenFrameCount++;
            screenFpsCounter++;

            if (screenCanvasEl.width !== frame.displayWidth ||
                screenCanvasEl.height !== frame.displayHeight) {
                screenCanvasEl.width = frame.displayWidth;
                screenCanvasEl.height = frame.displayHeight;
            }

            screenCtx.drawImage(frame, 0, 0);
            frame.close();

            if (!screenCanvasEl.classList.contains('active')) {
                screenCanvasEl.classList.add('active');
                document.getElementById('screenPlaceholder').classList.add('hidden');
            }

            const now = Date.now();
            if (now - screenLastFpsTime >= 1000) {
                document.getElementById('screenFPS').textContent = screenFpsCounter + ' FPS';
                screenFpsCounter = 0;
                screenLastFpsTime = now;
            }
        },
        error(err) {
            console.error('Screen decoder error:', err);
        }
    });

    return screenDecoder;
}

function configureScreenDecoderFromConfig() {
    if (screenConfigured) return true;
    if (!screenConfig.sps.length || !screenConfig.pps.length) return false;

    const sps = screenConfig.sps[0];
    const pps = screenConfig.pps[0];
    if (sps.length < 4) return false;

    const codec = `avc1.${toHex2(sps[1])}${toHex2(sps[2])}${toHex2(sps[3])}`;
    const description = buildAvcC(screenConfig.sps, screenConfig.pps);
    const decoder = ensureScreenDecoder();
    decoder.configure({
        codec,
        description,
        optimizeForLatency: true,
    });
    screenConfigured = true;
    console.log('Screen decoder configured', { codec, sps: screenConfig.sps.length, pps: screenConfig.pps.length });
    return true;
}

function destroyScreenDecoder() {
    if (screenDecoder) {
        try { screenDecoder.close(); } catch (e) {}
        screenDecoder = null;
    }
    screenConfigured = false;
    screenConfig = { sps: [], pps: [] };
    screenFrameCount = 0;
    screenFpsCounter = 0;
    if (screenCanvasEl) {
        screenCanvasEl.classList.remove('active');
    }
    const placeholder = document.getElementById('screenPlaceholder');
    if (placeholder) placeholder.classList.remove('hidden');
    document.getElementById('screenFPS').textContent = '';
}

// Handle binary WebSocket frames for screen.
// Format: [1 byte flags][Annex B H.264 access unit bytes]
// flags bit0=config, bit1=keyframe
function handleBinaryFrame(data) {
    if (!screenActive || !data || data.length < 2) return;

    try {
        const flags = data[0];
        const payload = data.slice(1);
        const isConfig = (flags & 0x01) !== 0;
        const isKey = (flags & 0x02) !== 0;

        const nalUnits = extractAnnexBNALUnits(payload);
        if (!nalUnits.length) return;

        collectScreenConfig(nalUnits);
        if (!screenConfigured) {
            configureScreenDecoderFromConfig();
        }

        if (isConfig) {
            return;
        }
        if (!screenConfigured) {
            console.warn('Screen frame dropped: decoder not configured yet');
            return;
        }

        const chunkData = annexBAccessUnitToAVCC(payload);
        const chunkType = isKey || containsIDR(nalUnits) ? 'key' : 'delta';
        ensureScreenDecoder().decode(new EncodedVideoChunk({
            type: chunkType,
            timestamp: performance.now() * 1000,
            duration: 0,
            data: chunkData,
        }));
    } catch (err) {
        console.error('Screen decode error:', err);
    }
}

function extractAnnexBNALUnits(data) {
    const view = data instanceof Uint8Array ? data : new Uint8Array(data);
    const starts = [];

    for (let i = 0; i < view.length - 3; i++) {
        if (view[i] === 0x00 && view[i + 1] === 0x00) {
            if (view[i + 2] === 0x01) {
                starts.push({ index: i, len: 3 });
                i += 2;
            } else if (view[i + 2] === 0x00 && view[i + 3] === 0x01) {
                starts.push({ index: i, len: 4 });
                i += 3;
            }
        }
    }

    if (!starts.length) {
        return [];
    }

    const nalUnits = [];
    for (let i = 0; i < starts.length; i++) {
        const start = starts[i].index + starts[i].len;
        const end = i + 1 < starts.length ? starts[i + 1].index : view.length;
        if (end > start) {
            nalUnits.push(view.slice(start, end));
        }
    }
    return nalUnits;
}

function annexBAccessUnitToAVCC(data) {
    const nalUnits = extractAnnexBNALUnits(data);
    let total = 0;
    for (const nal of nalUnits) {
        total += 4 + nal.length;
    }
    const out = new Uint8Array(total);
    let offset = 0;
    for (const nal of nalUnits) {
        const len = nal.length;
        out[offset + 0] = (len >>> 24) & 0xFF;
        out[offset + 1] = (len >>> 16) & 0xFF;
        out[offset + 2] = (len >>> 8) & 0xFF;
        out[offset + 3] = len & 0xFF;
        out.set(nal, offset + 4);
        offset += 4 + len;
    }
    return out;
}

function collectScreenConfig(nalUnits) {
    for (const nal of nalUnits) {
        if (!nal.length) continue;
        const nalType = nal[0] & 0x1F;
        if (nalType === 7) {
            if (!screenConfig.sps.some(existing => byteArrayEquals(existing, nal))) {
                screenConfig.sps.push(nal);
            }
        } else if (nalType === 8) {
            if (!screenConfig.pps.some(existing => byteArrayEquals(existing, nal))) {
                screenConfig.pps.push(nal);
            }
        }
    }
}

function containsIDR(nalUnits) {
    return nalUnits.some(nal => nal.length && ((nal[0] & 0x1F) === 5));
}

function buildAvcC(spsList, ppsList) {
    const sps = spsList[0];
    const pps = ppsList[0];
    const size = 11 + sps.length + pps.length;
    const out = new Uint8Array(size);
    let o = 0;
    out[o++] = 1;
    out[o++] = sps[1];
    out[o++] = sps[2];
    out[o++] = sps[3];
    out[o++] = 0xFF;
    out[o++] = 0xE0 | 1;
    out[o++] = (sps.length >>> 8) & 0xFF;
    out[o++] = sps.length & 0xFF;
    out.set(sps, o);
    o += sps.length;
    out[o++] = 1;
    out[o++] = (pps.length >>> 8) & 0xFF;
    out[o++] = pps.length & 0xFF;
    out.set(pps, o);
    return out;
}

function toHex2(value) {
    return value.toString(16).padStart(2, '0');
}

function byteArrayEquals(a, b) {
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) {
        if (a[i] !== b[i]) return false;
    }
    return true;
}

function showScreenViewer(show) {
    screenActive = show;
    const panel = document.getElementById('screenPanel');
    if (panel) panel.style.display = show ? '' : 'none';
    if (!show) destroyScreenDecoder();
}
