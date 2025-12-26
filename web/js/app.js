class App {
    constructor() {
        this.currentPage = 'dashboard';
        this.currentUser = null;
        this.nodes = [];
        this.certificates = [];
        this.protocols = [];
        this.guideStep = 1;
        this.warpEventHandlersInitialized = false;
        this.init()
    }

    async init() {
        if (API.isAuthenticated()) {
            try {
                this.currentUser = await API.getCurrentUser();
                this.showMainApp();
                this.checkFirstLogin()
            } catch (e) {
                API.removeToken();
                this.showAuth()
            }
        } else {
            this.showAuth()
        }
        this.bindEvents();
        this.hideLoading()
    }

    hideLoading() {
        const l = document.getElementById('loading');
        l.style.opacity = '0';
        setTimeout(() => l.classList.add('hidden'), 300)
    }

    showAuth() {
        document.getElementById('auth-container').classList.remove('hidden');
        document.getElementById('main-container').classList.add('hidden')
    }

    showMainApp() {
        document.getElementById('auth-container').classList.add('hidden');
        document.getElementById('main-container').classList.remove('hidden');
        if (this.currentUser) {
            document.getElementById('username-display').textContent = this.currentUser.username;
            document.getElementById('user-avatar').textContent = this.currentUser.username[0].toUpperCase()
        }
        // Bind mobile menu events after main app is shown
        this.bindMobileMenuEvents();
        this.loadDashboardData();
        this.loadProtocols();
        this.loadCertificatesForDropdown();
    }

    bindMobileMenuEvents() {
        const toggle = document.getElementById('mobile-menu-toggle');
        const overlay = document.getElementById('sidebar-overlay');
        if (toggle && !toggle._bound) {
            toggle._bound = true;
            toggle.addEventListener('click', (e) => {
                e.preventDefault();
                e.stopPropagation();
                this.toggleMobileMenu();
            });
        }
        if (overlay && !overlay._bound) {
            overlay._bound = true;
            overlay.addEventListener('click', () => this.closeMobileMenu());
        }
    }

    checkFirstLogin() {
        if (!localStorage.getItem('guide_completed')) {
            this.showGuide()
        }
    }

    showGuide() {
        this.guideStep = 1;
        const o = document.getElementById('guide-overlay');
        o.classList.remove('hidden');
        this.updateGuideStep();
        this.initGuideDots()
    }

    hideGuide() {
        document.getElementById('guide-overlay').classList.add('hidden');
        localStorage.setItem('guide_completed', 'true')
    }

    updateGuideStep() {
        document.querySelectorAll('.guide-step').forEach(s => s.classList.remove('active'));
        const cs = document.querySelector(`.guide-step[data-step="${this.guideStep}"]`);
        if (cs) cs.classList.add('active');
        document.querySelectorAll('.guide-dot').forEach((d, i) => d.classList.toggle('active', i + 1 === this.guideStep));
        const nb = document.getElementById('guide-next');
        if (nb) nb.textContent = this.guideStep >= 3 ? '开始使用' : '下一步'
    }

    initGuideDots() {
        const dc = document.getElementById('guide-dots');
        if (!dc) return;
        dc.innerHTML = '';
        for (let i = 1; i <= 3; i++) {
            const d = document.createElement('div');
            d.className = 'guide-dot' + (i === 1 ? ' active' : '');
            d.onclick = () => { this.guideStep = i; this.updateGuideStep() };
            dc.appendChild(d)
        }
    }

    async loadProtocols() {
        try {
            const r = await API.getProtocols();
            this.protocols = r.protocols || [];
            this.updateProtocolSelect()
        } catch (e) {
            console.error('Failed to load protocols:', e)
        }
    }

    async loadCertificatesForDropdown() {
        try {
            const d = await API.getCertificates();
            this.certificates = d.certificates || [];
            this.updateDomainDropdown();
        } catch (e) {
            console.error('Failed to load certificates for dropdown:', e);
        }
    }

    updateDomainDropdown() {
        const s = document.getElementById('node-domain');
        if (!s) return;
        // Convert input to select if needed
        if (s.tagName === 'INPUT') {
            const select = document.createElement('select');
            select.id = 'node-domain';
            select.className = s.className;
            s.parentNode.replaceChild(select, s);
        }
        const sel = document.getElementById('node-domain');
        sel.innerHTML = '<option value="">不需要域名 / 使用服务器IP</option>';
        this.certificates.forEach(c => {
            const o = document.createElement('option');
            o.value = c.domain;
            o.textContent = c.domain;
            sel.appendChild(o);
        });
    }

    updateProtocolSelect() {
        const s = document.getElementById('node-protocol');
        if (!s) return;
        s.innerHTML = '<option value="">请选择协议</option>';
        this.protocols.forEach(p => {
            const o = document.createElement('option');
            o.value = p.id;
            o.textContent = p.name + (p.recommended ? ' ⭐' : '');
            o.dataset.desc = p.description;
            o.dataset.needsDomain = p.needs_domain;
            o.dataset.needsCert = p.needs_cert;
            s.appendChild(o)
        })
    }

    toggleMobileMenu() {
        const sidebar = document.getElementById('sidebar');
        const overlay = document.getElementById('sidebar-overlay');
        sidebar.classList.toggle('open');
        overlay.classList.toggle('active');
    }

    closeMobileMenu() {
        const sidebar = document.getElementById('sidebar');
        const overlay = document.getElementById('sidebar-overlay');
        sidebar.classList.remove('open');
        overlay.classList.remove('active');
    }

    bindEvents() {
        // Mobile menu toggle
        document.getElementById('mobile-menu-toggle')?.addEventListener('click', () => this.toggleMobileMenu());
        document.getElementById('sidebar-overlay')?.addEventListener('click', () => this.closeMobileMenu());

        document.getElementById('login-form')?.addEventListener('submit', async (e) => {
            e.preventDefault();
            const u = document.getElementById('login-username').value;
            const p = document.getElementById('login-password').value;
            try {
                const r = await API.login(u, p);
                this.currentUser = r.user;
                this.showMainApp();
                this.checkFirstLogin();
                this.showToast('登录成功', 'success')
            } catch (err) {
                this.showToast(err.message, 'error')
            }
        });

        document.querySelectorAll('.nav-item').forEach(i => i.addEventListener('click', (e) => {
            e.preventDefault();
            this.navigateTo(i.dataset.page);
            this.closeMobileMenu(); // Close menu after navigation on mobile
        }));

        document.getElementById('logout-btn')?.addEventListener('click', () => {
            API.logout();
            this.currentUser = null;
            this.showAuth();
            this.showToast('已退出登录', 'info')
        });

        document.getElementById('guide-skip')?.addEventListener('click', () => this.hideGuide());
        document.getElementById('guide-next')?.addEventListener('click', () => {
            if (this.guideStep >= 3) { this.hideGuide() } else { this.guideStep++; this.updateGuideStep() }
        });

        document.getElementById('quick-create-node')?.addEventListener('click', () => this.openCreateNodeModal());
        document.getElementById('quick-apply-cert')?.addEventListener('click', () => this.openModal('apply-cert-modal'));
        document.getElementById('create-node-btn')?.addEventListener('click', () => this.openCreateNodeModal());
        document.getElementById('empty-create-node')?.addEventListener('click', () => this.openCreateNodeModal());
        document.getElementById('apply-cert-btn')?.addEventListener('click', () => this.openModal('apply-cert-modal'));
        document.getElementById('empty-apply-cert')?.addEventListener('click', () => this.openModal('apply-cert-modal'));
        document.getElementById('refresh-detection')?.addEventListener('click', () => this.loadDetectionResult());
        document.getElementById('redetect-proxy')?.addEventListener('click', () => this.loadDetectionResult());

        document.querySelectorAll('.modal-close, .modal-cancel').forEach(b => b.addEventListener('click', () => this.closeModal()));
        document.getElementById('modal-overlay')?.addEventListener('click', (e) => { if (e.target.id === 'modal-overlay') this.closeModal() });

        document.getElementById('create-node-form')?.addEventListener('submit', async (e) => { e.preventDefault(); await this.createNode() });

        document.getElementById('node-protocol')?.addEventListener('change', (e) => {
            const s = e.target;
            const opt = s.options[s.selectedIndex];
            const desc = document.getElementById('protocol-desc');
            const dg = document.getElementById('domain-group');
            const ipg = document.getElementById('ip-bind-group');
            if (opt.value) {
                const p = this.protocols.find(x => x.id === opt.value);
                if (p) {
                    desc.innerHTML = p.description + (p.recommended ? ' <span class="recommended">✓ 推荐</span>' : '');
                    desc.classList.add('show');
                    dg.style.display = p.needs_domain ? 'block' : 'none';
                    // Show IP binding for Reality protocols
                    if (ipg) ipg.style.display = opt.value.includes('reality') ? 'block' : 'none';
                    if (!p.needs_domain) document.getElementById('node-domain').value = ''
                } else {
                    desc.classList.remove('show');
                    dg.style.display = 'block';
                    if (ipg) ipg.style.display = 'none';
                }
            } else {
                desc.classList.remove('show');
                dg.style.display = 'block';
                if (ipg) ipg.style.display = 'none';
            }
        });

        // Port conflict checking with debounce
        let portCheckTimeout = null;
        document.getElementById('node-port')?.addEventListener('input', (e) => {
            clearTimeout(portCheckTimeout);
            portCheckTimeout = setTimeout(() => this.checkPortConflict(e.target.value), 500);
        });

        document.getElementById('cert-method')?.addEventListener('change', (e) => {
            const d = document.getElementById('dns-settings');
            e.target.value === 'dns' ? d.classList.remove('hidden') : d.classList.add('hidden')
        });

        document.getElementById('dns-provider')?.addEventListener('change', (e) => {
            const cfe = document.getElementById('cf-email-group');
            if (cfe) cfe.style.display = e.target.value === 'cloudflare' ? 'block' : 'none'
        });

        document.getElementById('apply-cert-form')?.addEventListener('submit', async (e) => { e.preventDefault(); await this.applyCertificate() });

        document.getElementById('edit-node-form')?.addEventListener('submit', async (e) => { e.preventDefault(); await this.saveNodeEdit() });

        window.showGuide = () => this.showGuide();
        window.installCore = (c) => this.installCore(c);
        window.uninstallCore = (c) => this.uninstallCore(c);
        window.shareNode = (id) => this.shareNode(id);
        window.editNode = (id) => this.editNode(id);
        window.copyToClipboard = (text) => this.copyToClipboard(text);
        window.showWarpTutorial = () => this.openModal('warp-tutorial-modal');
    }

    navigateTo(p) {
        this.currentPage = p;
        document.querySelectorAll('.nav-item').forEach(i => i.classList.toggle('active', i.dataset.page === p));
        document.querySelectorAll('.page').forEach(pg => pg.classList.toggle('active', pg.id === `page-${p}`));
        switch (p) {
            case 'dashboard': this.loadDashboardData(); break;
            case 'nodes': this.loadNodes(); break;
            case 'certificates': this.loadCertificates(); break;
            case 'system': this.loadSystemInfo(); break
        }
    }

    async loadDashboardData() {
        try {
            const d = await API.getNodes();
            this.nodes = d.nodes || [];
            const r = this.nodes.filter(n => n.status === 'running').length;
            document.getElementById('running-nodes').textContent = r;
            document.getElementById('total-nodes').textContent = this.nodes.length;
            try {
                const c = await API.getCertificates();
                this.certificates = c.certificates || [];
                document.getElementById('total-certs').textContent = this.certificates.length
            } catch {
                document.getElementById('total-certs').textContent = '0'
            }
            document.getElementById('system-status').textContent = '正常';
            this.loadDetectionResult()
        } catch (e) {
            console.error('Dashboard error:', e)
        }
    }

    async loadDetectionResult() {
        const c = document.getElementById('detection-result');
        if (!c) return;
        c.innerHTML = '<div class="detection-loading">正在检测系统环境...</div>';
        try {
            const r = await API.detectReverseProxy();
            let h = '';
            h += `<div class="detection-item"><div class="detection-icon ${r.nginx_installed ? 'installed' : 'not-installed'}">${r.nginx_installed ? '✓' : '×'}</div><div class="detection-info"><div class="detection-name">Nginx</div><div class="detection-version">${r.nginx_installed ? `版本 ${r.nginx_version || '未知'}` : '未安装'}</div></div></div>`;
            h += `<div class="detection-item"><div class="detection-icon ${r.openresty_installed ? 'installed' : 'not-installed'}">${r.openresty_installed ? '✓' : '×'}</div><div class="detection-info"><div class="detection-name">OpenResty</div><div class="detection-version">${r.openresty_installed ? `版本 ${r.openresty_version || '未知'}${r.openresty_container ? ' (容器)' : ''}` : '未安装'}</div></div></div>`;
            if (r.panel_detected) {
                h += `<div class="detection-item"><div class="detection-icon installed">✓</div><div class="detection-info"><div class="detection-name">控制面板</div><div class="detection-version">${r.panel_detected}</div></div></div>`
            }
            h += `<div class="detection-recommendation">💡 ${r.recommendation}</div>`;
            c.innerHTML = h
        } catch {
            c.innerHTML = '<div class="detection-loading">检测失败，请重试</div>'
        }
    }

    async loadNodes() {
        const c = document.getElementById('nodes-list');
        if (!c) return;
        try {
            const d = await API.getNodes();
            this.nodes = d.nodes || [];
            if (this.nodes.length === 0) {
                c.innerHTML = '<div class="empty-state"><svg viewBox="0 0 24 24" width="64" height="64"><path fill="currentColor" d="M4 6h18V4H4c-1.1 0-2 .9-2 2v11H0v3h14v-3H4V6zm19 2h-6c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h6c.55 0 1-.45 1-1V9c0-.55-.45-1-1-1zm-1 9h-4v-7h4v7z"/></svg><p>暂无节点</p><button class="btn btn-primary" onclick="app.openCreateNodeModal()">创建第一个节点</button></div>';
                return
            }
            c.innerHTML = this.nodes.map(n => this.renderNodeCard(n)).join('')
        } catch {
            c.innerHTML = '<div class="empty-state"><p>加载失败</p></div>'
        }
    }

    renderNodeCard(n) {
        const pn = this.protocols.find(p => p.id === n.protocol);
        const warpEnabled = n.warp_enabled === 1 || n.warp_enabled === true;
        return `<div class="node-card" data-node-id="${n.id}">
            <div class="node-header">
                <span class="node-name">${this.escapeHtml(n.name)}</span>
                <span class="node-status ${n.status}">${n.status === 'running' ? '运行中' : '已停止'}</span>
            </div>
            <div class="node-info">
                <div class="node-info-row"><span class="node-info-label">协议</span><span class="node-info-value">${pn ? pn.name : n.protocol}</span></div>
                <div class="node-info-row"><span class="node-info-label">域名/IP</span><span class="node-info-value">${n.domain || '-'}</span></div>
                <div class="node-info-row"><span class="node-info-label">端口</span><span class="node-info-value">${n.port}</span></div>
                <div class="node-info-row">
                    <span class="node-info-label">WARP</span>
                    <label class="toggle-switch">
                        <input type="checkbox" ${warpEnabled ? 'checked' : ''} onchange="app.toggleNodeWarp(${n.id}, this.checked)">
                        <span class="toggle-slider"></span>
                    </label>
                </div>
            </div>
            <div class="node-actions">
                ${n.status === 'running' ? `<button class="btn btn-secondary btn-sm" onclick="app.stopNode(${n.id})">停止</button>` : `<button class="btn btn-success btn-sm" onclick="app.startNode(${n.id})">启动</button>`}
                <button class="btn btn-primary btn-sm" onclick="shareNode(${n.id})">分享</button>
                <button class="btn btn-warning btn-sm" onclick="editNode(${n.id})">编辑</button>
                <button class="btn btn-danger btn-sm" onclick="app.deleteNode(${n.id})">删除</button>
            </div>
        </div>`
    }

    async editNode(id) {
        const node = this.nodes.find(n => n.id === id);
        if (!node) {
            this.showToast('节点不存在', 'error');
            return;
        }

        const protocol = node.protocol || '';
        const config = node.config || {};

        // Populate basic fields
        document.getElementById('edit-node-id').value = node.id;
        document.getElementById('edit-node-protocol').value = protocol;
        document.getElementById('edit-node-name').value = node.name;
        document.getElementById('edit-node-port').value = node.port;
        document.getElementById('edit-node-domain').value = node.domain || '';

        // Hide all config groups first
        ['uuid', 'password', 'reality', 'transport', 'grpc', 'speed', 'congestion'].forEach(g => {
            const el = document.getElementById(`edit-${g}-group`);
            if (el) el.style.display = 'none';
        });

        // Show relevant fields based on protocol
        const isVless = protocol.includes('vless');
        const isReality = protocol.includes('reality');
        const isGrpc = protocol.includes('grpc');
        const isWs = protocol.includes('ws');
        const isHysteria2 = protocol.includes('hysteria');
        const isTuic = protocol.includes('tuic');

        // UUID for VLESS and TUIC
        if (isVless || isTuic) {
            document.getElementById('edit-uuid-group').style.display = 'block';
            document.getElementById('edit-config-uuid').value = config.uuid || '';
        }

        // Password for Hysteria2 and TUIC
        if (isHysteria2 || isTuic) {
            document.getElementById('edit-password-group').style.display = 'block';
            document.getElementById('edit-config-password').value = config.password || '';
        }

        // Reality config
        if (isReality) {
            document.getElementById('edit-reality-group').style.display = 'block';
            document.getElementById('edit-config-servername').value = config.serverName || 'www.microsoft.com';
            document.getElementById('edit-config-publickey').value = config.publicKey || '';
            document.getElementById('edit-config-shortid').value = config.shortId || '';
        }

        // WebSocket path
        if (isWs) {
            document.getElementById('edit-transport-group').style.display = 'block';
            document.getElementById('edit-config-path').value = config.path || '/vless-ws';
        }

        // gRPC service name
        if (isGrpc) {
            document.getElementById('edit-grpc-group').style.display = 'block';
            document.getElementById('edit-config-servicename').value = config.serviceName || 'grpc';
        }

        // Hysteria2 speed settings
        if (isHysteria2) {
            document.getElementById('edit-speed-group').style.display = 'block';
            document.getElementById('edit-config-upmbps').value = config.upMbps || 100;
            document.getElementById('edit-config-downmbps').value = config.downMbps || 100;
            document.getElementById('edit-config-obfs').value = config.obfs || '';
        }

        // TUIC congestion control
        if (isTuic) {
            document.getElementById('edit-congestion-group').style.display = 'block';
            document.getElementById('edit-config-congestion').value = config.congestion || 'bbr';
        }

        this.openModal('edit-node-modal');
    }

    async saveNodeEdit() {
        const id = parseInt(document.getElementById('edit-node-id').value);
        const protocol = document.getElementById('edit-node-protocol').value;
        const name = document.getElementById('edit-node-name').value;
        const port = parseInt(document.getElementById('edit-node-port').value);
        const domain = document.getElementById('edit-node-domain').value;

        // Build config from visual fields
        let config = {};

        const isVless = protocol.includes('vless');
        const isReality = protocol.includes('reality');
        const isGrpc = protocol.includes('grpc');
        const isWs = protocol.includes('ws');
        const isHysteria2 = protocol.includes('hysteria');
        const isTuic = protocol.includes('tuic');

        // UUID
        if (isVless || isTuic) {
            const uuid = document.getElementById('edit-config-uuid').value;
            if (uuid) config.uuid = uuid;
        }

        // Password
        if (isHysteria2 || isTuic) {
            const password = document.getElementById('edit-config-password').value;
            if (password) config.password = password;
        }

        // Reality config
        if (isReality) {
            config.serverName = document.getElementById('edit-config-servername').value || 'www.microsoft.com';
            const pk = document.getElementById('edit-config-publickey').value;
            const sid = document.getElementById('edit-config-shortid').value;
            if (pk) config.publicKey = pk;
            if (sid) config.shortId = sid;
        }

        // WebSocket path
        if (isWs) {
            config.path = document.getElementById('edit-config-path').value || '/vless-ws';
        }

        // gRPC service name
        if (isGrpc) {
            config.serviceName = document.getElementById('edit-config-servicename').value || 'grpc';
        }

        // Hysteria2 speed settings
        if (isHysteria2) {
            config.upMbps = parseInt(document.getElementById('edit-config-upmbps').value) || 100;
            config.downMbps = parseInt(document.getElementById('edit-config-downmbps').value) || 100;
            const obfs = document.getElementById('edit-config-obfs').value;
            if (obfs) config.obfs = obfs;
        }

        // TUIC congestion control
        if (isTuic) {
            config.congestion = document.getElementById('edit-config-congestion').value || 'bbr';
        }

        try {
            await API.updateNode(id, { name, port, domain, config });
            this.showToast('节点更新成功', 'success');
            this.closeModal();
            this.loadNodes();
        } catch (e) {
            this.showToast(e.message, 'error');
        }
    }

    async shareNode(id) {
        try {
            const share = await API.getNodeShare(id);
            this.openShareModal(share)
        } catch (e) {
            this.showToast(e.message, 'error')
        }
    }

    openShareModal(share) {
        const url = document.getElementById('share-url');
        const json = document.getElementById('share-json');
        const qr = document.getElementById('share-qrcode');
        if (url) url.value = share.url;
        if (json) json.textContent = share.json;
        if (qr) {
            qr.innerHTML = '';
            try {
                if (typeof qrcode !== 'undefined') {
                    const qrGen = qrcode(0, 'M');
                    qrGen.addData(share.url);
                    qrGen.make();
                    qr.innerHTML = qrGen.createSvgTag(5, 0)
                } else {
                    qr.innerHTML = `<img src="https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(share.url)}" alt="QR Code" style="width:200px;height:200px">`
                }
            } catch (e) {
                qr.innerHTML = `<img src="https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(share.url)}" alt="QR Code" style="width:200px;height:200px">`
            }
        }
        this.openModal('share-modal')
    }

    async checkPortConflict(port) {
        const warning = document.getElementById('port-conflict-warning');
        const msg = document.getElementById('port-conflict-message');
        if (!warning || !port) return;

        const ip = document.getElementById('node-ip-bind')?.value || '';
        try {
            const result = await API.checkPort(parseInt(port), ip);
            if (!result.available) {
                msg.textContent = `端口 ${port} 已被占用${result.process_name ? ` (${result.process_name})` : ''}`;
                warning.classList.remove('hidden');
            } else {
                warning.classList.add('hidden');
            }
        } catch (e) {
            warning.classList.add('hidden');
        }
    }

    async loadServerIPs() {
        const select = document.getElementById('node-ip-bind');
        if (!select) return;

        try {
            const data = await API.getServerIPs();
            select.innerHTML = '<option value="">自动 (所有接口)</option>';

            // Add IPv4 addresses
            if (data.ipv4_list && data.ipv4_list.length > 0) {
                const optgroup4 = document.createElement('optgroup');
                optgroup4.label = 'IPv4';
                data.ipv4_list.forEach(ip => {
                    const opt = document.createElement('option');
                    opt.value = ip;
                    opt.textContent = ip + (ip === data.public_ipv4 ? ' (公网)' : '');
                    optgroup4.appendChild(opt);
                });
                select.appendChild(optgroup4);
            }

            // Add IPv6 addresses  
            if (data.ipv6_list && data.ipv6_list.length > 0) {
                const optgroup6 = document.createElement('optgroup');
                optgroup6.label = 'IPv6';
                data.ipv6_list.forEach(ip => {
                    const opt = document.createElement('option');
                    opt.value = ip;
                    opt.textContent = ip.length > 20 ? ip.substring(0, 20) + '...' : ip;
                    opt.title = ip;
                    optgroup6.appendChild(opt);
                });
                select.appendChild(optgroup6);
            }
        } catch (e) {
            console.error('Failed to load server IPs:', e);
        }
    }

    copyToClipboard(text) {
        if (navigator.clipboard) {
            navigator.clipboard.writeText(text).then(() => this.showToast('已复制到剪贴板', 'success')).catch(() => this.fallbackCopy(text))
        } else {
            this.fallbackCopy(text)
        }
    }

    fallbackCopy(text) {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.left = '-9999px';
        document.body.appendChild(ta);
        ta.select();
        try {
            document.execCommand('copy');
            this.showToast('已复制到剪贴板', 'success')
        } catch {
            this.showToast('复制失败', 'error')
        }
        document.body.removeChild(ta)
    }

    async loadCertificates() {
        const c = document.getElementById('certs-list');
        if (!c) return;
        try {
            const d = await API.getCertificates();
            this.certificates = d.certificates || [];
            this.updateDomainDropdown(); // Update dropdown when certificates change
            if (this.certificates.length === 0) {
                c.innerHTML = '<div class="empty-state"><svg viewBox="0 0 24 24" width="64" height="64"><path fill="currentColor" d="M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4z"/></svg><p>暂无证书</p><button class="btn btn-primary" onclick="app.openModal(\'apply-cert-modal\')">申请第一个证书</button></div>';
                return
            }
            c.innerHTML = `<div class="certs-grid">${this.certificates.map(ct => this.renderCertCard(ct)).join('')}</div>`;
        } catch {
            c.innerHTML = '<div class="empty-state"><p>加载失败</p></div>'
        }
    }

    renderCertCard(ct) {
        const expiresStr = ct.expires_at ? new Date(ct.expires_at).toLocaleDateString('zh-CN') : '-';
        const renewStr = ct.next_renew_at ? new Date(ct.next_renew_at).toLocaleDateString('zh-CN') + ' (自动)' : '自动续签 (到期前60天)';
        const acmePath = ct.acme_path || `/root/.acme.sh/${ct.domain}_ecc`;
        const acmeCertPath = `${acmePath}/${ct.domain}.cer`;
        const acmeKeyPath = `${acmePath}/${ct.domain}.key`;

        return `<div class="node-card cert-card">
            <div class="node-header">
                <span class="node-name">${this.escapeHtml(ct.domain)}</span>
                <button class="btn btn-danger btn-sm" onclick="app.deleteCertificate('${this.escapeHtml(ct.domain)}')">删除</button>
            </div>
            <div class="node-info">
                <div class="node-info-row">
                    <span class="node-info-label">到期时间</span>
                    <span class="node-info-value">${expiresStr}</span>
                </div>
                <div class="node-info-row">
                    <span class="node-info-label">续签时间</span>
                    <span class="node-info-value">${renewStr}</span>
                </div>
            </div>
            <div class="cert-paths" style="margin-top:var(--space-md);padding-top:var(--space-md);border-top:1px solid var(--border-color)">
                <div style="font-size:0.8rem;color:var(--text-secondary);margin-bottom:var(--space-sm)">📂 使用路径 (推荐用于伪装站点)</div>
                <div style="font-size:0.75rem;color:var(--text-muted);word-break:break-all">
                    <div>证书: ${ct.cert_path}</div>
                    <div>私钥: ${ct.key_path}</div>
                </div>
                <div style="font-size:0.8rem;color:var(--text-secondary);margin-top:var(--space-md);margin-bottom:var(--space-sm)">📂 原始路径 (acme.sh 源文件)</div>
                <div style="font-size:0.75rem;color:var(--text-muted);word-break:break-all">
                    <div>证书: ${acmeCertPath}</div>
                    <div>私钥: ${acmeKeyPath}</div>
                </div>
            </div>
            <div class="cert-warning" style="margin-top:var(--space-md);padding:var(--space-sm);background:rgba(245,158,11,0.1);border:1px solid rgba(245,158,11,0.3);border-radius:var(--radius-sm)">
                <div style="font-size:0.8rem;color:#f59e0b">⚠️ 为提升节点抗检测能力，请配置伪装站点</div>
                <div style="margin-top:var(--space-xs);font-size:0.75rem">
                    <a href="https://1panel.cn/docs/installation/online_installation/" target="_blank" style="color:var(--accent-primary);margin-right:var(--space-md)">📖 1Panel 教程</a>
                    <a href="https://www.bt.cn/new/index.html" target="_blank" style="color:var(--accent-primary)">📖 宝塔面板 教程</a>
                </div>
            </div>
        </div>`;
    }

    async deleteCertificate(domain) {
        if (!confirm(`确定要删除证书 "${domain}" 吗？\n\n注意：删除后需要重新申请证书。`)) return;
        try {
            await API.deleteCertificate(domain);
            this.showToast('证书已删除', 'success');
            this.loadCertificates();
        } catch (e) {
            this.showToast(e.message, 'error');
        }
    }

    async loadSystemInfo() {
        try {
            const s = await API.getSystemStatus();
            document.getElementById('os-info').textContent = s.os;
            document.getElementById('arch-info').textContent = s.arch;
            document.getElementById('hostname-info').textContent = s.hostname;
            document.getElementById('singbox-version').textContent = s.singbox_installed ? `v${s.singbox_version}` : '未安装';
            this.loadProxyDetection();
            this.loadCoreStatus();
            this.loadWarpStatus();
            this.setupWarpEventHandlers();
        } catch (e) {
            console.error('System info error:', e)
        }
    }

    async loadWarpStatus() {
        try {
            const s = await API.getWarpStatus();
            const statusEl = document.getElementById('warp-status');
            const ipv4Row = document.getElementById('warp-ipv4-row');
            const ipv6Row = document.getElementById('warp-ipv6-row');
            const typeRow = document.getElementById('warp-type-row');
            const registerBtn = document.getElementById('warp-register-btn');
            const refreshBtn = document.getElementById('warp-refresh-btn');
            const deleteBtn = document.getElementById('warp-delete-btn');
            const upgradeSection = document.getElementById('warp-upgrade-section');

            if (s.configured) {
                statusEl.innerHTML = '<span class="text-success">✓ 已配置</span>';
                document.getElementById('warp-ipv4').textContent = s.ipv4 || '-';
                document.getElementById('warp-ipv6').textContent = s.ipv6 ? (s.ipv6.length > 30 ? s.ipv6.substring(0, 30) + '...' : s.ipv6) : '-';
                document.getElementById('warp-type').textContent = s.account_type === 'plus' ? 'WARP+' : (s.account_type === 'teams' ? 'Zero Trust' : '免费');
                ipv4Row.style.display = s.ipv4 ? 'flex' : 'none';
                ipv6Row.style.display = s.ipv6 ? 'flex' : 'none';
                typeRow.style.display = 'flex';
                registerBtn.style.display = 'none';
                refreshBtn.style.display = 'inline-block';
                deleteBtn.style.display = 'inline-block';
                upgradeSection.style.display = s.account_type !== 'plus' ? 'block' : 'none';
            } else {
                statusEl.innerHTML = '<span class="text-muted">未配置</span>';
                ipv4Row.style.display = 'none';
                ipv6Row.style.display = 'none';
                typeRow.style.display = 'none';
                registerBtn.style.display = 'inline-block';
                refreshBtn.style.display = 'none';
                deleteBtn.style.display = 'none';
                upgradeSection.style.display = 'none';
            }
        } catch (e) {
            console.error('WARP status error:', e);
        }
    }

    setupWarpEventHandlers() {
        // 防止重复绑定事件监听器，避免多次弹窗
        if (this.warpEventHandlersInitialized) return;
        this.warpEventHandlersInitialized = true;
        
        document.getElementById('warp-register-btn')?.addEventListener('click', () => this.registerWarp());
        document.getElementById('warp-refresh-btn')?.addEventListener('click', () => this.refreshWarp());
        document.getElementById('warp-delete-btn')?.addEventListener('click', () => this.deleteWarp());
        document.getElementById('warp-upgrade-btn')?.addEventListener('click', () => this.upgradeWarp());
    }

    async registerWarp() {
        this.showToast('正在注册 WARP 账号...', 'info');
        try {
            await API.registerWarp();
            this.showToast('WARP 账号注册成功', 'success');
            this.loadWarpStatus();
        } catch (e) {
            this.showToast(e.message, 'error');
        }
    }

    async refreshWarp() {
        if (!confirm('更换节点将获取新的 WARP IP，确定继续？')) return;
        this.showToast('正在更换 WARP 节点...', 'info');
        try {
            await API.refreshWarp();
            this.showToast('WARP 节点已更换', 'success');
            this.loadWarpStatus();
        } catch (e) {
            this.showToast(e.message, 'error');
        }
    }

    async deleteWarp() {
        if (!confirm('确定要删除 WARP 配置吗？')) return;
        try {
            await API.deleteWarp();
            this.showToast('WARP 配置已删除', 'success');
            this.loadWarpStatus();
        } catch (e) {
            this.showToast(e.message, 'error');
        }
    }

    async upgradeWarp() {
        const key = document.getElementById('warp-license-key').value.trim();
        if (!key) {
            this.showToast('请输入 WARP+ License Key', 'error');
            return;
        }
        this.showToast('正在升级到 WARP+...', 'info');
        try {
            await API.upgradeWarp(key);
            this.showToast('升级成功', 'success');
            this.loadWarpStatus();
        } catch (e) {
            this.showToast(e.message, 'error');
        }
    }

    async loadCoreStatus() {
        try {
            const s = await API.getCoreStatus();
            const sc = document.getElementById('singbox-status');
            if (sc) {
                sc.innerHTML = s.singbox_installed ? `<span class="text-success">✓ 已安装 (v${s.singbox_version || '未知'})</span> <button class="btn btn-danger btn-sm" onclick="uninstallCore('singbox')">卸载</button>` : `<span class="text-muted">未安装</span> <button class="btn btn-primary btn-sm" onclick="installCore('singbox')">安装</button>`
            }
        } catch (e) {
            console.error('Core status error:', e)
        }
    }

    async installCore(c) {
        this.showToast(`正在安装 ${c}，请稍候...`, 'info');
        try {
            await API.installCore(c);
            this.showToast(`${c} 安装成功`, 'success');
            this.loadCoreStatus();
            this.loadSystemInfo()
        } catch (e) {
            this.showToast(e.message, 'error')
        }
    }

    async uninstallCore(c) {
        if (!confirm(`确定要卸载 ${c} 吗？`)) return;
        try {
            await API.uninstallCore(c);
            this.showToast(`${c} 已卸载`, 'success');
            this.loadCoreStatus();
            this.loadSystemInfo()
        } catch (e) {
            this.showToast(e.message, 'error')
        }
    }

    async loadProxyDetection() {
        const c = document.getElementById('proxy-detection');
        if (!c) return;
        c.innerHTML = '<div class="detection-loading">检测中...</div>';
        try {
            const r = await API.detectReverseProxy();
            let h = `<div class="info-row"><span class="info-label">Nginx</span><span class="info-value">${r.nginx_installed ? `已安装 (${r.nginx_version})` : '未安装'}</span></div><div class="info-row"><span class="info-label">OpenResty</span><span class="info-value">${r.openresty_installed ? `已安装 (${r.openresty_version})` : '未安装'}</span></div>`;
            if (r.panel_detected) h += `<div class="info-row"><span class="info-label">控制面板</span><span class="info-value">${r.panel_detected}</span></div>`;
            h += `<div class="detection-recommendation" style="margin-top:var(--space-md)">${r.recommendation}</div>`;
            h += `<button class="btn btn-secondary btn-sm show-guide-btn" onclick="showGuide()">📖 查看使用指南</button>`;
            c.innerHTML = h
        } catch {
            c.innerHTML = '<div class="detection-loading">检测失败</div>'
        }
    }

    async openCreateNodeModal() {
        this.openModal('create-node-modal');
        await this.loadCertificatesForDropdown();
        await this.loadServerIPs();
        // Hide IP binding group initially (shown when Reality is selected)
        const ipg = document.getElementById('ip-bind-group');
        if (ipg) ipg.style.display = 'none';
        // Hide port conflict warning
        document.getElementById('port-conflict-warning')?.classList.add('hidden');
        try {
            const r = await API.getRandomPort();
            document.getElementById('node-port').value = r.port
        } catch {
            document.getElementById('node-port').value = 10000 + Math.floor(Math.random() * 50000)
        }
    }

    async createNode() {
        const n = document.getElementById('node-name').value;
        const p = document.getElementById('node-protocol').value;
        const d = document.getElementById('node-domain').value;
        const pt = parseInt(document.getElementById('node-port').value);
        const listenIP = document.getElementById('node-ip-bind')?.value || '';

        // Include listen IP in config if specified
        const config = {};
        if (listenIP) {
            config.listen = listenIP;
        }

        try {
            await API.createNode({ name: n, protocol: p, domain: d, port: pt, config });
            this.showToast('节点创建成功', 'success');
            this.closeModal();
            document.getElementById('create-node-form').reset();
            if (this.currentPage === 'nodes') this.loadNodes();
            this.loadDashboardData()
        } catch (e) {
            this.showToast(e.message, 'error')
        }
    }

    async startNode(id) {
        this.showToast('正在启动节点...', 'info');
        try {
            await API.startNode(id);
            this.showToast('节点启动成功', 'success');
            this.loadNodes();
            this.loadDashboardData()
        } catch (e) {
            this.showToast(e.message, 'error')
        }
    }

    async toggleNodeWarp(id, enabled) {
        try {
            await API.toggleNodeWarp(id, enabled);
            this.showToast(enabled ? 'WARP 已开启' : 'WARP 已关闭', 'success');
            // Update local node state
            const node = this.nodes.find(n => n.id === id);
            if (node) node.warp_enabled = enabled ? 1 : 0;
            // If node is running, need to restart for WARP to take effect
            if (node && node.status === 'running') {
                this.showToast('需要重启节点以应用 WARP 设置', 'info');
            }
        } catch (e) {
            this.showToast(e.message, 'error');
            this.loadNodes(); // Reload to reset checkbox state
        }
    }

    async stopNode(id) {
        try {
            await API.stopNode(id);
            this.showToast('节点已停止', 'success');
            this.loadNodes();
            this.loadDashboardData()
        } catch (e) {
            this.showToast(e.message, 'error')
        }
    }

    async deleteNode(id) {
        if (!confirm('确定要删除这个节点吗？')) return;
        try {
            await API.deleteNode(id);
            this.showToast('节点已删除', 'success');
            this.loadNodes();
            this.loadDashboardData()
        } catch (e) {
            this.showToast(e.message, 'error')
        }
    }

    async applyCertificate() {
        const d = document.getElementById('cert-domain').value;
        const e = document.getElementById('cert-email').value;
        const p = document.getElementById('cert-provider').value;
        const m = document.getElementById('cert-method').value;
        const dp = document.getElementById('dns-provider')?.value || '';
        const t = document.getElementById('dns-token')?.value || '';
        const cfe = document.getElementById('cf-email')?.value || '';
        this.showToast('正在申请证书，请稍候...', 'info');
        try {
            await API.applyCertificate({ domain: d, email: e, provider: p, method: m, dns_provider: m === 'dns' ? dp : '', api_token: m === 'dns' ? t : '', cf_email: m === 'dns' && dp === 'cloudflare' ? cfe : '' });
            this.showToast('证书申请成功！将自动续签', 'success');
            this.closeModal();
            document.getElementById('apply-cert-form').reset();
            if (this.currentPage === 'certificates') this.loadCertificates();
            await this.loadCertificatesForDropdown();
        } catch (er) {
            this.showToast(er.message, 'error')
        }
    }

    openModal(id) {
        document.getElementById('modal-overlay').classList.remove('hidden');
        document.querySelectorAll('.modal').forEach(m => m.classList.add('hidden'));
        document.getElementById(id).classList.remove('hidden')
    }

    closeModal() {
        document.getElementById('modal-overlay').classList.add('hidden');
        document.querySelectorAll('.modal').forEach(m => m.classList.add('hidden'))
    }

    showToast(msg, type = 'info') {
        const c = document.getElementById('toast-container');
        const t = document.createElement('div');
        t.className = `toast ${type}`;
        t.innerHTML = `<span class="toast-message">${this.escapeHtml(msg)}</span><button class="toast-close">&times;</button>`;
        t.querySelector('.toast-close').addEventListener('click', () => t.remove());
        c.appendChild(t);
        setTimeout(() => {
            if (t.parentNode) {
                t.style.opacity = '0';
                t.style.transform = 'translateX(100%)';
                setTimeout(() => t.remove(), 300)
            }
        }, 5000)
    }

    escapeHtml(t) {
        const d = document.createElement('div');
        d.textContent = t;
        return d.innerHTML
    }

    // Generate functions for edit node modal
    generateUUID() {
        const uuid = 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, c => {
            const r = Math.random() * 16 | 0;
            const v = c === 'x' ? r : (r & 0x3 | 0x8);
            return v.toString(16);
        });
        document.getElementById('edit-config-uuid').value = uuid;
    }

    generatePassword() {
        const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
        let password = '';
        for (let i = 0; i < 16; i++) {
            password += chars.charAt(Math.floor(Math.random() * chars.length));
        }
        document.getElementById('edit-config-password').value = password;
    }

    generateShortId() {
        const hex = '0123456789abcdef';
        let shortId = '';
        for (let i = 0; i < 8; i++) { // 8位 Short ID
            shortId += hex.charAt(Math.floor(Math.random() * 16));
        }
        document.getElementById('edit-config-shortid').value = shortId;
    }

    generatePath() {
        const words = ['update', 'api', 'v1', 'v2', 'data', 'sync', 'stream', 'ws', 'socket', 'chat', 'msg', 'upload', 'download'];
        const word1 = words[Math.floor(Math.random() * words.length)];
        const word2 = words[Math.floor(Math.random() * words.length)];
        document.getElementById('edit-config-path').value = `/${word1}-${word2}`;
    }

    generateServiceName() {
        const prefixes = ['ShopService', 'ApiGateway', 'DataSync', 'UpdateService', 'StreamApi', 'AuthService', 'UserApi'];
        const suffix = Math.random().toString(36).substring(2, 6);
        const name = prefixes[Math.floor(Math.random() * prefixes.length)] + '_' + suffix;
        document.getElementById('edit-config-servicename').value = name;
    }
}

const app = new App();
