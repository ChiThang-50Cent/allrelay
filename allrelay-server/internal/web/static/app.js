/**
 * AllRelay Dashboard - Frontend Application
 * Handles UI interactions, API communication, and WebSocket real-time updates
 */

const pageMode = document.body.dataset.page || 'dashboard';

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
        { name: 'speaker', port: 5003, active: false, icon: '🔊', fps: 0, bitrate: 0, latency: 0, bytes: 0, frames: 0 }
    ],
    ws: null,
    wsReconnectTimer: null,
    remotePowerOffAutoTried: false,
    remotePowerOffSent: false,
    remotePopup: null,
    remotePopupCloseHandled: false,
};

let screenConfigKey = '';
let screenPendingSessionSize = null;
let screenLastPopupFitKey = '';

// DOM Elements
const elements = {};

// Initialize the application
function init() {
    // Cache shared DOM elements
    elements.connectionStatus = document.getElementById('connectionStatus');
    elements.statusDot = elements.connectionStatus?.querySelector('.status-dot') || null;
    elements.statusText = elements.connectionStatus?.querySelector('.status-text') || null;

    // Dashboard-only elements
    elements.phoneIP = document.getElementById('phoneIP');
    elements.connectBtn = document.getElementById('connectBtn');
    elements.scanBtn = document.getElementById('scanBtn');
    elements.phoneList = document.getElementById('phoneList');
    elements.streamsGrid = document.getElementById('streamsGrid');
    elements.enableAllBtn = document.getElementById('enableAllBtn');
    elements.disableAllBtn = document.getElementById('disableAllBtn');

    // Remote-only elements
    elements.remoteScreenToggleBtn = document.getElementById('remoteScreenToggleBtn');
    elements.remoteWakeBtn = document.getElementById('remoteWakeBtn');
    elements.remotePhoneStatus = document.getElementById('remotePhoneStatus');
    elements.remotePowerStatus = document.getElementById('remotePowerStatus');

    // Dashboard event listeners
    if (elements.connectBtn) {
        elements.connectBtn.addEventListener('click', handleConnect);
    }
    if (elements.scanBtn) {
        elements.scanBtn.addEventListener('click', handleScan);
    }
    if (elements.enableAllBtn) {
        elements.enableAllBtn.addEventListener('click', () => toggleAllStreams(true));
    }
    if (elements.disableAllBtn) {
        elements.disableAllBtn.addEventListener('click', () => toggleAllStreams(false));
    }
    if (elements.streamsGrid) {
        elements.streamsGrid.addEventListener('change', (e) => {
            const toggle = e.target.closest('.stream-toggle-input');
            if (toggle) {
                const streamName = toggle.dataset.stream;
                handleToggleStream(streamName, toggle.checked);
            }
        });
    }
    if (elements.phoneIP) {
        elements.phoneIP.addEventListener('keypress', (e) => {
            if (e.key === 'Enter') handleConnect();
        });
    }

    // Remote event listeners
    if (elements.remoteScreenToggleBtn) {
        elements.remoteScreenToggleBtn.addEventListener('click', toggleRemoteScreen);
    }
    if (elements.remoteWakeBtn) {
        elements.remoteWakeBtn.addEventListener('click', wakeRemotePhoneScreen);
    }
    if (pageMode === 'remote') {
        window.addEventListener('beforeunload', restoreRemotePhoneScreen);
        window.addEventListener('pagehide', restoreRemotePhoneScreen);
    }

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

    if (pageMode === 'dashboard') {
        setInterval(checkRemotePopupLifecycle, 1000);
    }
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
            maybeApplyRemoteMode();
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

        case 'screen_session':
            handleScreenSession(msg.data);
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
        updateRemoteUI();

        // Show/hide screen viewer
        if (stream.name === 'screen') {
            showScreenViewer(stream.active && state.connected);
            if (!stream.active) {
                screenPendingSessionSize = null;
            }
            maybeApplyRemoteMode();
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
    let popupOpened = false;
    if (pageMode === 'dashboard' && streamName === 'screen' && active) {
        popupOpened = openRemotePopup();
        if (!popupOpened) {
            showError('Popup blocked. Please allow popups for AllRelay.');
            renderStreams();
            return;
        }
    }

    try {
        await apiCall(API.toggleStream, 'POST', { stream: streamName, active });
        
        // Update local state
        const stream = state.streams.find(s => s.name === streamName);
        if (stream) {
            stream.active = active;
        }

        if (streamName === 'screen') {
            if (!active) {
                closeRemotePopup();
            } else if (pageMode === 'dashboard') {
                focusRemotePopup();
            }
        }
        
        renderStreams();
        updateStatusDisplay();
        updateRemoteUI();
        
        showSuccess(`${streamName} ${active ? 'enabled' : 'disabled'}`);
    } catch (error) {
        if (popupOpened) {
            closeRemotePopup();
        }
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
    if (!elements.streamsGrid) return;
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
            <div class="stream-port">${stream.name === 'screen' ? 'Opens popup remote window' : `Port ${stream.port}`}</div>
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
    if (!elements.phoneList) return;
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
    if (elements.statusDot) {
        elements.statusDot.className = `status-dot ${state.connected ? 'connected' : 'disconnected'}`;
    }
    if (elements.statusText) {
        elements.statusText.textContent = state.connected ? 'Connected' : 'Disconnected';
    }

    // Update connect button (dashboard only)
    if (elements.connectBtn && elements.phoneIP) {
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
    }

    // Re-render phone list to update button states
    renderPhoneList();
}

function updateConnectionStatus(status) {
    if (status.connected !== state.connected) {
        state.connected = status.connected;
        state.currentPhone = status.phone;
        if (!state.connected) {
            state.remotePowerOffAutoTried = false;
            state.remotePowerOffSent = false;
            closeRemotePopup();
        }
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

    const screen = getStream('screen');
    showScreenViewer(Boolean(screen?.active && state.connected));
    if (pageMode === 'dashboard') {
        if (screen?.active) {
            focusRemotePopup();
        } else {
            closeRemotePopup();
        }
    }
    updateStatusDisplay();
    updateRemoteUI();
    maybeApplyRemoteMode();
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

function getStream(name) {
    return state.streams.find(stream => stream.name === name) || null;
}

function openRemotePopup() {
    if (pageMode !== 'dashboard') return false;
    if (state.remotePopup && !state.remotePopup.closed) {
        focusRemotePopup();
        return true;
    }

    const width = 430;
    const height = 920;
    const left = Math.max(0, Math.round((window.screen.width - width) / 2));
    const top = Math.max(0, Math.round((window.screen.height - height) / 2));
    const features = [
        'popup=yes',
        'noopener=no',
        'resizable=yes',
        'scrollbars=no',
        'toolbar=no',
        'menubar=no',
        'location=no',
        'status=no',
        `width=${width}`,
        `height=${height}`,
        `left=${left}`,
        `top=${top}`,
    ].join(',');

    state.remotePopup = window.open('/remote', 'allrelay-remote', features);
    state.remotePopupCloseHandled = false;
    if (!state.remotePopup) {
        return false;
    }
    focusRemotePopup();
    return true;
}

function focusRemotePopup() {
    if (state.remotePopup && !state.remotePopup.closed) {
        state.remotePopup.focus();
    }
}

function closeRemotePopup() {
    if (state.remotePopup && !state.remotePopup.closed) {
        state.remotePopup.close();
    }
    state.remotePopup = null;
    state.remotePopupCloseHandled = false;
}

function checkRemotePopupLifecycle() {
    if (pageMode !== 'dashboard') return;
    const screen = getStream('screen');
    if (!state.remotePopup || !state.remotePopup.closed) {
        if (!screen?.active) {
            state.remotePopupCloseHandled = false;
        }
        return;
    }
    state.remotePopup = null;
    if (screen?.active && !state.remotePopupCloseHandled) {
        state.remotePopupCloseHandled = true;
        handleToggleStream('screen', false);
    }
}

function updateRemoteUI() {
    if (pageMode !== 'remote') return;
    const screen = getStream('screen');

    if (elements.remotePhoneStatus) {
        elements.remotePhoneStatus.textContent = state.connected
            ? (state.currentPhone?.ip || state.currentPhone?.name || 'Connected')
            : 'Disconnected';
        elements.remotePhoneStatus.className = `status-value remote-status-value ${state.connected ? 'active' : 'inactive'}`;
    }

    if (elements.remotePowerStatus) {
        if (!state.connected) {
            elements.remotePowerStatus.textContent = 'Connect from dashboard first';
        } else if (screen?.active) {
            elements.remotePowerStatus.textContent = state.remotePowerOffSent || state.remotePowerOffAutoTried
                ? 'Remote mode active · phone display off requested'
                : 'Remote mode ready';
        } else {
            elements.remotePowerStatus.textContent = 'Start the screen stream to control';
        }
    }

    if (elements.remoteScreenToggleBtn) {
        elements.remoteScreenToggleBtn.textContent = screen?.active ? 'Stop Screen' : 'Start Screen';
        elements.remoteScreenToggleBtn.disabled = !state.connected;
    }

    if (elements.remoteWakeBtn) {
        elements.remoteWakeBtn.disabled = !state.connected;
    }
}

async function toggleRemoteScreen() {
    const screen = getStream('screen');
    if (!screen || !state.connected) {
        showError('Connect from the dashboard first');
        return;
    }
    const nextActive = !screen.active;
    await handleToggleStream('screen', nextActive);
    if (!nextActive) {
        window.close();
    }
}

function maybeApplyRemoteMode() {
    if (pageMode !== 'remote') return;
    const screen = getStream('screen');
    if (!state.connected || !screen?.active) {
        state.remotePowerOffAutoTried = false;
        state.remotePowerOffSent = false;
        return;
    }
    if (!state.ws || state.ws.readyState !== WebSocket.OPEN) {
        return;
    }
    if (!state.remotePowerOffAutoTried) {
        sendControlPacket(buildSetDisplayPowerControlMessage(false), { markPowerOff: false });
        state.remotePowerOffAutoTried = true;
        if (elements.remotePowerStatus) {
            elements.remotePowerStatus.textContent = 'Requested phone display off for remote mode';
        }
    }
}

function wakeRemotePhoneScreen() {
    if (pageMode !== 'remote') return;
    sendControlPacket(buildSetDisplayPowerControlMessage(true), { force: true, wake: true });
    state.remotePowerOffAutoTried = false;
    state.remotePowerOffSent = false;
    if (elements.remotePowerStatus) {
        elements.remotePowerStatus.textContent = 'Requested phone display on';
    }
}

function restoreRemotePhoneScreen() {
    if (pageMode !== 'remote') return;
    const screen = getStream('screen');
    if (state.ws && state.ws.readyState === WebSocket.OPEN && (state.remotePowerOffAutoTried || state.remotePowerOffSent)) {
        sendControlPacket(buildSetDisplayPowerControlMessage(true), { force: true, wake: true });
    }
    if (screen?.active) {
        fetch(API.toggleStream, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ stream: 'screen', active: false }),
            keepalive: true,
        }).catch(() => {});
    }
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
let screenVideoSize = { width: 0, height: 0 };
let screenPointerDown = false;

function initScreenViewer() {
    screenCanvasEl = document.getElementById('screenCanvas');
    if (!screenCanvasEl) return;
    screenCtx = screenCanvasEl.getContext('2d');

    if (pageMode !== 'remote') {
        return;
    }

    screenCanvasEl.tabIndex = 0;

    const sendTouch = (action, e) => {
        if (!screenActive) return;
        const position = getScreenPointerPosition(e);
        if (!position) return;
        sendControlPacket(buildTouchControlMessage({
            action,
            pointerId: 0,
            x: position.x,
            y: position.y,
            screenWidth: position.screenWidth,
            screenHeight: position.screenHeight,
            pressure: action === ANDROID_MOTION_ACTION_UP ? 0 : 1,
            actionButton: ANDROID_BUTTON_PRIMARY,
            buttons: action === ANDROID_MOTION_ACTION_UP ? 0 : ANDROID_BUTTON_PRIMARY,
        }), { markPowerOff: true });
    };

    screenCanvasEl.addEventListener('mousedown', (e) => {
        if (!screenActive || e.button !== 0) return;
        e.preventDefault();
        screenCanvasEl.focus();
        screenPointerDown = true;
        sendTouch(ANDROID_MOTION_ACTION_DOWN, e);
    });
    screenCanvasEl.addEventListener('mouseup', (e) => {
        if (!screenActive || e.button !== 0) return;
        e.preventDefault();
        sendTouch(ANDROID_MOTION_ACTION_UP, e);
        screenPointerDown = false;
    });
    screenCanvasEl.addEventListener('mouseleave', (e) => {
        if (!screenActive || !screenPointerDown) return;
        sendTouch(ANDROID_MOTION_ACTION_UP, e);
        screenPointerDown = false;
    });
    screenCanvasEl.addEventListener('mousemove', (e) => {
        if (!screenActive || !screenPointerDown) return;
        e.preventDefault();
        sendTouch(ANDROID_MOTION_ACTION_MOVE, e);
    });

    document.addEventListener('keydown', (e) => {
        if (!screenActive) return;
        const keycode = mapBrowserKeyToAndroid(e);
        if (keycode == null) return;
        e.preventDefault();
        sendControlPacket(buildKeyControlMessage({
            action: ANDROID_KEY_ACTION_DOWN,
            keycode,
            repeat: e.repeat ? 1 : 0,
            metaState: 0,
        }), { markPowerOff: true });
    });
    document.addEventListener('keyup', (e) => {
        if (!screenActive) return;
        const keycode = mapBrowserKeyToAndroid(e);
        if (keycode == null) return;
        e.preventDefault();
        sendControlPacket(buildKeyControlMessage({
            action: ANDROID_KEY_ACTION_UP,
            keycode,
            repeat: 0,
            metaState: 0,
        }), { markPowerOff: true });
    });
}

function sendControlPacket(data, options = {}) {
    if (!(data instanceof Uint8Array)) return;
    if (pageMode === 'remote' && options.force !== true && !screenActive) return;
    if (state.ws && state.ws.readyState === WebSocket.OPEN) {
        if (pageMode === 'remote' && options.wake !== true && options.markPowerOff === true && !state.remotePowerOffSent) {
            state.ws.send(buildSetDisplayPowerControlMessage(false));
            state.remotePowerOffSent = true;
            state.remotePowerOffAutoTried = true;
            if (elements.remotePowerStatus) {
                elements.remotePowerStatus.textContent = 'Phone display off while remote control is active';
            }
        }
        state.ws.send(data);
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
            screenVideoSize.width = frame.displayWidth;
            screenVideoSize.height = frame.displayHeight;

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

function configureScreenDecoderFromConfig(force = false) {
    if (!screenConfig.sps.length || !screenConfig.pps.length) return false;

    const sps = screenConfig.sps[0];
    const pps = screenConfig.pps[0];
    if (sps.length < 4) return false;

    const codec = `avc1.${toHex2(sps[1])}${toHex2(sps[2])}${toHex2(sps[3])}`;
    const description = buildAvcC(screenConfig.sps, screenConfig.pps);
    const nextKey = `${codec}:${bytesToHex(description)}`;

    if (screenConfigured && !force && screenConfigKey === nextKey) {
        return true;
    }

    resetScreenDecoderForReconfigure();
    const decoder = ensureScreenDecoder();
    decoder.configure({
        codec,
        description,
        optimizeForLatency: true,
    });
    screenConfigured = true;
    screenConfigKey = nextKey;
    console.log('Screen decoder configured', { codec, sps: screenConfig.sps.length, pps: screenConfig.pps.length, force });
    return true;
}

function destroyScreenDecoder() {
    if (screenDecoder) {
        try { screenDecoder.close(); } catch (e) {}
        screenDecoder = null;
    }
    screenConfigured = false;
    screenConfigKey = '';
    screenConfig = { sps: [], pps: [] };
    screenPendingSessionSize = null;
    screenLastPopupFitKey = '';
    screenVideoSize = { width: 0, height: 0 };
    screenPointerDown = false;
    screenFrameCount = 0;
    screenFpsCounter = 0;
    if (screenCanvasEl) {
        screenCanvasEl.classList.remove('active');
    }
    const placeholder = document.getElementById('screenPlaceholder');
    if (placeholder) placeholder.classList.remove('hidden');
    const fpsEl = document.getElementById('screenFPS');
    if (fpsEl) fpsEl.textContent = '';
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

        const configChanged = collectScreenConfig(nalUnits);
        if (!screenConfigured || configChanged || isConfig) {
            configureScreenDecoderFromConfig(configChanged || isConfig);
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
        resetScreenDecoderForReconfigure();
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
    const next = { sps: [], pps: [] };
    for (const nal of nalUnits) {
        if (!nal.length) continue;
        const nalType = nal[0] & 0x1F;
        if (nalType === 7) {
            next.sps.push(nal);
        } else if (nalType === 8) {
            next.pps.push(nal);
        }
    }

    if (!next.sps.length && !next.pps.length) {
        return false;
    }

    const nextKey = `${next.sps.map(bytesToHex).join('|')}::${next.pps.map(bytesToHex).join('|')}`;
    const currentKey = `${screenConfig.sps.map(bytesToHex).join('|')}::${screenConfig.pps.map(bytesToHex).join('|')}`;
    if (nextKey === currentKey) {
        return false;
    }

    if (next.sps.length) {
        screenConfig.sps = next.sps;
    }
    if (next.pps.length) {
        screenConfig.pps = next.pps;
    }
    return true;
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

function bytesToHex(data) {
    return Array.from(data, b => b.toString(16).padStart(2, '0')).join('');
}

function resetScreenDecoderForReconfigure() {
    if (screenDecoder) {
        try { screenDecoder.close(); } catch (e) {}
        screenDecoder = null;
    }
    screenConfigured = false;
}

function handleScreenSession(data) {
    if (!data) return;
    const width = Number(data.width || 0);
    const height = Number(data.height || 0);
    if (!width || !height) return;

    screenPendingSessionSize = { width, height };
    screenVideoSize.width = width;
    screenVideoSize.height = height;
    fitPopupToVideo(width, height);

    // Rotation/resolution change creates a brand-new encoder session on Android.
    // Drop both decoder state and codec config so we wait for fresh SPS/PPS.
    screenConfig = { sps: [], pps: [] };
    screenConfigKey = '';
    resetScreenDecoderForReconfigure();
}

function fitPopupToVideo(width, height) {
    if (pageMode !== 'remote' || !width || !height) return;
    const fitKey = `${width}x${height}`;
    if (screenLastPopupFitKey === fitKey) {
        return;
    }
    screenLastPopupFitKey = fitKey;

    if (!screenCanvasEl || typeof window.resizeTo !== 'function') return;

    const availWidth = Math.max(360, window.screen.availWidth - 48);
    const availHeight = Math.max(480, window.screen.availHeight - 48);
    const scale = Math.min(availWidth / width, availHeight / height, 1.75);
    const targetInnerWidth = Math.max(240, Math.round(width * scale));
    const targetInnerHeight = Math.max(320, Math.round(height * scale));

    const chromeWidth = Math.max(0, window.outerWidth - window.innerWidth);
    const chromeHeight = Math.max(0, window.outerHeight - window.innerHeight);
    try {
        window.resizeTo(targetInnerWidth + chromeWidth, targetInnerHeight + chromeHeight);
    } catch (e) {
        // ignore popup resize failures
    }
}

const SCRCPY_CONTROL_TYPE_KEYCODE = 0;
const SCRCPY_CONTROL_TYPE_TOUCH = 2;
const SCRCPY_CONTROL_TYPE_SET_DISPLAY_POWER = 10;
const ANDROID_KEY_ACTION_DOWN = 0;
const ANDROID_KEY_ACTION_UP = 1;
const ANDROID_MOTION_ACTION_DOWN = 0;
const ANDROID_MOTION_ACTION_UP = 1;
const ANDROID_MOTION_ACTION_MOVE = 2;
const ANDROID_BUTTON_PRIMARY = 1;

function getScreenPointerPosition(e) {
    if (!screenCanvasEl || !screenVideoSize.width || !screenVideoSize.height) {
        return null;
    }
    const rect = screenCanvasEl.getBoundingClientRect();
    if (!rect.width || !rect.height) {
        return null;
    }
    const relX = clamp((e.clientX - rect.left) / rect.width, 0, 1);
    const relY = clamp((e.clientY - rect.top) / rect.height, 0, 1);
    return {
        x: Math.round(relX * (screenVideoSize.width - 1)),
        y: Math.round(relY * (screenVideoSize.height - 1)),
        screenWidth: screenVideoSize.width,
        screenHeight: screenVideoSize.height,
    };
}

function buildKeyControlMessage({ action, keycode, repeat = 0, metaState = 0 }) {
    const data = new Uint8Array(14);
    const view = new DataView(data.buffer);
    view.setUint8(0, SCRCPY_CONTROL_TYPE_KEYCODE);
    view.setUint8(1, action);
    view.setInt32(2, keycode, false);
    view.setInt32(6, repeat, false);
    view.setInt32(10, metaState, false);
    return data;
}

function buildTouchControlMessage({ action, pointerId, x, y, screenWidth, screenHeight, pressure, actionButton, buttons }) {
    const data = new Uint8Array(32);
    const view = new DataView(data.buffer);
    view.setUint8(0, SCRCPY_CONTROL_TYPE_TOUCH);
    view.setUint8(1, action);
    setUint64BE(view, 2, pointerId);
    view.setInt32(10, x, false);
    view.setInt32(14, y, false);
    view.setUint16(18, screenWidth, false);
    view.setUint16(20, screenHeight, false);
    view.setUint16(22, floatToU16FixedPoint(pressure), false);
    view.setInt32(24, actionButton, false);
    view.setInt32(28, buttons, false);
    return data;
}

function buildSetDisplayPowerControlMessage(on) {
    const data = new Uint8Array(2);
    const view = new DataView(data.buffer);
    view.setUint8(0, SCRCPY_CONTROL_TYPE_SET_DISPLAY_POWER);
    view.setUint8(1, on ? 1 : 0);
    return data;
}

function setUint64BE(view, offset, value) {
    const big = BigInt(value);
    view.setUint32(offset, Number((big >> 32n) & 0xffffffffn), false);
    view.setUint32(offset + 4, Number(big & 0xffffffffn), false);
}

function floatToU16FixedPoint(value) {
    const clamped = clamp(value, 0, 1);
    return Math.round(clamped * 0xffff);
}

function clamp(value, min, max) {
    return Math.min(max, Math.max(min, value));
}

function mapBrowserKeyToAndroid(e) {
    const special = {
        Enter: 66,
        Backspace: 67,
        Delete: 112,
        Escape: 4,
        Tab: 61,
        ' ': 62,
        ArrowUp: 19,
        ArrowDown: 20,
        ArrowLeft: 21,
        ArrowRight: 22,
        Home: 3,
        End: 123,
        PageUp: 92,
        PageDown: 93,
    };
    if (special[e.key] != null) {
        return special[e.key];
    }
    if (/^[a-zA-Z]$/.test(e.key)) {
        return 29 + (e.key.toUpperCase().charCodeAt(0) - 65);
    }
    if (/^[0-9]$/.test(e.key)) {
        return 7 + Number(e.key);
    }
    return null;
}

function showScreenViewer(show) {
    screenActive = pageMode === 'remote' && !!screenCanvasEl && !!screenCtx ? show : false;
    if (show && pageMode === 'remote' && screenCanvasEl) {
        screenCanvasEl.focus();
    }
    if (!screenActive) destroyScreenDecoder();
}
