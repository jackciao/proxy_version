const API = {
    baseURL: '/api',
    getToken() { return localStorage.getItem('auth_token') },
    setToken(t) { localStorage.setItem('auth_token', t) },
    removeToken() { localStorage.removeItem('auth_token') },
    isAuthenticated() { return !!this.getToken() },
    async request(e, o = {}) {
        const u = `${this.baseURL}${e}`, t = this.getToken(), h = { 'Content-Type': 'application/json', ...o.headers };
        t && (h.Authorization = `Bearer ${t}`);
        try {
            const r = await fetch(u, { ...o, headers: h });
            const body = await r.text();
            let d = {};
            if (body) {
                try { d = JSON.parse(body) } catch { d = { error: r.ok ? '服务器返回了无法解析的响应' : `请求失败 (HTTP ${r.status})` } }
            }
            if (!r.ok) { if (r.status === 401) { this.removeToken(); window.location.reload() } throw new Error(d.error || `请求失败 (HTTP ${r.status})`) }
            return d
        } catch (e) { console.error('API Error:', e); throw e }
    },
    get(e) { return this.request(e, { method: 'GET' }) },
    post(e, b) { return this.request(e, { method: 'POST', body: JSON.stringify(b) }) },
    put(e, b) { return this.request(e, { method: 'PUT', body: JSON.stringify(b) }) },
    delete(e) { return this.request(e, { method: 'DELETE' }) },
    // Auth
    register(u, p, e = '') { return this.post('/auth/register', { username: u, password: p, email: e }) },
    async login(u, p) { const d = await this.post('/auth/login', { username: u, password: p }); d.token && this.setToken(d.token); return d },
    logout() { this.removeToken() },
    getCurrentUser() { return this.get('/auth/me') },
    // Nodes
    getNodes() { return this.get('/nodes') },
    getNode(i) { return this.get(`/nodes/${i}`) },
    createNode(d) { return this.post('/nodes', d) },
    updateNode(i, d) { return this.put(`/nodes/${i}`, d) },
    deleteNode(i) { return this.delete(`/nodes/${i}`) },
    startNode(i) { return this.post(`/nodes/${i}/start`) },
    stopNode(i) { return this.post(`/nodes/${i}/stop`) },
    getNodeShare(i) { return this.get(`/nodes/${i}/share`) },
    toggleNodeWarp(i, enabled) { return this.post(`/nodes/${i}/warp`, { enabled }) },
    toggleNodeAimili(i, enabled) { return this.post(`/nodes/${i}/aimili`, { enabled }) },
    toggleNodePacketStream(i, enabled) { return this.post(`/nodes/${i}/packetstream`, { enabled }) },
    // System
    getSystemStatus() { return this.get('/system/status') },
    detectReverseProxy() { return this.get('/system/detect') },
    getProtocols() { return this.get('/system/protocols') },
    getCoreStatus() { return this.get('/system/cores') },
    getRandomPort() { return this.get('/system/random-port') },
    getServerIPs() { return this.get('/system/ips') },
    checkPort(port, ip = '') { return this.post('/system/check-port', { port, ip }) },
    getSuggestedSNI() { return this.get('/system/suggest-sni') },
    installCore(c) { return this.post('/system/cores/install', { core: c }) },
    uninstallCore(c) { return this.post('/system/cores/uninstall', { core: c }) },
    // Certificates
    getCertificates() { return this.get('/certificates') },
    applyCertificate(d) { return this.post('/certificates/apply', d) },
    getCertProgress(domain) { return this.get(`/certificates/progress/${encodeURIComponent(domain)}`) },
    deleteCertificate(domain) { return this.delete(`/certificates/${encodeURIComponent(domain)}`) },
    // Camouflage
    getCamouflageStatus(domain) { return this.get(`/camouflage/status/${encodeURIComponent(domain)}`) },
    // WARP
    getWarpStatus() { return this.get('/warp/status') },
    registerWarp() { return this.post('/warp/register', {}) },
    refreshWarp() { return this.post('/warp/refresh', {}) },
    upgradeWarp(licenseKey) { return this.post('/warp/upgrade', { license_key: licenseKey }) },
    importWarp(config) { return this.post('/warp/import', config) },
    deleteWarp() { return this.delete('/warp') },
    exportWarp() { return this.get('/warp/export') },
    checkWarpStreaming() { return this.get('/warp/streaming-check') },
    // Aimili VPN
    getAimiliStatus() { return this.get('/aimili/status') },
    installAimili() { return this.post('/aimili/install', {}) },
    refreshAimiliCountries() { return this.post('/aimili/refresh', {}) },
    configureAimili(country) { return this.post('/aimili/configure', { country }) },
    // PacketStream
    getPacketStreamStatus() { return this.get('/packetstream/status') },
    savePacketStreamConfig(cfg) { return this.post('/packetstream/config', cfg) },
    deletePacketStreamConfig() { return this.delete('/packetstream/config') },
    testPacketStream(cfg) { return this.post('/packetstream/test', cfg || {}) }
};
window.API = API;
