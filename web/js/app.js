// Alcove Dashboard — Single Page Application
(function () {
    'use strict';

    // ---------------------
    // API helper
    // ---------------------
    async function api(method, path, body) {
        const token = localStorage.getItem('alcove_token');
        const opts = {
            method,
            headers: {
                'Content-Type': 'application/json',
                'Authorization': 'Bearer ' + (token || '')
            }
        };
        if (body) opts.body = JSON.stringify(body);
        const resp = await fetch(path, opts);
        if (resp.status === 401) {
            showLogin();
            throw new Error('unauthorized');
        }
        return resp;
    }

    // ---------------------
    // DOM helpers
    // ---------------------
    const $ = (sel) => document.querySelector(sel);
    const $$ = (sel) => document.querySelectorAll(sel);

    function show(el) { el.hidden = false; }
    function hide(el) { el.hidden = true; }

    function escapeHtml(str) {
        const div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // ---------------------
    // State
    // ---------------------
    let refreshInterval = null;
    let durationInterval = null;
    let sseSource = null;
    let currentSessionId = null;
    let currentPage = 1;
    const perPage = 50;
    let editingScheduleId = null;
    let scheduleFromSession = null;
    let selectedProfiles = [];
    let allProfiles = [];
    let editingProfileId = null;
    let cachedCredentials = [];  // cached from last fetch for prerequisite checks
    let setupChecklistDismissed = localStorage.getItem('alcove_setup_dismissed') === 'true';

    // ---------------------
    // Auth
    // ---------------------
    function showLogin() {
        localStorage.removeItem('alcove_token');
        localStorage.removeItem('alcove_user');
        localStorage.removeItem('alcove_is_admin');
        show($('#login-view'));
        hide($('#dashboard-view'));
        stopRefresh();
        stopSSE();
    }

    function showDashboard() {
        hide($('#login-view'));
        show($('#dashboard-view'));
        const user = localStorage.getItem('alcove_user') || 'user';
        $('#user-info').textContent = user;
        // Reset loading states to prevent stale spinners after re-login
        hide($('#sessions-loading'));
        hide($('#schedules-loading'));
        hide($('#credentials-loading'));
        hide($('#tools-loading'));
        hide($('#profiles-loading'));
        hide($('#transcript-loading'));
        hide($('#proxy-log-loading'));
        // Refresh admin status from server and update UI
        api('GET', '/api/v1/auth/me').then(r => r.json()).then(data => {
            localStorage.setItem('alcove_is_admin', data.is_admin ? 'true' : 'false');
            updateAdminUI();
        }).catch(() => {});
        updateAdminUI(); // also call immediately with cached value
    }

    function isLoggedIn() {
        return !!localStorage.getItem('alcove_token');
    }

    function isAdmin() {
        return localStorage.getItem('alcove_is_admin') === 'true';
    }

    function updateAdminUI() {
        document.querySelectorAll('.nav-admin-only').forEach(el => {
            el.hidden = !isAdmin();
        });
    }

    // Login form
    $('#login-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const errEl = $('#login-error');
        hide(errEl);

        const username = $('#login-username').value.trim();
        const password = $('#login-password').value;

        if (!username || !password) {
            errEl.textContent = 'Please enter both username and password.';
            show(errEl);
            return;
        }

        try {
            const resp = await fetch('/api/v1/auth/login', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username, password })
            });

            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                errEl.textContent = data.error || data.message || 'Login failed. Check your credentials.';
                show(errEl);
                return;
            }

            const data = await resp.json();
            localStorage.setItem('alcove_token', data.token);
            localStorage.setItem('alcove_user', username);
            localStorage.setItem('alcove_is_admin', data.is_admin ? 'true' : 'false');
            // Force route handling — navigate may not trigger hashchange
            // if hash is already 'sessions' or empty
            window.location.hash = 'sessions';
            handleRoute();
        } catch (err) {
            errEl.textContent = 'Network error. Please try again.';
            show(errEl);
        }
    });

    // Clear login error on input focus
    $('#login-username').addEventListener('focus', () => hide($('#login-error')));
    $('#login-password').addEventListener('focus', () => hide($('#login-error')));

    // User dropdown toggle
    $('#user-dropdown-toggle').addEventListener('click', (e) => {
        e.stopPropagation();
        const menu = $('#user-dropdown-menu');
        menu.hidden = !menu.hidden;
    });

    // Close dropdown when clicking outside
    document.addEventListener('click', () => {
        hide($('#user-dropdown-menu'));
    });

    // Prevent menu clicks from closing
    $('#user-dropdown-menu').addEventListener('click', (e) => {
        e.stopPropagation();
    });

    // Logout
    $('#logout-btn').addEventListener('click', () => {
        hide($('#user-dropdown-menu'));
        showLogin();
        window.location.hash = '';
    });

    // Change Password modal
    $('#change-password-btn').addEventListener('click', () => {
        hide($('#user-dropdown-menu'));
        $('#change-password-form').reset();
        hide($('#cp-error'));
        hide($('#cp-success'));
        show($('#change-password-modal'));
    });

    $('#cp-cancel').addEventListener('click', () => {
        hide($('#change-password-modal'));
    });

    // Close modal on overlay click
    $('#change-password-modal').addEventListener('click', (e) => {
        if (e.target === e.currentTarget) hide($('#change-password-modal'));
    });

    // System LLM modal
    $('#system-llm-btn').addEventListener('click', function() {
        hide($('#user-dropdown-menu'));
        show($('#system-llm-modal'));
        loadSystemLLMModal();
    });

    $('#system-llm-close').addEventListener('click', function() {
        hide($('#system-llm-modal'));
    });

    $('#system-llm-modal').addEventListener('click', function(e) {
        if (e.target === e.currentTarget) hide(e.currentTarget);
    });

    // Change Password form submit
    $('#change-password-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const errEl = $('#cp-error');
        const successEl = $('#cp-success');
        hide(errEl);
        hide(successEl);

        const current = $('#cp-current').value;
        const newPw = $('#cp-new').value;
        const confirm = $('#cp-confirm').value;

        if (!current || !newPw || !confirm) {
            errEl.textContent = 'All fields are required.';
            show(errEl);
            return;
        }
        if (newPw.length < 8) {
            errEl.textContent = 'New password must be at least 8 characters.';
            show(errEl);
            return;
        }
        if (newPw !== confirm) {
            errEl.textContent = 'New passwords do not match.';
            show(errEl);
            return;
        }

        try {
            const resp = await api('PUT', '/api/v1/auth/password', {
                current_password: current,
                new_password: newPw
            });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                errEl.textContent = data.error || 'Failed to change password.';
                show(errEl);
                return;
            }
            successEl.textContent = 'Password changed successfully.';
            show(successEl);
            $('#change-password-form').reset();
            setTimeout(() => hide($('#change-password-modal')), 2000);
        } catch (err) {
            if (err.message !== 'unauthorized') {
                errEl.textContent = 'Failed to change password.';
                show(errEl);
            }
        }
    });

    // ---------------------
    // Routing
    // ---------------------
    function navigate(hash) {
        window.location.hash = hash;
    }

    function getRoute() {
        const hash = window.location.hash.replace(/^#\/?/, '');
        return hash || 'sessions';
    }

    function handleRoute() {
        if (!isLoggedIn()) {
            showLogin();
            return;
        }

        showDashboard();
        stopRefresh();
        stopSSE();

        const route = getRoute();
        const pages = ['sessions', 'task-new', 'schedules', 'credentials', 'profiles', 'tools', 'session-detail', 'users'];
        pages.forEach((p) => hide($('#page-' + p)));

        // Update active nav tab
        var navRoute = route.startsWith('session/') ? 'sessions' : route;
        if (navRoute === 'tools' || navRoute === 'tools-admin') navRoute = 'profiles';
        $$('.nav-tab').forEach((tab) => {
            tab.classList.toggle('active', tab.dataset.tab === navRoute);
        });

        if (route === 'sessions') {
            show($('#page-sessions'));
            loadSessions();
            updateSetupChecklist();
        } else if (route === 'task/new') {
            show($('#page-task-new'));
            hide($('#task-error'));
            hide($('#task-success'));
            hide($('#task-warnings'));
            loadProviders();
            loadTaskProfiles();
        } else if (route === 'schedules') {
            show($('#page-schedules'));
            loadSchedules();
            loadScheduleProviders();
            if (scheduleFromSession) {
                openScheduleForm(scheduleFromSession);
                scheduleFromSession = null;
            }
        } else if (route === 'credentials') {
            show($('#page-credentials'));
            loadCredentials();
        } else if (route === 'profiles') {
            show($('#page-profiles'));
            loadProfilesPage();
        } else if (route === 'tools' || route === 'tools-admin') {
            show($('#page-tools'));
            loadToolsPage();
        } else if (route.startsWith('session/')) {
            const id = route.replace('session/', '');
            show($('#page-session-detail'));
            loadSessionDetail(id);
        } else if (route === 'users') {
            if (!isAdmin()) { navigate('sessions'); return; }
            show($('#page-users'));
            loadUsers();
        } else {
            show($('#page-sessions'));
            loadSessions();
            updateSetupChecklist();
        }
    }

    window.addEventListener('hashchange', handleRoute);

    // ---------------------
    // Sessions list
    // ---------------------
    async function loadSessions(silent) {
        const tbody = $('#sessions-tbody');
        const loading = $('#sessions-loading');
        const empty = $('#sessions-empty');

        if (!silent) {
            tbody.innerHTML = '';
            show(loading);
            hide(empty);
        }

        try {
            const resp = await api('GET', '/api/v1/sessions?page=' + currentPage + '&per_page=' + perPage);
            const data = await resp.json();
            hide(loading);

            const sessions = Array.isArray(data) ? data : (data.sessions || data.items || []);
            if (sessions.length === 0) {
                show(empty);
                return;
            }

            renderSessions(sessions);
            renderPagination(data.page, data.pages, data.total);
            startAutoRefresh();
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--status-error);">Failed to load sessions. Check your connection and try again.</td></tr>';
            }
        }
    }

    function renderSessions(sessions) {
        const tbody = $('#sessions-tbody');
        const statusFilter = $('#filter-status').value;
        const searchFilter = $('#filter-search').value.toLowerCase();

        const filtered = sessions.filter((s) => {
            if (statusFilter && s.status !== statusFilter) return false;
            if (searchFilter) {
                const text = (s.id + ' ' + (s.prompt || '') + ' ' + (s.provider || '')).toLowerCase();
                if (!text.includes(searchFilter)) return false;
            }
            return true;
        });

        const empty = $('#sessions-empty');
        if (filtered.length === 0) {
            tbody.innerHTML = '';
            show(empty);
            return;
        }
        hide(empty);

        tbody.innerHTML = filtered.map((s) => {
            const idShort = (s.id || '').substring(0, 12);
            const submitter = s.submitter || '-';
            const status = s.status || 'unknown';
            const provider = s.provider || '-';
            const duration = formatDuration(s.started_at, s.finished_at, s.duration);
            const prompt = truncate(s.prompt || s.task_prompt || '-', 80);

            return '<tr class="clickable" data-session-id="' + escapeHtml(s.id) + '" tabindex="0" role="link">' +
                '<td class="mono" title="' + escapeHtml(s.id) + '">' + escapeHtml(idShort) + '</td>' +
                '<td>' + escapeHtml(submitter) + '</td>' +
                '<td><span class="badge badge-' + escapeHtml(status) + '">' + escapeHtml(status) + '</span></td>' +
                '<td>' + escapeHtml(provider) + '</td>' +
                '<td class="mono">' + escapeHtml(duration) + '</td>' +
                '<td class="truncate">' + escapeHtml(prompt) + '</td>' +
                '</tr>';
        }).join('');

        // Click and keyboard handlers
        tbody.querySelectorAll('tr.clickable').forEach((row) => {
            row.addEventListener('click', () => {
                navigate('session/' + row.dataset.sessionId);
            });
            row.addEventListener('keydown', (e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    navigate('session/' + row.dataset.sessionId);
                }
            });
        });
    }

    function renderPagination(page, pages, total) {
        let paginationEl = $('#sessions-pagination');
        if (!paginationEl) {
            paginationEl = document.createElement('div');
            paginationEl.id = 'sessions-pagination';
            paginationEl.style.cssText = 'display:flex;justify-content:center;align-items:center;gap:12px;padding:16px 0;color:var(--text-muted);';
            // Insert after the table container
            const tableContainer = $('#page-sessions .table-container');
            tableContainer.parentNode.insertBefore(paginationEl, tableContainer.nextSibling);
        }
        if (pages <= 1) {
            paginationEl.innerHTML = '<span>' + total + ' session' + (total !== 1 ? 's' : '') + '</span>';
            return;
        }
        paginationEl.innerHTML =
            '<button class="btn btn-small btn-outline" id="prev-page"' + (page <= 1 ? ' disabled' : '') + '>&laquo; Previous</button>' +
            '<span>Page ' + page + ' of ' + pages + ' (' + total + ' total)</span>' +
            '<button class="btn btn-small btn-outline" id="next-page"' + (page >= pages ? ' disabled' : '') + '>Next &raquo;</button>';

        const prev = $('#prev-page');
        const next = $('#next-page');
        if (prev && !prev.disabled) {
            prev.addEventListener('click', () => { currentPage--; loadSessions(); });
        }
        if (next && !next.disabled) {
            next.addEventListener('click', () => { currentPage++; loadSessions(); });
        }
    }

    // Filters
    $('#filter-status').addEventListener('change', () => { currentPage = 1; loadSessions(); });
    $('#filter-search').addEventListener('input', debounce(() => { currentPage = 1; loadSessions(); }, 300));

    function startAutoRefresh() {
        stopRefresh();
        refreshInterval = setInterval(() => {
            if (getRoute() === 'sessions') loadSessions(true);
        }, 10000);
    }

    function stopRefresh() {
        if (refreshInterval) {
            clearInterval(refreshInterval);
            refreshInterval = null;
        }
        if (durationInterval) {
            clearInterval(durationInterval);
            durationInterval = null;
        }
    }

    // ---------------------
    // New Task
    // ---------------------
    async function loadProviders() {
        const select = $('#task-provider');
        try {
            const resp = await api('GET', '/api/v1/credentials');
            const data = await resp.json();
            const allCreds = data.credentials || [];
            cachedCredentials = allCreds;
            // Only show LLM credentials in the provider dropdown
            const llmCreds = allCreds.filter(function (c) {
                return c.provider !== 'github' && c.provider !== 'gitlab';
            });
            select.innerHTML = '<option value="">Select a provider</option>';
            llmCreds.forEach((c) => {
                const label = c.name + ' (' + (c.provider === 'google-vertex' ? 'Vertex AI' : 'Anthropic') + ')';
                select.innerHTML += '<option value="' + escapeHtml(c.name) + '">' + escapeHtml(label) + '</option>';
            });
            if (llmCreds.length === 1) select.selectedIndex = 1;
            checkTaskPrerequisites();
        } catch (err) {
            if (err.message !== 'unauthorized') {
                select.innerHTML = '<option value="">Failed to load providers</option>';
            }
        }
    }

    // Timeout slider
    $('#task-timeout').addEventListener('input', (e) => {
        $('#timeout-value').textContent = e.target.value;
    });

    // Submit task
    $('#task-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const errEl = $('#task-error');
        const successEl = $('#task-success');
        hide(errEl);
        hide(successEl);

        const prompt = $('#task-prompt').value.trim();
        if (!prompt) {
            errEl.textContent = 'Prompt is required.';
            show(errEl);
            return;
        }

        // Block submission if no LLM credential
        var llmCreds = cachedCredentials.filter(function (c) {
            return c.provider !== 'github' && c.provider !== 'gitlab';
        });
        if (llmCreds.length === 0) {
            errEl.textContent = 'No LLM provider configured. Add an LLM credential on the Credentials page before submitting tasks.';
            show(errEl);
            return;
        }

        const payload = {
            prompt: prompt,
            provider: $('#task-provider').value || undefined,
            repo: $('#task-repo').value.trim() || undefined,
            timeout: parseInt($('#task-timeout').value, 10) * 60,
            debug: $('#task-debug').checked,
            profiles: selectedProfiles.length > 0 ? selectedProfiles : undefined
        };

        // Remove undefined keys
        Object.keys(payload).forEach((k) => {
            if (payload[k] === undefined) delete payload[k];
        });

        const btn = e.target.querySelector('button[type="submit"]');
        btn.disabled = true;
        btn.textContent = 'Submitting...';

        try {
            const resp = await api('POST', '/api/v1/tasks', payload);
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                throw new Error(data.error || data.message || 'Failed to submit task.');
            }
            const data = await resp.json();
            const sessionId = data.session_id || data.id || '';

            successEl.innerHTML = 'Task submitted! Session ID: <span class="mono">' +
                escapeHtml(sessionId) + '</span> &mdash; ' +
                '<a href="#session/' + escapeHtml(sessionId) + '" style="color:var(--status-running)">Watch live</a>';
            show(successEl);

            // Auto-navigate to the new session after a short delay
            setTimeout(() => {
                navigate('session/' + sessionId);
            }, 1500);

            // Reset form
            $('#task-prompt').value = '';
            $('#task-repo').value = '';
            $('#task-timeout').value = '60';
            $('#timeout-value').textContent = '60';
            $('#task-debug').checked = false;
            selectedProfiles = [];
            renderProfileChips();
            updateEffectivePermissions();
        } catch (err) {
            if (err.message !== 'unauthorized') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Submit Task';
        }
    });

    // ---------------------
    // Credentials
    // ---------------------
    async function loadCredentials() {
        const tbodyLlm = $('#credentials-tbody-llm');
        const tbodyScm = $('#credentials-tbody-scm');
        const sectionLlm = $('#credentials-section-llm');
        const sectionScm = $('#credentials-section-scm');
        const loading = $('#credentials-loading');
        const empty = $('#credentials-empty');

        tbodyLlm.innerHTML = '';
        tbodyScm.innerHTML = '';
        hide(sectionLlm);
        hide(sectionScm);
        show(loading);
        hide(empty);

        try {
            const resp = await api('GET', '/api/v1/credentials');
            const data = await resp.json();
            hide(loading);

            const credentials = Array.isArray(data) ? data : (data.credentials || data.items || []);
            if (credentials.length === 0) {
                show(empty);
                return;
            }

            // Split credentials into LLM and SCM groups
            var llmCreds = [];
            var scmCreds = [];
            credentials.forEach(function (c) {
                if (c.provider === 'github' || c.provider === 'gitlab') {
                    scmCreds.push(c);
                } else {
                    llmCreds.push(c);
                }
            });

            function renderLlmRow(c) {
                const name = c.name || '-';
                const provider = c.provider === 'vertex' ? 'Vertex AI' : (c.provider === 'anthropic' ? 'Anthropic' : escapeHtml(c.provider || '-'));
                var authBadge = '';
                if (c.auth_type === 'api_key') {
                    authBadge = '<span class="badge">API Key</span>';
                } else if (c.auth_type === 'service_account') {
                    authBadge = '<span class="badge badge-running">Service Account</span>';
                } else if (c.auth_type === 'adc') {
                    authBadge = '<span class="badge badge-completed">ADC</span>';
                } else {
                    authBadge = '<span class="badge">' + escapeHtml(c.auth_type || '-') + '</span>';
                }
                var projectRegion = '-';
                if (c.project_id || c.region) {
                    projectRegion = escapeHtml(c.project_id || '') + (c.region ? ' / ' + escapeHtml(c.region) : '');
                }
                const created = formatTime(c.created_at || c.created);
                const id = c.id || '';
                return '<tr>' +
                    '<td>' + escapeHtml(name) + '</td>' +
                    '<td>' + provider + '</td>' +
                    '<td>' + authBadge + '</td>' +
                    '<td>' + projectRegion + '</td>' +
                    '<td>' + escapeHtml(created) + '</td>' +
                    '<td>' +
                        '<button class="btn btn-small btn-outline delete-credential-btn" data-id="' + escapeHtml(id) + '" data-name="' + escapeHtml(name) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button>' +
                    '</td>' +
                    '</tr>';
            }

            function renderScmRow(c) {
                const name = c.name || '-';
                const provider = c.provider === 'github' ? 'GitHub' : (c.provider === 'gitlab' ? 'GitLab' : escapeHtml(c.provider || '-'));
                var authBadge = '<span class="badge">PAT</span>';
                var host = '-';
                if (c.provider === 'gitlab' && c.gitlab_host) {
                    host = escapeHtml(c.gitlab_host);
                } else if (c.provider === 'github') {
                    host = 'github.com';
                } else if (c.provider === 'gitlab') {
                    host = 'gitlab.com';
                }
                const created = formatTime(c.created_at || c.created);
                const id = c.id || '';
                return '<tr>' +
                    '<td>' + escapeHtml(name) + '</td>' +
                    '<td>' + provider + '</td>' +
                    '<td>' + authBadge + '</td>' +
                    '<td>' + host + '</td>' +
                    '<td>' + escapeHtml(created) + '</td>' +
                    '<td>' +
                        '<button class="btn btn-small btn-outline delete-credential-btn" data-id="' + escapeHtml(id) + '" data-name="' + escapeHtml(name) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button>' +
                    '</td>' +
                    '</tr>';
            }

            if (llmCreds.length > 0) {
                show(sectionLlm);
                tbodyLlm.innerHTML = llmCreds.map(renderLlmRow).join('');
            }

            if (scmCreds.length > 0) {
                show(sectionScm);
                tbodyScm.innerHTML = scmCreds.map(renderScmRow).join('');
            }

            // Delete handlers for both sections
            var container = $('#credentials-grouped-container');
            container.querySelectorAll('.delete-credential-btn').forEach(function (btn) {
                btn.addEventListener('click', async function () {
                    const id = btn.dataset.id;
                    const name = btn.dataset.name;
                    if (!confirm('Are you sure you want to delete credential "' + name + '"? This cannot be undone.')) return;
                    btn.disabled = true;
                    try {
                        const resp = await api('DELETE', '/api/v1/credentials/' + id);
                        if (!resp.ok) {
                            const data = await resp.json().catch(function () { return {}; });
                            alert(data.error || data.message || 'Failed to delete credential.');
                            btn.disabled = false;
                        } else {
                            loadCredentials();
                        }
                    } catch (err) {
                        if (err.message !== 'unauthorized') {
                            alert('Failed to delete credential.');
                        }
                        btn.disabled = false;
                    }
                });
            });
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                tbodyLlm.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--status-error);">Failed to load credentials. Check your connection and try again.</td></tr>';
                show(sectionLlm);
            }
        }
    }

    // ---------------------
    // Shared credential form (template-based)
    // ---------------------
    function initCredentialForm(container, options) {
        var template = document.getElementById('credential-form-template');
        var clone = template.content.cloneNode(true);
        container.innerHTML = '';
        container.appendChild(clone);

        var q = function(role) { return container.querySelector('[data-role="' + role + '"]'); };

        // Hide name field if not needed
        if (!options.showName) {
            var nameGroup = q('cred-name-group');
            if (nameGroup) nameGroup.hidden = true;
        }

        // Set submit button text
        var submitBtn = q('cred-submit');
        if (submitBtn) submitBtn.textContent = options.submitLabel || 'Save';

        // Wire up provider toggle
        var providerSelect = q('cred-provider');
        var anthropicFields = q('cred-anthropic-fields');
        var vertexFields = q('cred-vertex-fields');
        var scmFields = q('cred-scm-fields');
        var gitlabHostGroup = q('cred-gitlab-host-group');

        providerSelect.addEventListener('change', function() {
            var val = this.value;

            // Hide all
            if (anthropicFields) anthropicFields.hidden = true;
            if (vertexFields) vertexFields.hidden = true;
            if (scmFields) scmFields.hidden = true;

            if (val === 'anthropic') {
                if (anthropicFields) anthropicFields.hidden = false;
            } else if (val === 'google-vertex') {
                if (vertexFields) vertexFields.hidden = false;
            } else if (val === 'github' || val === 'gitlab') {
                if (scmFields) scmFields.hidden = false;
                // Show GitLab host field only for GitLab
                if (gitlabHostGroup) gitlabHostGroup.hidden = (val !== 'gitlab');
            }
        });

        // Wire up auth type help text toggle
        var authType = q('cred-auth-type');
        if (authType) {
            authType.addEventListener('change', function() {
                var saHelp = q('cred-json-help-sa');
                var adcHelp = q('cred-json-help-adc');
                if (saHelp) saHelp.hidden = this.value === 'adc';
                if (adcHelp) adcHelp.hidden = this.value !== 'adc';

                // Sync guide tabs
                container.querySelectorAll('.vertex-guide-tab').forEach(function(t) { t.classList.remove('active'); });
                container.querySelectorAll('.vertex-guide-panel').forEach(function(p) { p.hidden = true; });
                var tabSelector = this.value === 'adc' ? 'adc' : 'sa';
                var activeTab = container.querySelector('[data-guide-tab="' + tabSelector + '"]');
                var activePanel = q('guide-panel-' + tabSelector);
                if (activeTab) activeTab.classList.add('active');
                if (activePanel) activePanel.hidden = false;
            });
        }

        // Wire up file upload
        var fileInput = q('cred-json-file');
        var jsonTextarea = q('cred-json');
        if (fileInput && jsonTextarea) {
            fileInput.addEventListener('change', function(e) {
                var file = e.target.files[0];
                if (!file) return;
                var reader = new FileReader();
                reader.onload = function(ev) { jsonTextarea.value = ev.target.result; };
                reader.readAsText(file);
            });
        }

        // Wire up guide tabs
        container.querySelectorAll('.vertex-guide-tab').forEach(function(tab) {
            tab.addEventListener('click', function() {
                container.querySelectorAll('.vertex-guide-tab').forEach(function(t) { t.classList.remove('active'); });
                container.querySelectorAll('.vertex-guide-panel').forEach(function(p) { p.hidden = true; });
                tab.classList.add('active');
                var panel = q('guide-panel-' + tab.dataset.guideTab);
                if (panel) panel.hidden = false;
            });
        });

        // Wire up form submit
        var form = container.querySelector('form');
        var errorEl = q('credential-form-error');

        form.addEventListener('submit', async function(e) {
            e.preventDefault();
            if (errorEl) hide(errorEl);

            var name = options.showName ? (q('cred-name') || {}).value?.trim() : '_system';
            var provider = providerSelect.value;

            if (options.showName && !name) {
                if (errorEl) { errorEl.textContent = 'Name is required.'; show(errorEl); }
                return;
            }
            if (!provider) {
                if (errorEl) { errorEl.textContent = 'Provider is required.'; show(errorEl); }
                return;
            }

            var payload = { name: name, provider: provider };

            if (provider === 'anthropic') {
                var apiKey = (q('cred-api-key') || {}).value?.trim();
                if (!apiKey) { if (errorEl) { errorEl.textContent = 'API key is required.'; show(errorEl); } return; }
                payload.auth_type = 'api_key';
                payload.credential = apiKey;
            } else if (provider === 'google-vertex') {
                var at = authType ? authType.value : 'service_account';
                var jsonVal = jsonTextarea ? jsonTextarea.value.trim() : '';
                var projectId = (q('cred-project-id') || {}).value?.trim();
                var region = (q('cred-region') || {}).value?.trim();

                if (!jsonVal) { if (errorEl) { errorEl.textContent = 'Credential JSON is required.'; show(errorEl); } return; }
                try { JSON.parse(jsonVal); } catch(parseErr) { if (errorEl) { errorEl.textContent = 'Invalid JSON.'; show(errorEl); } return; }
                if (!projectId) { if (errorEl) { errorEl.textContent = 'Project ID is required.'; show(errorEl); } return; }
                if (!region) { if (errorEl) { errorEl.textContent = 'Region is required.'; show(errorEl); } return; }

                payload.auth_type = at;
                payload.credential = jsonVal;
                payload.project_id = projectId;
                payload.region = region;
            } else if (provider === 'github' || provider === 'gitlab') {
                var pat = (q('cred-pat') || {}).value?.trim();
                if (!pat) {
                    if (errorEl) { errorEl.textContent = 'Personal access token is required.'; show(errorEl); }
                    return;
                }
                payload.auth_type = 'pat';
                payload.credential = pat;

                // For GitLab, include api_host if specified
                if (provider === 'gitlab') {
                    var gitlabHost = (q('cred-gitlab-host') || {}).value?.trim();
                    if (gitlabHost) {
                        payload.api_host = gitlabHost;
                    }
                }
            }

            var btn = q('cred-submit');
            if (btn) { btn.disabled = true; btn.textContent = 'Saving...'; }

            try {
                await options.onSubmit(payload);
                form.reset();
                if (options.showName) container.hidden = true;
            } catch(err) {
                if (errorEl) { errorEl.textContent = err.message || 'Failed to save.'; show(errorEl); }
            } finally {
                if (btn) { btn.disabled = false; btn.textContent = options.submitLabel || 'Save'; }
            }
        });

        // Wire up cancel
        var cancelBtn = q('cred-cancel');
        if (cancelBtn) {
            cancelBtn.addEventListener('click', function() {
                form.reset();
                if (options.onCancel) options.onCancel();
            });
        }
    }

    // Show create credential form (Credentials page)
    $('#show-create-credential').addEventListener('click', function () {
        var container = $('#credential-form-container');
        show(container);
        initCredentialForm(container, {
            showName: true,
            submitLabel: 'Add Credential',
            onSubmit: async function(payload) {
                var resp = await api('POST', '/api/v1/credentials', payload);
                if (!resp.ok) {
                    var data = await resp.json().catch(function () { return {}; });
                    throw new Error(data.error || data.message || 'Failed to add credential.');
                }
                hide(container);
                loadCredentials();
            },
            onCancel: function() {
                hide(container);
            }
        });
    });

    // ---------------------
    // Session detail
    // ---------------------
    async function loadSessionDetail(id) {
        currentSessionId = id;
        $('#detail-title').textContent = 'Session ' + id.substring(0, 12);

        // Clean up dynamically added sections from previous render
        document.querySelectorAll('.session-prompt-section, .session-artifacts-section').forEach((el) => el.remove());
        // Remove any previously added cancel button
        const header = $('#page-session-detail .page-header');
        header.querySelectorAll('.btn').forEach((btn) => {
            if (btn.id !== 'back-to-sessions') btn.remove();
        });

        // Load session metadata
        try {
            const resp = await api('GET', '/api/v1/sessions/' + id);
            const session = await resp.json();
            renderSessionMeta(session);
            loadTranscript(id, session.status);
            loadProxyLog(id);

            // Auto-refresh while running
            if (session.status === 'running') {
                stopRefresh();
                refreshInterval = setInterval(async () => {
                    if (getRoute() !== 'session/' + id) return;
                    try {
                        const r = await api('GET', '/api/v1/sessions/' + id);
                        if (!r.ok) return;
                        const s = await r.json();
                        renderSessionMeta(s);
                        // Reload transcript and proxy log
                        loadTranscript(id, s.status, true);
                        loadProxyLog(id, true);
                        // Stop polling when session finishes, do a final data reload
                        if (s.status !== 'running') {
                            stopRefresh();
                            // Final reload after a delay (Gate flushes logs every 30s)
                            setTimeout(() => {
                                loadTranscript(id, s.status);
                                loadProxyLog(id);
                            }, 2000);
                        }
                    } catch (e) { /* ignore silent refresh errors */ }
                }, 5000);
            }
        } catch (err) {
            // Hide transcript and proxy log spinners since those loads will not run
            hide($('#transcript-loading'));
            hide($('#proxy-log-loading'));
            if (err.message !== 'unauthorized') {
                $('#session-meta').innerHTML = '<div class="meta-card"><div class="meta-value" style="color:var(--status-error)">Failed to load session.</div></div>';
            }
        }
    }

    function renderSessionMeta(s) {
        // Clean up dynamically added sections from previous render
        document.querySelectorAll('.session-prompt-section, .session-artifacts-section').forEach((el) => el.remove());
        const headerEl = $('#page-session-detail .page-header');
        headerEl.querySelectorAll('.btn').forEach((btn) => {
            if (btn.id !== 'back-to-sessions') btn.remove();
        });

        const fields = [
            { label: 'ID', value: s.id || '-' },
            { label: 'Submitter', value: s.submitter || '-' },
            { label: 'Status', value: s.status || '-', badge: true },
            { label: 'Provider', value: s.provider || '-' },
            { label: 'Started', value: formatTime(s.started_at) },
            { label: 'Finished', value: formatTime(s.finished_at) },
            { label: 'Duration', value: formatDuration(s.started_at, s.finished_at, s.duration) },
            { label: 'Exit Code', value: s.exit_code !== undefined && s.exit_code !== null ? String(s.exit_code) : '-' }
        ];

        let html = fields.map((f) => {
            let valueHtml;
            if (f.badge && f.value !== '-') {
                valueHtml = '<span class="badge badge-' + escapeHtml(f.value) + '">' + escapeHtml(f.value) + '</span>';
            } else {
                valueHtml = '<span>' + escapeHtml(f.value) + '</span>';
            }
            return '<div class="meta-card">' +
                '<div class="meta-label">' + escapeHtml(f.label) + '</div>' +
                '<div class="meta-value">' + valueHtml + '</div>' +
                '</div>';
        }).join('');

        $('#session-meta').innerHTML = html;

        // Cancel button for running sessions
        if (s.status === 'running') {
            const cancelBtn = document.createElement('button');
            cancelBtn.className = 'btn btn-small btn-outline';
            cancelBtn.style.cssText = 'margin-left: 1rem; color: var(--status-error); border-color: var(--status-error);';
            cancelBtn.textContent = 'Cancel Session';
            cancelBtn.addEventListener('click', async () => {
                if (!confirm('Are you sure you want to cancel this session? The running task will be terminated.')) return;
                cancelBtn.disabled = true;
                cancelBtn.textContent = 'Cancelling...';
                try {
                    const resp = await api('DELETE', '/api/v1/sessions/' + s.id);
                    if (resp.ok) {
                        loadSessionDetail(s.id);
                    } else {
                        const data = await resp.json().catch(() => ({}));
                        alert(data.error || data.message || 'Failed to cancel session.');
                        cancelBtn.disabled = false;
                        cancelBtn.textContent = 'Cancel Session';
                    }
                } catch (err) {
                    if (err.message !== 'unauthorized') {
                        alert('Failed to cancel session.');
                        cancelBtn.disabled = false;
                        cancelBtn.textContent = 'Cancel Session';
                    }
                }
            });
            const header = $('#page-session-detail .page-header');
            header.appendChild(cancelBtn);
        }

        // "Schedule This" button for non-running sessions
        if (s.status !== 'running') {
            const schedBtn = document.createElement('button');
            schedBtn.className = 'btn btn-small btn-outline';
            schedBtn.textContent = 'Schedule This';
            schedBtn.addEventListener('click', function () {
                scheduleFromSession = {
                    prompt: s.prompt || s.task_prompt || '',
                    provider: s.provider || '',
                    repo: s.repo || '',
                    timeout: s.timeout ? Math.round(s.timeout / 60) : 60,
                    debug: s.debug || false,
                };
                navigate('schedules');
            });
            const header = $('#page-session-detail .page-header');
            header.appendChild(schedBtn);
        }

        // Prompt section
        const prompt = s.prompt || s.task_prompt || '';
        if (prompt) {
            const promptSection = document.createElement('div');
            promptSection.className = 'session-prompt-section';
            promptSection.innerHTML = '<h3>Prompt</h3>' +
                '<pre class="session-prompt-pre">' +
                escapeHtml(prompt) + '</pre>';
            $('#session-meta').parentNode.insertBefore(promptSection, $('#session-meta').nextSibling);
        }

        // Artifacts section
        if (s.artifacts && s.artifacts.length > 0) {
            const artifactsSection = document.createElement('div');
            artifactsSection.className = 'session-artifacts-section';
            let artifactsHtml = '<h3>Artifacts</h3><ul>';
            s.artifacts.forEach((a) => {
                const label = escapeHtml(a.type || 'artifact') + ': ' + escapeHtml(a.ref || a.url || '-');
                if (a.url) {
                    artifactsHtml += '<li><a href="' + escapeHtml(a.url) + '" target="_blank" rel="noopener">' + label + '</a></li>';
                } else {
                    artifactsHtml += '<li>' + label + '</li>';
                }
            });
            artifactsHtml += '</ul>';
            artifactsSection.innerHTML = artifactsHtml;
            // Insert after prompt section if it exists, otherwise after meta
            const promptEl = document.querySelector('.session-prompt-section');
            const insertAfter = promptEl || $('#session-meta');
            insertAfter.parentNode.insertBefore(artifactsSection, insertAfter.nextSibling);
        }

        // Live-counting duration for running sessions
        if (durationInterval) {
            clearInterval(durationInterval);
            durationInterval = null;
        }
        if (s.status === 'running' && s.started_at) {
            durationInterval = setInterval(() => {
                const durationCards = $('#session-meta').querySelectorAll('.meta-card');
                // Duration is the 7th field (index 6)
                if (durationCards.length > 6) {
                    const valueEl = durationCards[6].querySelector('.meta-value span');
                    if (valueEl) {
                        valueEl.textContent = formatDuration(s.started_at, null, null);
                    }
                }
            }, 1000);
        }
    }

    // Detail tabs
    $$('.detail-tab').forEach((tab) => {
        tab.addEventListener('click', () => {
            $$('.detail-tab').forEach((t) => {
                t.classList.remove('active');
                t.setAttribute('aria-selected', 'false');
            });
            tab.classList.add('active');
            tab.setAttribute('aria-selected', 'true');

            const target = tab.dataset.detailTab;
            if (target === 'transcript') {
                show($('#detail-transcript'));
                hide($('#detail-proxy-log'));
            } else {
                hide($('#detail-transcript'));
                show($('#detail-proxy-log'));
                // Re-load proxy log if content is missing or empty
                const proxyTbody = $('#proxy-log-tbody');
                if (!proxyTbody.innerHTML.trim() || proxyTbody.querySelector('td[colspan]')) {
                    loadProxyLog(currentSessionId);
                }
            }
        });
    });

    // Back button
    $('#back-to-sessions').addEventListener('click', () => {
        navigate('sessions');
    });

    // ---------------------
    // Transcript
    // ---------------------
    async function loadTranscript(id, status, silent) {
        const content = $('#transcript-content');
        const loading = $('#transcript-loading');
        if (!silent) {
            content.innerHTML = '';
            show(loading);
        }

        // If running, try SSE
        if (status === 'running') {
            stopSSE();
            try {
                const token = localStorage.getItem('alcove_token');
                sseSource = new EventSource('/api/v1/sessions/' + id + '/transcript?stream=true&token=' + encodeURIComponent(token));

                // Safety timeout: if SSE hasn't connected in 5s, hide spinner and fall back
                let sseConnected = false;
                const sseTimeout = setTimeout(() => {
                    if (!sseConnected) {
                        hide(loading);
                        stopSSE();
                        fetchTranscript(id, content, loading);
                    }
                }, 5000);

                sseSource.onopen = () => {
                    sseConnected = true;
                    clearTimeout(sseTimeout);
                    hide(loading);
                    showLiveIndicator();
                };

                sseSource.onmessage = (event) => {
                    sseConnected = true;
                    clearTimeout(sseTimeout);
                    hide(loading);
                    showLiveIndicator();
                    const isAtBottom = content.scrollHeight - content.scrollTop - content.clientHeight < 50;
                    try {
                        const ev = JSON.parse(event.data);
                        appendTranscriptEvent(content, ev);
                    } catch (e) {
                        // Plain text event
                        appendTranscriptEvent(content, { type: 'system', content: event.data });
                    }
                    if (isAtBottom) {
                        content.scrollTop = content.scrollHeight;
                    }
                };

                sseSource.onerror = () => {
                    sseConnected = true;
                    clearTimeout(sseTimeout);
                    hideLiveIndicator();
                    stopSSE();
                    // Fall back to polling
                    fetchTranscript(id, content, loading);
                };

                // Handle status updates from server
                sseSource.addEventListener('status', function(event) {
                    try {
                        var update = JSON.parse(event.data);
                        // Update status badge in session meta
                        var badges = document.querySelectorAll('#session-meta .badge');
                        badges.forEach(function(badge) {
                            if (badge.classList.contains('badge-running') || badge.classList.contains('badge-completed') ||
                                badge.classList.contains('badge-error') || badge.classList.contains('badge-cancelled') ||
                                badge.classList.contains('badge-timeout')) {
                                badge.className = 'badge badge-' + (update.status || update.Status || '');
                                badge.textContent = (update.status || update.Status || '').toUpperCase();
                            }
                        });
                    } catch (e) { /* ignore */ }
                });

                // Handle session completion
                sseSource.addEventListener('done', function(event) {
                    hideLiveIndicator();
                    stopSSE();
                    // Reload session detail after a short delay to get final state
                    try {
                        var data = JSON.parse(event.data);
                        var finalStatus = data.status || 'completed';
                        // Show a brief completion message in the transcript
                        if (content) {
                            var notice = document.createElement('div');
                            notice.className = 'tx-system';
                            notice.innerHTML = '<span class="tx-system-icon">&#10003;</span> Session ' + escapeHtml(finalStatus);
                            content.appendChild(notice);
                            content.scrollTop = content.scrollHeight;
                        }
                    } catch (e) { /* ignore */ }
                    // Reload full session detail
                    setTimeout(function() {
                        var route = getRoute();
                        if (route.startsWith('session/')) {
                            loadSessionDetail(route.replace('session/', ''));
                        }
                    }, 1500);
                });

                return;
            } catch (err) {
                // Fall back to fetch
                hide(loading);
            }
        }

        fetchTranscript(id, content, loading);
    }

    async function fetchTranscript(id, content, loading) {
        try {
            const resp = await api('GET', '/api/v1/sessions/' + id + '/transcript');
            hide(loading);

            if (!resp.ok) {
                content.innerHTML = '<div class="empty-state"><p>No transcript available.</p></div>';
                return;
            }

            const data = await resp.json();
            let events = Array.isArray(data) ? data : (data.events || data.transcript || []);

            if (events.length === 0) {
                content.innerHTML = '<div class="empty-state"><p>No transcript events yet.</p></div>';
                return;
            }

            content.innerHTML = '';

            const MAX_EVENTS = 500;
            if (events.length > MAX_EVENTS) {
                const notice = document.createElement('div');
                notice.className = 'empty-state';
                notice.innerHTML = '<p>Showing last ' + MAX_EVENTS + ' of ' + events.length + ' events.</p>';
                content.appendChild(notice);
                events = events.slice(-MAX_EVENTS);
            }

            events.forEach((ev) => appendTranscriptEvent(content, ev));
            // Auto-scroll to bottom for completed sessions
            content.scrollTop = content.scrollHeight;
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                content.innerHTML = '<div class="empty-state"><p>Failed to load transcript.</p></div>';
            }
        }
    }

    // Simple markdown-like rendering (no library needed)
    function renderMarkdown(text) {
        if (!text) return '';
        var html = escapeHtml(text);
        // Code blocks: ```lang\n...\n```
        html = html.replace(/```(\w*)\n([\s\S]*?)```/g, function(m, lang, code) {
            return '<pre class="tx-code-block"><code>' + code + '</code></pre>';
        });
        // Inline code: `...`
        html = html.replace(/`([^`\n]+)`/g, '<code class="tx-inline-code">$1</code>');
        // Bold: **...**
        html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
        // Italic: *...*
        html = html.replace(/\*([^*]+)\*/g, '<em>$1</em>');
        // Line breaks
        html = html.replace(/\n/g, '<br>');
        return html;
    }

    // Summarize tool input for the header line
    function toolInputSummary(name, input) {
        if (!input) return '';
        if (typeof input === 'string') {
            try { input = JSON.parse(input); } catch(e) { return input.substring(0, 120); }
        }
        switch (name) {
            case 'Bash':
                return input.command ? '$ ' + input.command : '';
            case 'Read':
                return input.file_path || '';
            case 'Edit':
                return input.file_path || '';
            case 'Write':
                return input.file_path || '';
            case 'Grep':
                return input.pattern ? '/' + input.pattern + '/' + (input.path ? ' in ' + input.path : '') : '';
            case 'Glob':
                return input.pattern || '';
            default:
                // For unknown tools, show a compact summary
                var keys = Object.keys(input);
                if (keys.length === 0) return '';
                if (keys.length === 1) {
                    var v = input[keys[0]];
                    return typeof v === 'string' ? v.substring(0, 120) : '';
                }
                return keys.join(', ');
        }
    }

    function appendTranscriptEvent(container, ev) {
        var type = ev.type || ev.role || 'system';
        var subtype = ev.subtype || '';

        // --- system/init (may have subtype='init' or just have model/tools fields) ---
        if (type === 'system' && (subtype === 'init' || ev.model || ev.tools)) {
            var model = '';
            var toolCount = 0;
            if (ev.model) model = ev.model;
            if (ev.tools) toolCount = ev.tools.length;
            if (!model && ev.message) model = ev.message;
            var div = document.createElement('div');
            div.className = 'tx-system';
            div.innerHTML = '<span class="tx-system-icon">&#9654;</span> Session started' +
                (model ? ' &middot; <span class="tx-system-model">' + escapeHtml(String(model)) + '</span>' : '') +
                (toolCount ? ' &middot; ' + toolCount + ' tools available' : '');
            container.appendChild(div);
            return;
        }

        // --- system/api_retry ---
        if (type === 'system' && subtype === 'api_retry') {
            var attempt = ev.attempt || '?';
            var max = ev.max_attempts || '?';
            var div = document.createElement('div');
            div.className = 'tx-system tx-system-warn';
            div.innerHTML = '<span class="tx-system-icon">&#x21bb;</span> Retrying API call (attempt ' + escapeHtml(String(attempt)) + '/' + escapeHtml(String(max)) + ')';
            container.appendChild(div);
            return;
        }

        // --- system (generic) ---
        if (type === 'system') {
            var div = document.createElement('div');
            div.className = 'tx-system';
            var msg = ev.content || ev.message || ev.text || '';
            if (typeof msg !== 'string') msg = JSON.stringify(msg);
            div.innerHTML = '<span class="tx-system-icon">&#9679;</span> ' + escapeHtml(msg);
            container.appendChild(div);
            return;
        }

        // --- result ---
        if (type === 'result') {
            var isError = ev.is_error || false;
            var cost = ev.total_cost_usd != null ? ev.total_cost_usd : (ev.usage && ev.usage.total_cost_usd != null ? ev.usage.total_cost_usd : null);
            var resultText = ev.result || ev.text || ev.content || '';
            if (typeof resultText !== 'string') resultText = JSON.stringify(resultText, null, 2);

            var div = document.createElement('div');
            div.className = 'tx-result ' + (isError ? 'tx-result-error' : 'tx-result-success');

            var headerParts = [];
            headerParts.push(isError ? '<span class="tx-result-icon">&#10007;</span> Task failed' : '<span class="tx-result-icon">&#10003;</span> Task completed');
            if (cost != null) headerParts.push(parseFloat(cost).toFixed(3) + ' USD');
            if (ev.num_turns) headerParts.push(ev.num_turns + (ev.num_turns === 1 ? ' turn' : ' turns'));
            if (ev.duration_seconds) headerParts.push(ev.duration_seconds.toFixed(1) + 's');
            if (ev.duration_ms) headerParts.push((ev.duration_ms / 1000).toFixed(1) + 's');

            div.innerHTML = '<div class="tx-result-header">' + headerParts.join(' &middot; ') + '</div>' +
                (resultText ? '<div class="tx-result-body">' + escapeHtml(resultText) + '</div>' : '');
            container.appendChild(div);
            return;
        }

        // --- tool_result ---
        if (type === 'tool_result') {
            var output = '';
            if (typeof ev.content === 'string') {
                output = ev.content;
            } else if (Array.isArray(ev.content)) {
                output = ev.content
                    .filter(function(b) { return b.type === 'text'; })
                    .map(function(b) { return b.text; })
                    .join('\n');
            } else if (ev.content) {
                output = JSON.stringify(ev.content, null, 2);
            } else if (ev.output) {
                output = typeof ev.output === 'string' ? ev.output : JSON.stringify(ev.output, null, 2);
            }
            if (!output) return; // skip empty tool results

            var div = document.createElement('div');
            div.className = 'tx-tool-output-block';
            var isError = ev.is_error || false;

            // Truncate very long output for display but keep full in expandable
            var truncLen = 500;
            var needsTrunc = output.length > truncLen;

            if (needsTrunc) {
                div.innerHTML = '<details class="tx-tool-output-details">' +
                    '<summary class="tx-tool-output-summary' + (isError ? ' tx-tool-output-error' : '') + '">Output (' + output.length.toLocaleString() + ' chars)</summary>' +
                    '<pre class="tx-tool-output-pre">' + escapeHtml(output) + '</pre>' +
                    '</details>';
            } else {
                div.innerHTML = '<pre class="tx-tool-output-pre' + (isError ? ' tx-tool-output-error' : '') + '">' + escapeHtml(output) + '</pre>';
            }
            container.appendChild(div);
            return;
        }

        // --- assistant ---
        if (type === 'assistant') {
            var contentBlocks = [];
            if (ev.message && ev.message.content && Array.isArray(ev.message.content)) {
                contentBlocks = ev.message.content;
            } else if (Array.isArray(ev.content)) {
                contentBlocks = ev.content;
            }

            // If no structured content, fall back
            if (contentBlocks.length === 0) {
                var text = '';
                if (typeof ev.content === 'string') text = ev.content;
                else if (ev.text) text = ev.text;
                else if (ev.message && typeof ev.message === 'string') text = ev.message;
                if (text) {
                    var div = document.createElement('div');
                    div.className = 'tx-msg';
                    div.innerHTML = '<div class="tx-msg-body">' + renderMarkdown(text) + '</div>';
                    container.appendChild(div);
                }
                return;
            }

            // Process each content block
            for (var i = 0; i < contentBlocks.length; i++) {
                var block = contentBlocks[i];

                // Thinking block
                if (block.type === 'thinking') {
                    var thinking = block.thinking || '';
                    if (!thinking) continue;
                    var div = document.createElement('div');
                    div.className = 'tx-thinking';
                    var preview = thinking.substring(0, 80).replace(/\n/g, ' ');
                    if (thinking.length > 80) preview += '...';
                    div.innerHTML = '<details class="tx-thinking-details">' +
                        '<summary class="tx-thinking-summary"><span class="tx-thinking-icon">&#x1F4AD;</span> Thinking... <span class="tx-thinking-preview">' + escapeHtml(preview) + '</span></summary>' +
                        '<div class="tx-thinking-body">' + escapeHtml(thinking) + '</div>' +
                        '</details>';
                    container.appendChild(div);
                    continue;
                }

                // Text block
                if (block.type === 'text') {
                    var text = block.text || '';
                    if (!text) continue;
                    var div = document.createElement('div');
                    div.className = 'tx-msg';
                    div.innerHTML = '<div class="tx-msg-label">Claude</div>' +
                        '<div class="tx-msg-body">' + renderMarkdown(text) + '</div>';
                    container.appendChild(div);
                    continue;
                }

                // Tool use block
                if (block.type === 'tool_use') {
                    var toolName = block.name || 'unknown';
                    var input = block.input || {};
                    var summary = toolInputSummary(toolName, input);

                    var div = document.createElement('div');
                    div.className = 'tx-tool';

                    var toolIcon = '&#x1F527;';

                    var headerHtml = '<div class="tx-tool-header">' +
                        '<span class="tx-tool-icon">' + toolIcon + '</span>' +
                        '<span class="tx-tool-name">' + escapeHtml(toolName) + '</span>' +
                        '</div>';

                    var bodyHtml = '';
                    if (summary) {
                        bodyHtml = '<div class="tx-tool-summary"><pre class="tx-tool-cmd">' + escapeHtml(summary) + '</pre></div>';
                    }

                    // Show full input as collapsible for complex tools
                    if (!summary || toolName === 'Edit') {
                        var inputStr = typeof input === 'string' ? input : JSON.stringify(input, null, 2);
                        bodyHtml += '<details class="tx-tool-input-details">' +
                            '<summary class="tx-tool-input-toggle">Show input</summary>' +
                            '<pre class="tx-tool-input-pre">' + escapeHtml(inputStr) + '</pre>' +
                            '</details>';
                    }

                    div.innerHTML = headerHtml + bodyHtml;
                    container.appendChild(div);
                    continue;
                }

                // Unknown block type — render as JSON
                var div = document.createElement('div');
                div.className = 'tx-system';
                div.innerHTML = '<pre class="tx-tool-input-pre">' + escapeHtml(JSON.stringify(block, null, 2)) + '</pre>';
                container.appendChild(div);
            }
            return;
        }

        // --- Legacy tool event (old format with ev.tool) ---
        if (ev.tool) {
            var toolName = ev.tool.name || ev.tool || 'unknown';
            if (typeof toolName !== 'string') toolName = JSON.stringify(toolName);
            var input = ev.tool.input;
            var output = ev.tool.output !== undefined ? (typeof ev.tool.output === 'string' ? ev.tool.output : JSON.stringify(ev.tool.output, null, 2)) : (ev.content || ev.text || '');
            var summary = toolInputSummary(toolName, input);

            var div = document.createElement('div');
            div.className = 'tx-tool';
            var headerHtml = '<div class="tx-tool-header">' +
                '<span class="tx-tool-icon">&#x1F527;</span>' +
                '<span class="tx-tool-name">' + escapeHtml(toolName) + '</span>' +
                '</div>';
            var bodyHtml = '';
            if (summary) {
                bodyHtml = '<div class="tx-tool-summary"><pre class="tx-tool-cmd">' + escapeHtml(summary) + '</pre></div>';
            }
            if (output) {
                bodyHtml += '<details class="tx-tool-output-details">' +
                    '<summary class="tx-tool-output-summary">Output</summary>' +
                    '<pre class="tx-tool-output-pre">' + escapeHtml(typeof output === 'string' ? output : JSON.stringify(output, null, 2)) + '</pre>' +
                    '</details>';
            }
            div.innerHTML = headerHtml + bodyHtml;
            container.appendChild(div);
            return;
        }

        // --- user / human ---
        if (type === 'user' || type === 'human') {
            var text = '';
            if (typeof ev.content === 'string') text = ev.content;
            else if (Array.isArray(ev.content)) {
                text = ev.content.filter(function(b) { return b.type === 'text'; }).map(function(b) { return b.text; }).join('\n');
            } else if (ev.message && typeof ev.message === 'string') text = ev.message;
            else if (ev.text) text = ev.text;
            if (!text) text = JSON.stringify(ev, null, 2);

            var div = document.createElement('div');
            div.className = 'tx-msg tx-msg-user';
            div.innerHTML = '<div class="tx-msg-label">User</div>' +
                '<div class="tx-msg-body">' + renderMarkdown(text) + '</div>';
            container.appendChild(div);
            return;
        }

        // --- Fallback for unknown types ---
        var div = document.createElement('div');
        div.className = 'tx-system';
        var body = ev.content || ev.text || ev.message || '';
        if (typeof body !== 'string') body = JSON.stringify(body || ev, null, 2);
        div.innerHTML = '<span class="tx-system-icon">&#9679;</span> ' + escapeHtml(type) + ': ' + escapeHtml(body);
        container.appendChild(div);
    }

    function getTypeClass(type) {
        // Kept for any external callers, but no longer used by appendTranscriptEvent
        var t = type.toLowerCase();
        if (t === 'user' || t === 'human') return 'type-user';
        if (t === 'assistant' || t === 'ai') return 'type-assistant';
        if (t === 'tool' || t === 'tool_use' || t === 'tool_result') return 'type-tool';
        if (t === 'error') return 'type-error';
        return 'type-system';
    }

    function showLiveIndicator() {
        var indicator = document.getElementById('live-indicator');
        if (!indicator) {
            indicator = document.createElement('span');
            indicator.id = 'live-indicator';
            indicator.className = 'live-badge';
            indicator.textContent = '\u25CF LIVE';
            var header = document.querySelector('#page-session-detail .page-header h2');
            if (header) header.appendChild(indicator);
        }
        indicator.hidden = false;
    }

    function hideLiveIndicator() {
        var indicator = document.getElementById('live-indicator');
        if (indicator) indicator.hidden = true;
    }

    function stopSSE() {
        if (sseSource) {
            sseSource.close();
            sseSource = null;
        }
        hideLiveIndicator();
    }

    // ---------------------
    // Proxy Log
    // ---------------------
    async function loadProxyLog(id, silent) {
        const tbody = $('#proxy-log-tbody');
        const loading = $('#proxy-log-loading');
        if (!silent) {
            tbody.innerHTML = '';
            show(loading);
        }

        try {
            const resp = await api('GET', '/api/v1/sessions/' + id + '/proxy-log');
            hide(loading);

            if (!resp.ok) {
                tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--text-muted)">No proxy log available.</td></tr>';
                return;
            }

            const data = await resp.json();
            const entries = Array.isArray(data) ? data : (data.proxy_log || data.entries || data.logs || []);

            if (entries.length === 0) {
                tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--text-muted)">No proxy log entries.</td></tr>';
                return;
            }

            tbody.innerHTML = entries.map((e) => {
                const decision = (e.decision || e.action || '').toLowerCase();
                const decisionClass = decision === 'allow' ? 'decision-allow' : (decision === 'deny' ? 'decision-deny' : '');
                const statusCode = e.status_code !== undefined && e.status_code !== null ? String(e.status_code) : '-';
                return '<tr>' +
                    '<td class="mono">' + escapeHtml(formatTime(e.timestamp)) + '</td>' +
                    '<td>' + escapeHtml(e.method || '-') + '</td>' +
                    '<td class="mono truncate">' + escapeHtml(e.url || e.path || '-') + '</td>' +
                    '<td>' + escapeHtml(e.operation || '-') + '</td>' +
                    '<td>' + escapeHtml((e.service || '-').toUpperCase()) + '</td>' +
                    '<td class="mono">' + escapeHtml(statusCode) + '</td>' +
                    '<td class="' + decisionClass + '">' + escapeHtml(e.decision || e.action || '-') + '</td>' +
                    '</tr>';
            }).join('');
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--status-error)">Failed to load proxy log.</td></tr>';
            }
        }
    }

    // ---------------------
    // Users
    // ---------------------
    async function loadUsers() {
        const tbody = $('#users-tbody');
        const loading = $('#users-loading');
        const empty = $('#users-empty');
        const currentUser = localStorage.getItem('alcove_user');

        tbody.innerHTML = '';
        show(loading);
        hide(empty);

        try {
            const resp = await api('GET', '/api/v1/users');
            const data = await resp.json();
            hide(loading);

            const users = Array.isArray(data) ? data : (data.users || data.items || []);
            if (users.length === 0) {
                show(empty);
                return;
            }

            tbody.innerHTML = users.map((u) => {
                const username = u.username || u.name || '-';
                const created = formatTime(u.created_at || u.created);
                const isUserAdmin = u.is_admin || false;
                const isSelf = username === currentUser;
                const roleBadge = isUserAdmin
                    ? '<span class="badge badge-running">admin</span>'
                    : '<span class="badge">user</span>';
                const selfLabel = isSelf ? ' <em>(you)</em>' : '';

                let actions = '';
                if (!isSelf) {
                    if (isUserAdmin) {
                        actions += '<button class="btn btn-small btn-outline toggle-admin-btn" data-username="' + escapeHtml(username) + '" data-admin="true">Revoke Admin</button> ';
                    } else {
                        actions += '<button class="btn btn-small btn-outline toggle-admin-btn" data-username="' + escapeHtml(username) + '" data-admin="false">Make Admin</button> ';
                    }
                    actions += '<button class="btn btn-small btn-outline delete-user-btn" data-username="' + escapeHtml(username) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button>';
                    actions += ' <button class="btn btn-small btn-outline reset-pw-btn" data-username="' + escapeHtml(username) + '">Reset Password</button>';
                }

                return '<tr>' +
                    '<td>' + escapeHtml(username) + selfLabel + '</td>' +
                    '<td>' + escapeHtml(created) + '</td>' +
                    '<td>' + roleBadge + '</td>' +
                    '<td>' + actions + '</td>' +
                    '</tr>';
            }).join('');

            // Toggle admin click handlers
            tbody.querySelectorAll('.toggle-admin-btn').forEach((btn) => {
                btn.addEventListener('click', async () => {
                    const username = btn.dataset.username;
                    const currentlyAdmin = btn.dataset.admin === 'true';
                    const action = currentlyAdmin ? 'revoke admin from' : 'make admin';
                    if (!confirm('Are you sure you want to ' + action + ' "' + username + '"?')) return;
                    btn.disabled = true;
                    try {
                        const resp = await api('PUT', '/api/v1/users/' + encodeURIComponent(username) + '/admin', { is_admin: !currentlyAdmin });
                        if (!resp.ok) {
                            const data = await resp.json().catch(() => ({}));
                            alert(data.error || data.message || 'Failed to update user.');
                        }
                        loadUsers();
                    } catch (err) {
                        if (err.message !== 'unauthorized') {
                            alert('Failed to update user.');
                        }
                        btn.disabled = false;
                    }
                });
            });

            // Delete click handlers
            tbody.querySelectorAll('.delete-user-btn').forEach((btn) => {
                btn.addEventListener('click', async () => {
                    const username = btn.dataset.username;
                    if (!confirm('Are you sure you want to delete user "' + username + '"? This cannot be undone.')) return;
                    btn.disabled = true;
                    try {
                        const resp = await api('DELETE', '/api/v1/users/' + encodeURIComponent(username));
                        if (!resp.ok) {
                            const data = await resp.json().catch(() => ({}));
                            alert(data.error || data.message || 'Failed to delete user.');
                            btn.disabled = false;
                        } else {
                            loadUsers();
                        }
                    } catch (err) {
                        if (err.message !== 'unauthorized') {
                            alert('Failed to delete user.');
                        }
                        btn.disabled = false;
                    }
                });
            });

            // Reset password click handlers
            tbody.querySelectorAll('.reset-pw-btn').forEach((btn) => {
                btn.addEventListener('click', async () => {
                    const username = btn.dataset.username;
                    const newPw = prompt('Enter new password for "' + username + '" (min 8 characters):');
                    if (!newPw) return;
                    if (newPw.length < 8) { alert('Password must be at least 8 characters.'); return; }
                    try {
                        const resp = await api('PUT', '/api/v1/users/' + encodeURIComponent(username) + '/password', {password: newPw});
                        if (!resp.ok) {
                            const data = await resp.json().catch(() => ({}));
                            alert(data.error || 'Failed to reset password.');
                        } else {
                            alert('Password reset for "' + username + '".');
                        }
                    } catch (err) {
                        if (err.message !== 'unauthorized') alert('Failed to reset password.');
                    }
                });
            });
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                tbody.innerHTML = '<tr><td colspan="4" style="text-align:center;color:var(--status-error);">Failed to load users.</td></tr>';
            }
        }
    }

    // Show create user form
    $('#show-create-user').addEventListener('click', () => {
        show($('#create-user-form-container'));
        $('#new-user-username').focus();
    });

    // Cancel create user
    $('#cancel-create-user').addEventListener('click', () => {
        hide($('#create-user-form-container'));
        $('#create-user-form').reset();
        hide($('#create-user-error'));
    });

    // Submit create user
    $('#create-user-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const errEl = $('#create-user-error');
        hide(errEl);

        const username = $('#new-user-username').value.trim();
        const password = $('#new-user-password').value;
        const isAdminChecked = $('#new-user-admin').checked;

        if (!username || !password) {
            errEl.textContent = 'Username and password are required.';
            show(errEl);
            return;
        }

        const btn = e.target.querySelector('button[type="submit"]');
        btn.disabled = true;
        btn.textContent = 'Creating...';

        try {
            const resp = await api('POST', '/api/v1/users', {
                username: username,
                password: password,
                is_admin: isAdminChecked
            });

            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                throw new Error(data.error || data.message || 'Failed to create user.');
            }

            hide($('#create-user-form-container'));
            $('#create-user-form').reset();
            loadUsers();
        } catch (err) {
            if (err.message !== 'unauthorized') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Create User';
        }
    });

    // ---------------------
    // Schedules
    // ---------------------
    function describeCron(expr) {
        const parts = expr.trim().split(/\s+/);
        if (parts.length !== 5) return expr;
        const [min, hr, dom, mon, dow] = parts;
        if (min === '0' && hr === '*' && dom === '*' && mon === '*' && dow === '*') return 'Every hour at :00';
        if (min.startsWith('*/') && hr === '*') return 'Every ' + min.slice(2) + ' minutes';
        if (hr.startsWith('*/') && min === '0') return 'Every ' + hr.slice(2) + ' hours';
        if (dom === '*' && mon === '*' && dow === '*') return 'Daily at ' + hr + ':' + min.padStart(2, '0');
        if (dom === '*' && mon === '*' && dow === '1-5') return 'Weekdays at ' + hr + ':' + min.padStart(2, '0');
        if (dom === '*' && mon === '*' && dow === '0') return 'Sundays at ' + hr + ':' + min.padStart(2, '0');
        if (mon === '*' && dow === '*') return 'Monthly on day ' + dom + ' at ' + hr + ':' + min.padStart(2, '0');
        return expr;
    }

    async function loadSchedules() {
        const tbody = $('#schedules-tbody');
        const loading = $('#schedules-loading');
        const empty = $('#schedules-empty');

        tbody.innerHTML = '';
        show(loading);
        hide(empty);

        try {
            const resp = await api('GET', '/api/v1/schedules');
            const data = await resp.json();
            hide(loading);

            const schedules = Array.isArray(data) ? data : (data.schedules || data.items || []);
            if (schedules.length === 0) {
                show(empty);
                return;
            }

            tbody.innerHTML = schedules.map(function (s) {
                const name = s.name || '-';
                const cron = s.cron || s.cron_expression || '-';
                const cronDesc = cron !== '-' ? describeCron(cron) : '-';
                const nextRun = formatTime(s.next_run || s.next_run_at);
                const lastRun = formatTime(s.last_run || s.last_run_at);
                const enabled = s.enabled !== false;
                const enabledBadge = enabled
                    ? '<span class="badge badge-completed">enabled</span>'
                    : '<span class="badge badge-cancelled">disabled</span>';
                const id = s.id || '';

                return '<tr>' +
                    '<td>' + escapeHtml(name) + '</td>' +
                    '<td><span class="mono">' + escapeHtml(cron) + '</span><br><small style="color:var(--text-muted)">' + escapeHtml(cronDesc) + '</small></td>' +
                    '<td>' + escapeHtml(nextRun) + '</td>' +
                    '<td>' + escapeHtml(lastRun) + '</td>' +
                    '<td>' + enabledBadge + '</td>' +
                    '<td>' +
                        '<button class="btn btn-small btn-outline edit-schedule-btn" data-id="' + escapeHtml(id) + '">Edit</button> ' +
                        '<button class="btn btn-small btn-outline delete-schedule-btn" data-id="' + escapeHtml(id) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button>' +
                    '</td>' +
                    '</tr>';
            }).join('');

            // Edit handlers
            tbody.querySelectorAll('.edit-schedule-btn').forEach(function (btn) {
                btn.addEventListener('click', async function () {
                    const id = btn.dataset.id;
                    try {
                        const resp = await api('GET', '/api/v1/schedules/' + id);
                        const s = await resp.json();
                        editingScheduleId = id;
                        $('#sched-name').value = s.name || '';
                        $('#sched-cron').value = s.cron || s.cron_expression || '';
                        $('#sched-prompt').value = s.prompt || '';
                        $('#sched-provider').value = s.provider || '';
                        $('#sched-repo').value = s.repo || '';
                        const timeout = s.timeout ? Math.round(s.timeout / 60) : 60;
                        $('#sched-timeout').value = timeout;
                        $('#sched-timeout-value').textContent = timeout;
                        $('#sched-debug').checked = s.debug || false;
                        $('#sched-enabled').checked = s.enabled !== false;
                        $('#schedule-submit-btn').textContent = 'Update Schedule';
                        show($('#schedule-form-container'));
                        $('#sched-name').focus();
                    } catch (err) {
                        if (err.message !== 'unauthorized') {
                            alert('Failed to load schedule.');
                        }
                    }
                });
            });

            // Delete handlers
            tbody.querySelectorAll('.delete-schedule-btn').forEach(function (btn) {
                btn.addEventListener('click', async function () {
                    const id = btn.dataset.id;
                    if (!confirm('Are you sure you want to delete this schedule?')) return;
                    btn.disabled = true;
                    try {
                        const resp = await api('DELETE', '/api/v1/schedules/' + id);
                        if (!resp.ok) {
                            const data = await resp.json().catch(function () { return {}; });
                            alert(data.error || data.message || 'Failed to delete schedule.');
                            btn.disabled = false;
                        } else {
                            loadSchedules();
                        }
                    } catch (err) {
                        if (err.message !== 'unauthorized') {
                            alert('Failed to delete schedule.');
                        }
                        btn.disabled = false;
                    }
                });
            });
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--status-error);">Failed to load schedules.</td></tr>';
            }
        }
    }

    async function loadScheduleProviders() {
        const select = $('#sched-provider');
        try {
            const resp = await api('GET', '/api/v1/credentials');
            const data = await resp.json();
            const creds = data.credentials || [];
            select.innerHTML = '<option value="">Select a provider</option>';
            creds.forEach(function (c) {
                const label = c.name + ' (' + (c.provider === 'google-vertex' ? 'Vertex AI' : 'Anthropic') + ')';
                select.innerHTML += '<option value="' + escapeHtml(c.name) + '">' + escapeHtml(label) + '</option>';
            });
            if (creds.length === 1) select.selectedIndex = 1;
        } catch (err) {
            if (err.message !== 'unauthorized') {
                select.innerHTML = '<option value="">Failed to load providers</option>';
            }
        }
    }

    // ---------------------
    // Natural language cron parser
    // ---------------------
    function parseNaturalCron(text) {
        if (!text) return null;
        const t = text.trim().toLowerCase();

        // Day name mapping
        const dayMap = {sunday:0, sun:0, monday:1, mon:1, tuesday:2, tue:2, wednesday:3, wed:3, thursday:4, thu:4, friday:5, fri:5, saturday:6, sat:6};

        // Parse time from text (returns {hour, minute, label} or null)
        function parseTime(s) {
            let m;
            // "at midnight"
            if (/midnight/.test(s)) return {hour: 0, minute: 0, label: 'midnight'};
            // "at noon"
            if (/noon/.test(s)) return {hour: 12, minute: 0, label: 'noon'};
            // "at 2:30pm", "at 14:30"
            m = s.match(/(\d{1,2}):(\d{2})\s*(am|pm)?/i);
            if (m) {
                let h = parseInt(m[1]), min = parseInt(m[2]);
                if (m[3] && m[3].toLowerCase() === 'pm' && h < 12) h += 12;
                if (m[3] && m[3].toLowerCase() === 'am' && h === 12) h = 0;
                return {hour: h, minute: min, label: h + ':' + String(min).padStart(2,'0')};
            }
            // "at 2pm", "at 2 pm"
            m = s.match(/(\d{1,2})\s*(am|pm)/i);
            if (m) {
                let h = parseInt(m[1]);
                if (m[2].toLowerCase() === 'pm' && h < 12) h += 12;
                if (m[2].toLowerCase() === 'am' && h === 12) h = 0;
                return {hour: h, minute: 0, label: h > 12 ? (h-12) + ' PM' : h + ' AM'};
            }
            // "at 14" (24h)
            m = s.match(/at\s+(\d{1,2})(?:\s|$)/);
            if (m) {
                let h = parseInt(m[1]);
                if (h >= 0 && h <= 23) return {hour: h, minute: 0, label: String(h).padStart(2,'0') + ':00'};
            }
            return null;
        }

        let m, time;

        // "every N minutes"
        m = t.match(/every\s+(\d+)\s+minutes?/);
        if (m) { const n = m[1]; return {cron: '*/' + n + ' * * * *', name: 'Every ' + n + ' minutes'}; }

        // "every N hours"
        m = t.match(/every\s+(\d+)\s+hours?/);
        if (m) { const n = m[1]; return {cron: '0 */' + n + ' * * *', name: 'Every ' + n + ' hours'}; }

        // "every hour"
        if (/every\s+hour/.test(t)) return {cron: '0 * * * *', name: 'Every hour'};

        // "twice a day" / "twice daily"
        if (/twice\s+(a\s+)?day|twice\s+daily/.test(t)) return {cron: '0 0,12 * * *', name: 'Twice daily'};

        // "hourly on weekdays"
        if (/hourly.*weekday|weekday.*hourly/.test(t)) return {cron: '0 * * * 1-5', name: 'Hourly on weekdays'};

        // "every weekday at TIME" / "weekdays at TIME"
        m = t.match(/(every\s+)?weekdays?\s+at\s+/);
        if (m) {
            time = parseTime(t);
            if (time) return {cron: time.minute + ' ' + time.hour + ' * * 1-5', name: 'Weekdays at ' + time.label};
        }

        // "every {day} and {day} at TIME"
        m = t.match(/every\s+(\w+)\s+and\s+(\w+)/);
        if (m && dayMap[m[1]] !== undefined && dayMap[m[2]] !== undefined) {
            time = parseTime(t);
            const d1 = dayMap[m[1]], d2 = dayMap[m[2]];
            const h = time ? time.hour : 0, min = time ? time.minute : 0;
            const label = time ? ' at ' + time.label : '';
            return {cron: min + ' ' + h + ' * * ' + d1 + ',' + d2, name: m[1].charAt(0).toUpperCase() + m[1].slice(1,3) + '/' + m[2].charAt(0).toUpperCase() + m[2].slice(1,3) + label};
        }

        // "every {day} at TIME"
        for (const [dayName, dayNum] of Object.entries(dayMap)) {
            const re = new RegExp('every\\s+' + dayName + '\\w*');
            if (re.test(t)) {
                time = parseTime(t);
                const h = time ? time.hour : 0, min = time ? time.minute : 0;
                const label = time ? ' at ' + time.label : '';
                const capDay = dayName.charAt(0).toUpperCase() + dayName.slice(1);
                return {cron: min + ' ' + h + ' * * ' + dayNum, name: capDay + label};
            }
        }

        // "monthly on the Nth at TIME" / "every month on the Nth"
        m = t.match(/monthly|every\s+month/);
        if (m) {
            const dm = t.match(/(\d{1,2})(st|nd|rd|th)?/);
            const day = dm ? parseInt(dm[1]) : 1;
            time = parseTime(t);
            const h = time ? time.hour : 0, min = time ? time.minute : 0;
            const label = time ? ' at ' + time.label : '';
            return {cron: min + ' ' + h + ' ' + day + ' * *', name: 'Monthly on day ' + day + label};
        }

        // "daily at TIME" / "every day at TIME"
        m = t.match(/daily|every\s+day/);
        if (m) {
            time = parseTime(t);
            if (time) return {cron: time.minute + ' ' + time.hour + ' * * *', name: 'Daily at ' + time.label};
            return {cron: '0 0 * * *', name: 'Daily at midnight'};
        }

        return null; // Unrecognized pattern
    }

    // Natural language input event listener (debounced)
    let naturalDebounce = null;
    $('#sched-natural').addEventListener('input', function (e) {
        clearTimeout(naturalDebounce);
        naturalDebounce = setTimeout(function () {
            const result = parseNaturalCron(e.target.value);
            if (result) {
                $('#sched-cron').value = result.cron;
                // Auto-name only if name field is empty or was auto-generated
                const nameEl = $('#sched-name');
                if (!nameEl.value || nameEl.dataset.autoNamed === 'true') {
                    nameEl.value = result.name;
                    nameEl.dataset.autoNamed = 'true';
                }
                // Show success indicator
                e.target.style.borderColor = 'var(--status-completed)';
                setTimeout(function () { e.target.style.borderColor = ''; }, 2000);
            } else if (e.target.value.trim()) {
                // No match but user typed something
                e.target.style.borderColor = '';
            }
        }, 300);
    });

    // Clear auto-named flag when user manually edits name
    $('#sched-name').addEventListener('input', function () {
        $('#sched-name').dataset.autoNamed = 'false';
    });

    function openScheduleForm(prefill) {
        editingScheduleId = null;
        $('#schedule-form').reset();
        $('#sched-natural').value = '';
        $('#schedule-submit-btn').textContent = 'Create Schedule';
        hide($('#schedule-form-error'));
        if (prefill) {
            $('#sched-prompt').value = prefill.prompt || '';
            $('#sched-provider').value = prefill.provider || '';
            $('#sched-repo').value = prefill.repo || '';
            const timeout = prefill.timeout || 60;
            $('#sched-timeout').value = timeout;
            $('#sched-timeout-value').textContent = timeout;
            $('#sched-debug').checked = prefill.debug || false;
            $('#sched-enabled').checked = true;
        }
        show($('#schedule-form-container'));
        $('#sched-name').focus();
    }

    // Show create schedule form
    $('#show-create-schedule').addEventListener('click', function () {
        openScheduleForm();
    });

    // Cancel create schedule
    $('#cancel-create-schedule').addEventListener('click', function () {
        hide($('#schedule-form-container'));
        $('#schedule-form').reset();
        hide($('#schedule-form-error'));
        editingScheduleId = null;
        $('#schedule-submit-btn').textContent = 'Create Schedule';
    });

    // Schedule timeout slider
    $('#sched-timeout').addEventListener('input', function (e) {
        $('#sched-timeout-value').textContent = e.target.value;
    });

    // Submit schedule form
    $('#schedule-form').addEventListener('submit', async function (e) {
        e.preventDefault();
        const errEl = $('#schedule-form-error');
        hide(errEl);

        const name = $('#sched-name').value.trim();
        const cron = $('#sched-cron').value.trim();
        const prompt = $('#sched-prompt').value.trim();

        if (!name || !cron || !prompt) {
            errEl.textContent = 'Name, cron expression, and prompt are required.';
            show(errEl);
            return;
        }

        const payload = {
            name: name,
            cron: cron,
            prompt: prompt,
            provider: $('#sched-provider').value || undefined,
            repo: $('#sched-repo').value.trim() || undefined,
            timeout: parseInt($('#sched-timeout').value, 10) * 60,
            debug: $('#sched-debug').checked,
            enabled: $('#sched-enabled').checked
        };

        Object.keys(payload).forEach(function (k) {
            if (payload[k] === undefined) delete payload[k];
        });

        const btn = $('#schedule-submit-btn');
        btn.disabled = true;
        btn.textContent = editingScheduleId ? 'Updating...' : 'Creating...';

        try {
            var resp;
            if (editingScheduleId) {
                resp = await api('PUT', '/api/v1/schedules/' + editingScheduleId, payload);
            } else {
                resp = await api('POST', '/api/v1/schedules', payload);
            }

            if (!resp.ok) {
                const data = await resp.json().catch(function () { return {}; });
                throw new Error(data.error || data.message || 'Failed to save schedule.');
            }

            hide($('#schedule-form-container'));
            $('#schedule-form').reset();
            editingScheduleId = null;
            loadSchedules();
        } catch (err) {
            if (err.message !== 'unauthorized') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Create Schedule';
        }
    });

    // ---------------------
    // Profiles page
    // ---------------------
    async function loadProfilesPage() {
        var builtinContainer = $('#profiles-builtin');
        var userContainer = $('#profiles-user');
        var loading = $('#profiles-loading');
        var empty = $('#profiles-empty');

        builtinContainer.innerHTML = '';
        userContainer.innerHTML = '';
        show(loading);
        hide(empty);

        try {
            var resp = await api('GET', '/api/v1/profiles');
            var data = await resp.json();
            hide(loading);

            var profiles = Array.isArray(data) ? data : (data.profiles || data.items || []);
            allProfiles = profiles;

            var builtinProfiles = profiles.filter(function (p) { return p.builtin === true || p.type === 'builtin'; });
            var userProfiles = profiles.filter(function (p) { return p.builtin !== true && p.type !== 'builtin'; });

            if (builtinProfiles.length === 0) {
                builtinContainer.innerHTML = '<p style="color:var(--text-muted);font-size:13px;">No starter profiles available.</p>';
            } else {
                builtinContainer.innerHTML = builtinProfiles.map(function (p) { return renderProfileCard(p, true); }).join('');
            }

            if (userProfiles.length === 0) {
                show(empty);
            } else {
                hide(empty);
                userContainer.innerHTML = userProfiles.map(function (p) { return renderProfileCard(p, false); }).join('');
            }

            attachProfileCardHandlers();
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                builtinContainer.innerHTML = '<p style="color:var(--status-error);font-size:13px;">Failed to load profiles. Check your connection and try again.</p>';
            }
        }
    }

    function renderProfileCard(p, isBuiltin) {
        var name = p.name || '-';
        var description = p.description || '';
        var permsSummary = '';
        if (p.tools && typeof p.tools === 'object') {
            var toolEntries = Object.keys(p.tools);
            permsSummary = toolEntries.map(function (t) {
                var rules = normalizeToolConfig(p.tools[t]);
                if (rules.length === 0) {
                    return '<div class="perm-tool-block"><span class="perm-tool-name">' + escapeHtml(t) + '</span></div>';
                }
                var rulesHtml = rules.map(function (rule, idx) {
                    var opsList = Array.isArray(rule.operations) ? rule.operations.map(function (o) { return typeof o === 'string' ? o : o.name; }) : [];
                    var reposList = Array.isArray(rule.repos) ? rule.repos : [];
                    if (opsList.length === 0) return '';
                    var reposHtml = '';
                    if (reposList.length === 0 || (reposList.length === 1 && reposList[0] === '*')) {
                        reposHtml = '<span class="perm-repos perm-repos-all">All repositories</span>';
                    } else {
                        reposHtml = '<span class="perm-repos">' + reposList.map(function (r) { return escapeHtml(r); }).join(', ') + '</span>';
                    }
                    if (rules.length === 1) {
                        // Single rule: display same as before (no rule block wrapper)
                        return reposHtml +
                            '<div class="perm-ops">' + opsList.map(function (o) { return escapeHtml(o); }).join(', ') + '</div>';
                    }
                    // Multi-rule: wrap each in a rule block
                    return '<div class="perm-rule-block">' +
                        '<div class="perm-rule-label">Rule ' + (idx + 1) + '</div>' +
                        reposHtml +
                        '<div class="perm-ops">' + opsList.map(function (o) { return escapeHtml(o); }).join(', ') + '</div>' +
                        '</div>';
                }).join('');
                return '<div class="perm-tool-block">' +
                    '<span class="perm-tool-name">' + escapeHtml(t) + '</span> ' +
                    rulesHtml +
                    '</div>';
            }).join('');
        }

        var typeBadge = isBuiltin
            ? '<span class="badge badge-builtin">builtin</span>'
            : '<span class="badge badge-custom">custom</span>';

        var actions = '<button class="btn btn-small btn-primary profile-use-btn" data-name="' + escapeHtml(name) + '">Use in New Task</button> ';
        actions += '<button class="btn btn-small btn-outline profile-duplicate-btn" data-name="' + escapeHtml(name) + '">Duplicate</button>';
        if (!isBuiltin) {
            actions += ' <button class="btn btn-small btn-outline profile-edit-btn" data-name="' + escapeHtml(name) + '">Edit</button>';
            actions += ' <button class="btn btn-small btn-outline profile-delete-btn" data-name="' + escapeHtml(name) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button>';
        }

        return '<div class="profile-card" data-profile-name="' + escapeHtml(name) + '">' +
            '<div class="profile-card-header">' +
            '<span class="profile-card-name">' + escapeHtml(name) + ' ' + typeBadge + '</span>' +
            '<div class="profile-card-actions">' + actions + '</div>' +
            '</div>' +
            (description ? '<div class="profile-card-desc">' + escapeHtml(description) + '</div>' : '') +
            (permsSummary ? '<div class="profile-card-perms">' + permsSummary + '</div>' : '') +
            '</div>';
    }

    function attachProfileCardHandlers() {
        // Use in New Task
        $$('.profile-use-btn').forEach(function (btn) {
            btn.addEventListener('click', function () {
                var name = btn.dataset.name;
                if (!selectedProfiles.includes(name)) {
                    selectedProfiles.push(name);
                }
                navigate('task/new');
            });
        });

        // Duplicate
        $$('.profile-duplicate-btn').forEach(function (btn) {
            btn.addEventListener('click', async function () {
                var name = btn.dataset.name;
                try {
                    var resp = await api('GET', '/api/v1/profiles/' + encodeURIComponent(name));
                    var p = await resp.json();
                    openProfileForm();
                    setProfileMode('manual');
                    $('#profile-name').value = p.name + '-copy';
                    $('#profile-description').value = p.description || '';
                    editingProfileId = null;
                    if (p.tools) {
                        populateProfileToolSelection(p.tools);
                    }
                } catch (err) {
                    if (err.message !== 'unauthorized') alert('Failed to load profile.');
                }
            });
        });

        // Edit
        $$('.profile-edit-btn').forEach(function (btn) {
            btn.addEventListener('click', async function () {
                var name = btn.dataset.name;
                try {
                    var resp = await api('GET', '/api/v1/profiles/' + encodeURIComponent(name));
                    var p = await resp.json();
                    openProfileForm();
                    setProfileMode('manual');
                    $('#profile-name').value = p.name || '';
                    $('#profile-description').value = p.description || '';
                    editingProfileId = name;
                    $('#profile-submit-btn').textContent = 'Update Profile';
                    if (p.tools) {
                        populateProfileToolSelection(p.tools);
                    }
                } catch (err) {
                    if (err.message !== 'unauthorized') alert('Failed to load profile.');
                }
            });
        });

        // Delete
        $$('.profile-delete-btn').forEach(function (btn) {
            btn.addEventListener('click', async function () {
                var name = btn.dataset.name;
                if (!confirm('Delete profile "' + name + '"? This cannot be undone.')) return;
                btn.disabled = true;
                try {
                    var resp = await api('DELETE', '/api/v1/profiles/' + encodeURIComponent(name));
                    if (!resp.ok) {
                        var data = await resp.json().catch(function () { return {}; });
                        alert(data.error || data.message || 'Failed to delete profile.');
                        btn.disabled = false;
                    } else {
                        loadProfilesPage();
                    }
                } catch (err) {
                    if (err.message !== 'unauthorized') alert('Failed to delete profile.');
                    btn.disabled = false;
                }
            });
        });
    }

    function openProfileForm() {
        editingProfileId = null;
        $('#profile-form').reset();
        hide($('#profile-form-error'));
        hide($('#profile-ai-error'));
        hide($('#profile-ai-result'));
        $('#profile-submit-btn').textContent = 'Save Profile';
        show($('#profile-form-container'));
        loadProfileToolChoices();
    }

    function setProfileMode(mode) {
        $$('.profile-mode-btn').forEach(function (b) {
            b.classList.toggle('active', b.dataset.profileMode === mode);
            if (b.dataset.profileMode !== mode) {
                b.classList.add('btn-outline');
            } else {
                b.classList.remove('btn-outline');
            }
        });
        if (mode === 'ai') {
            show($('#profile-ai-mode'));
            hide($('#profile-manual-mode'));
        } else {
            hide($('#profile-ai-mode'));
            show($('#profile-manual-mode'));
        }
    }

    // Profile mode toggle buttons
    document.addEventListener('click', function (e) {
        if (e.target.classList.contains('profile-mode-btn')) {
            e.preventDefault();
            setProfileMode(e.target.dataset.profileMode);
        }
    });

    // Show create profile form
    $('#show-create-profile').addEventListener('click', function () {
        openProfileForm();
        setProfileMode('ai');
    });

    // Cancel create profile
    $('#cancel-create-profile').addEventListener('click', function () {
        hide($('#profile-form-container'));
        $('#profile-form').reset();
        editingProfileId = null;
    });

    // AI Generate button
    $('#profile-ai-generate').addEventListener('click', async function () {
        var desc = $('#profile-ai-description').value.trim();
        if (!desc) return;

        var btn = $('#profile-ai-generate');
        btn.disabled = true;
        btn.textContent = 'Generating...';
        hide($('#profile-ai-error'));
        hide($('#profile-ai-result'));

        try {
            var resp = await api('POST', '/api/v1/profiles/build', { description: desc });
            if (resp.status === 503) {
                var errEl = $('#profile-ai-error');
                errEl.textContent = 'AI generation not available -- configure manually';
                show(errEl);
                return;
            }
            if (!resp.ok) {
                var data = await resp.json().catch(function () { return {}; });
                throw new Error(data.error || data.message || 'Failed to generate profile.');
            }
            var rawResult = await resp.json();
            var result = rawResult.profile || rawResult;

            // Show result for review
            var resultEl = $('#profile-ai-result');
            var resultHtml = '<p style="margin-bottom:8px;color:var(--text);font-weight:600;">Generated Profile:</p>';
            resultHtml += '<p><strong>Name:</strong> ' + escapeHtml(result.name || '') + '</p>';
            resultHtml += '<p><strong>Description:</strong> ' + escapeHtml(result.description || '') + '</p>';
            if (result.tools && typeof result.tools === 'object') {
                resultHtml += '<p style="margin-top:8px;"><strong>Permissions:</strong></p>';
                Object.keys(result.tools).forEach(function (toolName) {
                    var rules = normalizeToolConfig(result.tools[toolName]);
                    if (rules.length <= 1) {
                        var opsStr = rules.length === 1 && Array.isArray(rules[0].operations) ? rules[0].operations.join(', ') : 'all';
                        var reposStr = '';
                        if (rules.length === 1 && Array.isArray(rules[0].repos) && !(rules[0].repos.length === 1 && rules[0].repos[0] === '*')) {
                            reposStr = ' [' + rules[0].repos.join(', ') + ']';
                        }
                        resultHtml += '<div style="margin-left:12px;"><span class="badge badge-builtin" style="margin-right:4px;">' + escapeHtml(toolName) + '</span> ' + escapeHtml(opsStr + reposStr) + '</div>';
                    } else {
                        resultHtml += '<div style="margin-left:12px;"><span class="badge badge-builtin" style="margin-right:4px;">' + escapeHtml(toolName) + '</span>';
                        rules.forEach(function (rule, idx) {
                            var rOps = Array.isArray(rule.operations) ? rule.operations.join(', ') : 'all';
                            var rRepos = Array.isArray(rule.repos) && !(rule.repos.length === 1 && rule.repos[0] === '*') ? ' [' + rule.repos.join(', ') + ']' : '';
                            resultHtml += '<div class="perm-rule-block"><span class="perm-rule-label">Rule ' + (idx + 1) + '</span> ' + escapeHtml(rOps + rRepos) + '</div>';
                        });
                        resultHtml += '</div>';
                    }
                });
            }
            resultHtml += '<div style="margin-top:12px;">' +
                '<button type="button" class="btn btn-small btn-primary" id="profile-ai-accept">Accept &amp; Edit</button> ' +
                '<button type="button" class="btn btn-small btn-outline" id="profile-ai-discard">Discard</button>' +
                '</div>';
            resultEl.innerHTML = resultHtml;
            show(resultEl);

            // Accept button — populate manual form
            $('#profile-ai-accept').addEventListener('click', function () {
                setProfileMode('manual');
                $('#profile-name').value = result.name || '';
                $('#profile-description').value = result.description || '';
                hide(resultEl);
                if (result.tools) {
                    populateProfileToolSelection(result.tools);
                }
            });

            // Discard button
            $('#profile-ai-discard').addEventListener('click', function () {
                hide(resultEl);
            });
        } catch (err) {
            if (err.message !== 'unauthorized') {
                var errEl2 = $('#profile-ai-error');
                errEl2.textContent = err.message;
                show(errEl2);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Generate';
        }
    });

    // Load available tools for profile form checkboxes
    async function loadProfileToolChoices() {
        var container = $('#profile-tool-selection');
        container.innerHTML = '<div class="loading-state"><div class="spinner"></div><p>Loading tools...</p></div>';

        try {
            var resp = await api('GET', '/api/v1/tools');
            var data = await resp.json();
            var tools = Array.isArray(data) ? data : (data.tools || data.items || []);

            if (tools.length === 0) {
                container.innerHTML = '<p style="color:var(--text-muted);font-size:13px;">No tools available.</p>';
                return;
            }

            container.innerHTML = tools.map(function (t) {
                var name = t.name || '';
                var displayName = t.display_name || name;
                var ops = Array.isArray(t.operations) ? t.operations : [];

                return '<div class="profile-tool-item" data-tool-name="' + escapeHtml(name) + '">' +
                    '<label>' + escapeHtml(displayName) + '</label>' +
                    '<div class="profile-tool-rules" data-tool-rules="' + escapeHtml(name) + '">' +
                    renderRuleCard(name, 0, ops) +
                    '</div>' +
                    '<button type="button" class="btn btn-small btn-outline profile-add-rule-btn" data-tool="' + escapeHtml(name) + '">+ Add permission rule</button>' +
                    '</div>';
            }).join('');

            attachRuleHandlers(container);
        } catch (err) {
            if (err.message !== 'unauthorized') {
                container.innerHTML = '<p style="color:var(--status-error);font-size:13px;">Failed to load tools.</p>';
            }
        }
    }

    function renderRuleCard(toolName, ruleIndex, ops) {
        var opsHtml = ops.map(function (op) {
            var opName = typeof op === 'string' ? op : op.name;
            var opLabel = (typeof op === 'object' && op.description) ? op.description : opName;
            return '<label><input type="checkbox" data-profile-tool="' + escapeHtml(toolName) + '" data-profile-op="' + escapeHtml(opName) + '" data-rule-index="' + ruleIndex + '"> ' + escapeHtml(opLabel) + '</label>';
        }).join('');

        return '<div class="profile-rule-card" data-tool="' + escapeHtml(toolName) + '" data-rule-index="' + ruleIndex + '">' +
            '<div class="profile-rule-header">' +
            '<span class="profile-rule-label">Rule ' + (ruleIndex + 1) + '</span>' +
            '<button type="button" class="btn btn-small btn-outline profile-remove-rule-btn" data-tool="' + escapeHtml(toolName) + '" data-rule-index="' + ruleIndex + '"' + (ruleIndex === 0 ? ' hidden' : '') + ' style="font-size:11px;padding:2px 8px;color:var(--status-error);border-color:var(--status-error);">Remove</button>' +
            '</div>' +
            '<div class="profile-tool-ops">' + opsHtml + '</div>' +
            '<div class="profile-tool-repos">' +
            '<label>Repositories</label>' +
            '<div class="repo-tags" data-profile-tool-repos="' + escapeHtml(toolName) + '" data-rule-index="' + ruleIndex + '"></div>' +
            '<div style="display:flex;gap:4px;align-items:center;margin-top:4px;">' +
            '<input type="text" class="input" placeholder="org/repo" data-profile-repo-input="' + escapeHtml(toolName) + '" data-rule-index="' + ruleIndex + '" style="flex:1;">' +
            '<button type="button" class="btn btn-small btn-outline profile-add-repo-btn" data-tool="' + escapeHtml(toolName) + '" data-rule-index="' + ruleIndex + '">Add</button>' +
            '</div>' +
            '<div class="repos-help">Leave empty for all repositories (*). Add specific repos to restrict operations to those repos only.</div>' +
            '</div>' +
            '</div>';
    }

    function attachRuleHandlers(container) {
        // "+ Add permission rule" buttons
        container.querySelectorAll('.profile-add-rule-btn').forEach(function (btn) {
            btn.addEventListener('click', function () {
                var toolName = btn.dataset.tool;
                var rulesContainer = container.querySelector('[data-tool-rules="' + toolName + '"]');
                if (!rulesContainer) return;
                var existingCards = rulesContainer.querySelectorAll('.profile-rule-card');
                var newIndex = existingCards.length;
                // Get ops from the first rule card to know the available operations
                var firstCard = existingCards[0];
                var ops = [];
                if (firstCard) {
                    firstCard.querySelectorAll('[data-profile-op]').forEach(function (cb) {
                        ops.push(cb.dataset.profileOp);
                    });
                }
                var tempDiv = document.createElement('div');
                tempDiv.innerHTML = renderRuleCard(toolName, newIndex, ops);
                var newCard = tempDiv.firstChild;
                rulesContainer.appendChild(newCard);
                // Show all remove buttons when there are multiple rules
                rulesContainer.querySelectorAll('.profile-remove-rule-btn').forEach(function (rb) {
                    rb.hidden = false;
                });
                // Attach handlers for the new card
                attachSingleRuleCardHandlers(newCard);
            });
        });

        // Attach handlers for existing rule cards
        container.querySelectorAll('.profile-rule-card').forEach(function (card) {
            attachSingleRuleCardHandlers(card);
        });
    }

    function attachSingleRuleCardHandlers(card) {
        // Remove button
        var removeBtn = card.querySelector('.profile-remove-rule-btn');
        if (removeBtn) {
            removeBtn.addEventListener('click', function () {
                var toolName = removeBtn.dataset.tool;
                var rulesContainer = card.parentNode;
                card.remove();
                // Re-index remaining cards
                var remaining = rulesContainer.querySelectorAll('.profile-rule-card');
                remaining.forEach(function (c, idx) {
                    c.dataset.ruleIndex = idx;
                    var label = c.querySelector('.profile-rule-label');
                    if (label) label.textContent = 'Rule ' + (idx + 1);
                    c.querySelectorAll('[data-rule-index]').forEach(function (el) {
                        el.dataset.ruleIndex = idx;
                    });
                });
                // Hide remove button if only one rule remains
                if (remaining.length <= 1) {
                    remaining.forEach(function (c) {
                        var rb = c.querySelector('.profile-remove-rule-btn');
                        if (rb) rb.hidden = true;
                    });
                }
            });
        }

        // Repo add button
        card.querySelectorAll('.profile-add-repo-btn').forEach(function (btn) {
            btn.addEventListener('click', function () {
                addRuleRepoTag(card);
            });
        });

        // Enter key in repo input
        card.querySelectorAll('[data-profile-repo-input]').forEach(function (input) {
            input.addEventListener('keydown', function (e) {
                if (e.key === 'Enter') {
                    e.preventDefault();
                    addRuleRepoTag(card);
                }
            });
        });
    }

    function addRuleRepoTag(card) {
        var input = card.querySelector('[data-profile-repo-input]');
        if (!input) return;
        var repo = input.value.trim();
        if (!repo) return;
        if (repo !== '*' && !repo.includes('/')) {
            input.style.borderColor = 'var(--status-error)';
            setTimeout(function () { input.style.borderColor = ''; }, 1500);
            return;
        }
        var tagsContainer = card.querySelector('.repo-tags');
        if (!tagsContainer) return;
        var existing = tagsContainer.querySelectorAll('.repo-tag');
        for (var i = 0; i < existing.length; i++) {
            if (existing[i].dataset.repo === repo) {
                input.value = '';
                return;
            }
        }
        var tag = document.createElement('span');
        tag.className = 'repo-tag';
        tag.dataset.repo = repo;
        tag.innerHTML = escapeHtml(repo) + '<button type="button" class="repo-tag-remove">&times;</button>';
        tag.querySelector('.repo-tag-remove').addEventListener('click', function () {
            tag.remove();
        });
        tagsContainer.appendChild(tag);
        input.value = '';
    }

    function setRuleRepoTags(card, repos) {
        var tagsContainer = card.querySelector('.repo-tags');
        if (!tagsContainer) return;
        tagsContainer.innerHTML = '';
        repos.forEach(function (repo) {
            if (repo === '*') return;
            var tag = document.createElement('span');
            tag.className = 'repo-tag';
            tag.dataset.repo = repo;
            tag.innerHTML = escapeHtml(repo) + '<button type="button" class="repo-tag-remove">&times;</button>';
            tag.querySelector('.repo-tag-remove').addEventListener('click', function () {
                tag.remove();
            });
            tagsContainer.appendChild(tag);
        });
    }

    function getRuleRepoTags(card) {
        var tagsContainer = card.querySelector('.repo-tags');
        if (!tagsContainer) return [];
        var repos = [];
        tagsContainer.querySelectorAll('.repo-tag').forEach(function (tag) {
            repos.push(tag.dataset.repo);
        });
        return repos;
    }

    function normalizeToolConfig(toolConfig) {
        // Normalize any tool config format to rules array
        if (toolConfig && Array.isArray(toolConfig.rules)) {
            return toolConfig.rules;
        }
        // Old format: { operations: [...], repos: [...] }
        if (toolConfig && Array.isArray(toolConfig.operations)) {
            return [{ repos: toolConfig.repos || ['*'], operations: toolConfig.operations }];
        }
        // Bare array of operations
        if (Array.isArray(toolConfig)) {
            return [{ repos: ['*'], operations: toolConfig }];
        }
        return [];
    }

    function populateProfileToolSelection(toolsConfig) {
        setTimeout(function () {
            var container = $('#profile-tool-selection');
            if (!container) return;
            // Uncheck all and clear repo tags
            container.querySelectorAll('input[type="checkbox"]').forEach(function (cb) { cb.checked = false; });
            container.querySelectorAll('.repo-tags').forEach(function (el) { el.innerHTML = ''; });

            Object.keys(toolsConfig).forEach(function (toolName) {
                var rules = normalizeToolConfig(toolsConfig[toolName]);
                if (rules.length === 0) return;

                var rulesContainer = container.querySelector('[data-tool-rules="' + toolName + '"]');
                if (!rulesContainer) return;

                // Get available ops from the first existing card
                var firstCard = rulesContainer.querySelector('.profile-rule-card');
                var availableOps = [];
                if (firstCard) {
                    firstCard.querySelectorAll('[data-profile-op]').forEach(function (cb) {
                        availableOps.push(cb.dataset.profileOp);
                    });
                }

                // Remove existing rule cards beyond the first if needed, or add more
                var existingCards = rulesContainer.querySelectorAll('.profile-rule-card');
                // Remove all existing cards and re-render
                rulesContainer.innerHTML = '';
                rules.forEach(function (rule, idx) {
                    var tempDiv = document.createElement('div');
                    tempDiv.innerHTML = renderRuleCard(toolName, idx, availableOps);
                    var card = tempDiv.firstChild;
                    rulesContainer.appendChild(card);

                    // Check the matching operations
                    var opsList = Array.isArray(rule.operations) ? rule.operations : [];
                    opsList.forEach(function (op) {
                        var opName = typeof op === 'string' ? op : op.name;
                        var cb = card.querySelector('[data-profile-tool="' + toolName + '"][data-profile-op="' + opName + '"]');
                        if (cb) cb.checked = true;
                    });

                    // Set repo tags
                    var reposList = Array.isArray(rule.repos) ? rule.repos : ['*'];
                    setRuleRepoTags(card, reposList);

                    // Attach handlers
                    attachSingleRuleCardHandlers(card);
                });

                // Show/hide remove buttons based on rule count
                var allCards = rulesContainer.querySelectorAll('.profile-rule-card');
                allCards.forEach(function (c) {
                    var rb = c.querySelector('.profile-remove-rule-btn');
                    if (rb) rb.hidden = allCards.length <= 1;
                });
            });
        }, 200);
    }

    function getProfileToolsFromForm() {
        var container = $('#profile-tool-selection');
        if (!container) return {};
        var tools = {};

        container.querySelectorAll('.profile-tool-item').forEach(function (toolItem) {
            var toolName = toolItem.dataset.toolName;
            if (!toolName) return;

            var ruleCards = toolItem.querySelectorAll('.profile-rule-card');
            var rules = [];

            ruleCards.forEach(function (card) {
                var ops = [];
                card.querySelectorAll('input[type="checkbox"]:checked').forEach(function (cb) {
                    if (cb.dataset.profileOp) {
                        ops.push(cb.dataset.profileOp);
                    }
                });
                if (ops.length === 0) return; // skip rules with no operations checked

                var repos = getRuleRepoTags(card);
                if (repos.length === 0) repos = ['*'];

                rules.push({ repos: repos, operations: ops });
            });

            if (rules.length > 0) {
                tools[toolName] = { rules: rules };
            }
        });

        return tools;
    }

    // Submit profile form
    $('#profile-form').addEventListener('submit', async function (e) {
        e.preventDefault();
        var errEl = $('#profile-form-error');
        hide(errEl);

        // If AI mode is active and a result was generated, auto-accept it
        var aiResultEl = $('#profile-ai-result');
        if (aiResultEl && !aiResultEl.hidden) {
            var acceptBtn = $('#profile-ai-accept');
            if (acceptBtn) acceptBtn.click();
        }

        var name = $('#profile-name').value.trim();
        if (!name) {
            errEl.textContent = 'Name is required.';
            show(errEl);
            return;
        }

        var toolsConfig = getProfileToolsFromForm();
        var payload = {
            name: name,
            description: $('#profile-description').value.trim() || undefined,
            tools: Object.keys(toolsConfig).length > 0 ? toolsConfig : undefined
        };

        Object.keys(payload).forEach(function (k) {
            if (payload[k] === undefined) delete payload[k];
        });

        var btn = $('#profile-submit-btn');
        btn.disabled = true;
        btn.textContent = editingProfileId ? 'Updating...' : 'Saving...';

        try {
            var resp;
            if (editingProfileId) {
                resp = await api('PUT', '/api/v1/profiles/' + encodeURIComponent(editingProfileId), payload);
            } else {
                resp = await api('POST', '/api/v1/profiles', payload);
            }

            if (!resp.ok) {
                var data = await resp.json().catch(function () { return {}; });
                throw new Error(data.error || data.message || 'Failed to save profile.');
            }

            hide($('#profile-form-container'));
            $('#profile-form').reset();
            editingProfileId = null;
            loadProfilesPage();
        } catch (err) {
            if (err.message !== 'unauthorized') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Save Profile';
        }
    });

    // Back to profiles from tools admin
    $('#back-to-profiles').addEventListener('click', function () {
        navigate('profiles');
    });

    // Tools admin link from profiles page
    $('#profiles-tools-admin-link').addEventListener('click', function (e) {
        e.preventDefault();
        navigate('tools-admin');
    });

    // ---------------------
    // Task Profile Selector
    // ---------------------
    async function loadTaskProfiles() {
        var select = $('#task-profile-add');
        select.innerHTML = '<option value="">+ Add profile...</option>';

        try {
            var resp = await api('GET', '/api/v1/profiles');
            var data = await resp.json();
            var profiles = Array.isArray(data) ? data : (data.profiles || data.items || []);
            allProfiles = profiles;

            profiles.forEach(function (p) {
                var name = p.name || '';
                var desc = p.description ? ' -- ' + truncate(p.description, 40) : '';
                var opt = document.createElement('option');
                opt.value = name;
                opt.textContent = name + desc;
                select.appendChild(opt);
            });
        } catch (err) {
            if (err.message !== 'unauthorized') {
                select.innerHTML = '<option value="">Failed to load profiles</option>';
            }
        }

        renderProfileChips();
        updateEffectivePermissions();
        checkTaskPrerequisites();
    }

    // Add profile chip when selected from dropdown
    $('#task-profile-add').addEventListener('change', function () {
        var name = this.value;
        if (!name) return;
        addProfileChip(name);
        this.value = '';
    });

    function addProfileChip(name) {
        if (selectedProfiles.includes(name)) return;
        selectedProfiles.push(name);
        renderProfileChips();
        updateEffectivePermissions();
        checkTaskPrerequisites();
    }

    function removeProfileChip(name) {
        selectedProfiles = selectedProfiles.filter(function (n) { return n !== name; });
        renderProfileChips();
        updateEffectivePermissions();
        checkTaskPrerequisites();
    }

    function renderProfileChips() {
        var container = $('#task-profile-chips');
        if (!container) return;

        container.innerHTML = selectedProfiles.map(function (name) {
            var profile = allProfiles.find(function (p) { return p.name === name; });
            var displayName = profile ? (profile.display_name || profile.name) : name;
            return '<span class="profile-chip">' +
                escapeHtml(displayName) +
                '<button type="button" class="profile-chip-remove" data-name="' + escapeHtml(name) + '">&times;</button>' +
                '</span>';
        }).join('');

        container.querySelectorAll('.profile-chip-remove').forEach(function (btn) {
            btn.addEventListener('click', function () {
                removeProfileChip(btn.dataset.name);
            });
        });
    }

    function updateEffectivePermissions() {
        var effectiveEl = $('#task-profile-effective');
        var stackingHint = $('#task-profile-stacking-hint');
        if (!effectiveEl) return;

        if (selectedProfiles.length === 0) {
            hide(effectiveEl);
            if (stackingHint) hide(stackingHint);
            return;
        }

        // Show stacking hint when 1+ profiles selected to educate about composition
        if (stackingHint) {
            if (selectedProfiles.length >= 1) show(stackingHint);
            else hide(stackingHint);
        }

        // Merge permissions from all selected profiles, tracking repo scoping and sources
        // Structure: merged[toolName][repoScope] = { ops: Set, sources: Set }
        // repoScope is "*" for global or "org/repo" for specific repos
        var merged = {};
        selectedProfiles.forEach(function (profileName) {
            var profile = allProfiles.find(function (p) { return p.name === profileName; });
            if (!profile || !profile.tools) return;
            Object.keys(profile.tools).forEach(function (toolName) {
                if (!merged[toolName]) merged[toolName] = {};
                var rules = normalizeToolConfig(profile.tools[toolName]);
                rules.forEach(function (rule) {
                    var opsList = Array.isArray(rule.operations) ? rule.operations : [];
                    var reposList = Array.isArray(rule.repos) && rule.repos.length > 0 ? rule.repos : ['*'];
                    reposList.forEach(function (repo) {
                        var key = repo || '*';
                        if (!merged[toolName][key]) merged[toolName][key] = { ops: new Set(), sources: new Set() };
                        opsList.forEach(function (op) {
                            merged[toolName][key].ops.add(typeof op === 'string' ? op : op.name);
                        });
                        merged[toolName][key].sources.add(profileName);
                    });
                });
            });
        });

        if (Object.keys(merged).length === 0) {
            effectiveEl.textContent = 'No tool permissions from selected profiles.';
            show(effectiveEl);
            return;
        }

        var html = '<strong style="color:var(--text);font-size:11px;text-transform:uppercase;letter-spacing:0.5px;">Effective Permissions</strong>';
        Object.keys(merged).forEach(function (toolName) {
            html += '<div class="eff-tool-block"><span class="eff-tool-name">' + escapeHtml(toolName) + '</span>';
            var scopes = merged[toolName];
            // Show global scope first, then specific repos
            var scopeKeys = Object.keys(scopes).sort(function (a, b) {
                if (a === '*') return -1;
                if (b === '*') return 1;
                return a.localeCompare(b);
            });
            scopeKeys.forEach(function (scope) {
                var entry = scopes[scope];
                var ops = Array.from(entry.ops);
                var sources = Array.from(entry.sources);
                var scopeLabel = '';
                if (scope === '*') {
                    scopeLabel = '<span class="eff-scope-label eff-scope-label-global">All repositories</span>';
                } else {
                    scopeLabel = '<span class="eff-scope-label eff-scope-label-repo">' + escapeHtml(scope) + '</span>';
                }
                var sourceHint = selectedProfiles.length > 1 ? '<span class="eff-source">(from ' + sources.map(function (s) { return escapeHtml(s); }).join(', ') + ')</span>' : '';
                html += '<div class="eff-scope">' + scopeLabel + sourceHint +
                    '<div class="eff-ops">' + ops.map(function (o) { return escapeHtml(o); }).join(', ') + '</div>' +
                    '</div>';
            });
            html += '</div>';
        });

        effectiveEl.innerHTML = html;
        show(effectiveEl);
    }

    // ---------------------
    // Tools page
    // ---------------------
    const toolPresets = {
        read_only: ['clone', 'read_prs', 'read_issues', 'read_contents', 'read_mrs', 'read_pipelines', 'read_actions'],
        contributor: ['clone', 'read_prs', 'read_issues', 'read_contents', 'read_mrs', 'read_pipelines', 'read_actions', 'push_branch', 'create_pr_draft', 'create_mr_draft', 'create_comment'],
        maintainer: ['clone', 'read_prs', 'read_issues', 'read_contents', 'read_mrs', 'read_pipelines', 'read_actions', 'push_branch', 'create_pr_draft', 'create_mr_draft', 'create_pr', 'create_mr', 'create_comment', 'merge_pr', 'merge_mr', 'create_branch', 'delete_branch'],
    };

    async function loadToolsPage() {
        const tbody = $('#tools-tbody');
        const loading = $('#tools-loading');
        const empty = $('#tools-empty');

        tbody.innerHTML = '';
        show(loading);
        hide(empty);

        try {
            const resp = await api('GET', '/api/v1/tools');
            const data = await resp.json();
            hide(loading);

            const tools = Array.isArray(data) ? data : (data.tools || data.items || []);
            if (tools.length === 0) {
                show(empty);
                return;
            }

            tbody.innerHTML = tools.map(function (t) {
                const name = t.name || '-';
                const displayName = t.display_name || name;
                const isBuiltin = t.type === 'builtin' || t.builtin === true;
                const typeBadge = isBuiltin
                    ? '<span class="badge badge-builtin">builtin</span>'
                    : '<span class="badge badge-custom">custom</span>';
                const ops = Array.isArray(t.operations) ? t.operations.length : 0;
                const apiHost = t.api_host || '-';
                const id = t.id || t.name || '';

                var actions = '';
                if (isBuiltin) {
                    actions = '<button class="btn btn-small btn-outline view-tool-btn" data-id="' + escapeHtml(id) + '">View</button>';
                } else {
                    actions = '<button class="btn btn-small btn-outline edit-tool-btn" data-id="' + escapeHtml(id) + '">Edit</button> ' +
                        '<button class="btn btn-small btn-outline delete-tool-btn" data-id="' + escapeHtml(id) + '" data-name="' + escapeHtml(name) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button>';
                }

                return '<tr>' +
                    '<td>' + escapeHtml(displayName) + '</td>' +
                    '<td>' + typeBadge + '</td>' +
                    '<td>' + ops + '</td>' +
                    '<td class="mono">' + escapeHtml(apiHost) + '</td>' +
                    '<td>' + actions + '</td>' +
                    '</tr>';
            }).join('');

            // View handlers (builtin)
            tbody.querySelectorAll('.view-tool-btn').forEach(function (btn) {
                btn.addEventListener('click', async function () {
                    try {
                        const resp = await api('GET', '/api/v1/tools/' + encodeURIComponent(btn.dataset.id));
                        const t = await resp.json();
                        var info = 'Name: ' + (t.name || '-') + '\n';
                        info += 'Display Name: ' + (t.display_name || '-') + '\n';
                        info += 'Type: builtin\n';
                        info += 'API Host: ' + (t.api_host || '-') + '\n';
                        info += 'Operations: ' + (Array.isArray(t.operations) ? t.operations.map(function (op) { return typeof op === 'string' ? op : op.name; }).join(', ') : '-');
                        alert(info);
                    } catch (err) {
                        if (err.message !== 'unauthorized') alert('Failed to load tool details.');
                    }
                });
            });

            // Edit handlers (custom)
            tbody.querySelectorAll('.edit-tool-btn').forEach(function (btn) {
                btn.addEventListener('click', async function () {
                    try {
                        const resp = await api('GET', '/api/v1/tools/' + encodeURIComponent(btn.dataset.id));
                        const t = await resp.json();
                        $('#tool-name').value = t.name || '';
                        $('#tool-display-name').value = t.display_name || '';
                        $('#tool-mcp-command').value = t.mcp_command || '';
                        $('#tool-api-host').value = t.api_host || '';
                        $('#tool-auth-header').value = t.auth_header || '';
                        $('#tool-auth-format').value = t.auth_format || '';
                        $('#tool-operations').value = Array.isArray(t.operations) ? t.operations.map(function (op) { return typeof op === 'string' ? op : op.name; }).join('\n') : '';
                        $('#tool-submit-btn').textContent = 'Update Tool';
                        $('#tool-form').dataset.editId = btn.dataset.id;
                        show($('#tool-form-container'));
                        $('#tool-name').focus();
                    } catch (err) {
                        if (err.message !== 'unauthorized') alert('Failed to load tool.');
                    }
                });
            });

            // Delete handlers (custom)
            tbody.querySelectorAll('.delete-tool-btn').forEach(function (btn) {
                btn.addEventListener('click', async function () {
                    var name = btn.dataset.name;
                    if (!confirm('Are you sure you want to delete tool "' + name + '"?')) return;
                    btn.disabled = true;
                    try {
                        const resp = await api('DELETE', '/api/v1/tools/' + encodeURIComponent(btn.dataset.id));
                        if (!resp.ok) {
                            const data = await resp.json().catch(function () { return {}; });
                            alert(data.error || data.message || 'Failed to delete tool.');
                            btn.disabled = false;
                        } else {
                            loadToolsPage();
                        }
                    } catch (err) {
                        if (err.message !== 'unauthorized') alert('Failed to delete tool.');
                        btn.disabled = false;
                    }
                });
            });
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized') {
                tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;color:var(--status-error);">Failed to load tools.</td></tr>';
            }
        }
    }

    // Show create tool form
    $('#show-create-tool').addEventListener('click', function () {
        $('#tool-form').reset();
        delete $('#tool-form').dataset.editId;
        hide($('#tool-form-error'));
        $('#tool-submit-btn').textContent = 'Add Tool';
        show($('#tool-form-container'));
        $('#tool-name').focus();
    });

    // Cancel create tool
    $('#cancel-create-tool').addEventListener('click', function () {
        hide($('#tool-form-container'));
        $('#tool-form').reset();
        delete $('#tool-form').dataset.editId;
        hide($('#tool-form-error'));
    });

    // Submit tool form
    $('#tool-form').addEventListener('submit', async function (e) {
        e.preventDefault();
        const errEl = $('#tool-form-error');
        hide(errEl);

        const name = $('#tool-name').value.trim();
        const mcpCommand = $('#tool-mcp-command').value.trim();

        if (!name) {
            errEl.textContent = 'Name is required.';
            show(errEl);
            return;
        }
        if (!mcpCommand) {
            errEl.textContent = 'MCP Command is required.';
            show(errEl);
            return;
        }

        const opsText = $('#tool-operations').value.trim();
        const operations = opsText ? opsText.split('\n').map(function (s) { return s.trim(); }).filter(function (s) { return s; }) : [];

        var payload = {
            name: name,
            display_name: $('#tool-display-name').value.trim() || undefined,
            mcp_command: mcpCommand,
            api_host: $('#tool-api-host').value.trim() || undefined,
            auth_header: $('#tool-auth-header').value.trim() || undefined,
            auth_format: $('#tool-auth-format').value.trim() || undefined,
            operations: operations.length > 0 ? operations : undefined
        };

        Object.keys(payload).forEach(function (k) {
            if (payload[k] === undefined) delete payload[k];
        });

        const btn = $('#tool-submit-btn');
        btn.disabled = true;
        const editId = $('#tool-form').dataset.editId;
        btn.textContent = editId ? 'Updating...' : 'Adding...';

        try {
            var resp;
            if (editId) {
                resp = await api('PUT', '/api/v1/tools/' + encodeURIComponent(editId), payload);
            } else {
                resp = await api('POST', '/api/v1/tools', payload);
            }

            if (!resp.ok) {
                const data = await resp.json().catch(function () { return {}; });
                throw new Error(data.error || data.message || 'Failed to save tool.');
            }

            hide($('#tool-form-container'));
            $('#tool-form').reset();
            delete $('#tool-form').dataset.editId;
            loadToolsPage();
        } catch (err) {
            if (err.message !== 'unauthorized') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Add Tool';
        }
    });

    // ---------------------
    // Task Tools (New Task form)
    // ---------------------
    async function loadTaskTools() {
        const content = $('#task-tools-content');
        content.innerHTML = '<div class="loading-state"><div class="spinner"></div><p>Loading tools...</p></div>';

        try {
            const resp = await api('GET', '/api/v1/tools');
            const data = await resp.json();
            const tools = Array.isArray(data) ? data : (data.tools || data.items || []);

            if (tools.length === 0) {
                content.innerHTML = '<p style="color:var(--text-muted);font-size:13px;">No tools available.</p>';
                return;
            }

            content.innerHTML = tools.map(function (t) {
                const name = t.name || '';
                const displayName = t.display_name || name;
                const hasPresets = Array.isArray(t.operations) && t.operations.length > 0;

                var presetOptions = '<option value="">Select permissions...</option>' +
                    '<option value="read_only">Read Only</option>' +
                    '<option value="contributor">Contributor</option>' +
                    '<option value="maintainer">Maintainer</option>' +
                    '<option value="custom">Custom...</option>';

                var html = '<div class="tool-card" data-tool-name="' + escapeHtml(name) + '">' +
                    '<label class="tool-toggle">' +
                    '<input type="checkbox" data-tool="' + escapeHtml(name) + '"> ' + escapeHtml(displayName) +
                    '</label>' +
                    '<select class="input tool-preset" data-tool="' + escapeHtml(name) + '">' +
                    presetOptions +
                    '</select>' +
                    '<div class="tool-repos" hidden>' +
                    '<input type="text" class="input" placeholder="org/repo (optional)" data-tool-repo="' + escapeHtml(name) + '">' +
                    '</div>' +
                    '</div>';
                return html;
            }).join('');

            // Attach event listeners
            content.querySelectorAll('.tool-card input[type="checkbox"]').forEach(function (cb) {
                cb.addEventListener('change', function () {
                    updateToolsSummary();
                });
            });

            content.querySelectorAll('.tool-card .tool-preset').forEach(function (sel) {
                sel.addEventListener('change', function () {
                    var toolName = sel.dataset.tool;
                    var card = sel.closest('.tool-card');
                    var repoDiv = card.querySelector('.tool-repos');
                    if (sel.value === 'custom') {
                        repoDiv.hidden = false;
                    } else {
                        repoDiv.hidden = true;
                    }
                    // Auto-check the checkbox when a preset is selected
                    var cb = card.querySelector('input[type="checkbox"]');
                    if (sel.value && !cb.checked) {
                        cb.checked = true;
                    }
                    updateToolsSummary();
                });
            });
        } catch (err) {
            if (err.message !== 'unauthorized') {
                content.innerHTML = '<p style="color:var(--status-error);font-size:13px;">Failed to load tools.</p>';
            }
        }
    }

    function updateToolsSummary() {
        var summary = $('#task-tools-summary');
        var content = $('#task-tools-content');
        var enabled = [];

        content.querySelectorAll('.tool-card').forEach(function (card) {
            var cb = card.querySelector('input[type="checkbox"]');
            if (cb && cb.checked) {
                var toolName = card.dataset.toolName || cb.dataset.tool;
                var displayName = card.querySelector('.tool-toggle').textContent.trim();
                var preset = card.querySelector('.tool-preset');
                var presetLabel = '';
                if (preset && preset.value && preset.value !== 'custom') {
                    var opt = preset.options[preset.selectedIndex];
                    presetLabel = opt ? opt.textContent : '';
                }
                if (presetLabel) {
                    enabled.push(displayName + ': ' + presetLabel);
                } else {
                    enabled.push(displayName);
                }
            }
        });

        if (enabled.length === 0) {
            summary.textContent = 'Tools (none enabled)';
        } else {
            summary.textContent = 'Tools (' + enabled.join(', ') + ')';
        }
    }

    function getToolsConfig() {
        var tools = {};
        var content = $('#task-tools-content');
        if (!content) return tools;

        content.querySelectorAll('.tool-card').forEach(function (card) {
            var cb = card.querySelector('input[type="checkbox"]');
            if (!cb || !cb.checked) return;

            var toolName = cb.dataset.tool;
            var preset = card.querySelector('.tool-preset');
            var presetValue = preset ? preset.value : '';
            var operations = [];

            if (presetValue && presetValue !== 'custom' && toolPresets[presetValue]) {
                operations = toolPresets[presetValue].slice();
            } else if (presetValue === 'custom') {
                // For custom, send empty operations (server decides)
                operations = [];
            }

            var repoInput = card.querySelector('[data-tool-repo]');
            var repo = repoInput ? repoInput.value.trim() : '';

            var toolConfig = { enabled: true };
            if (operations.length > 0) toolConfig.operations = operations;
            if (presetValue) toolConfig.preset = presetValue;
            if (repo) toolConfig.repo = repo;

            tools[toolName] = toolConfig;
        });

        return tools;
    }

    function resetTaskTools() {
        var content = $('#task-tools-content');
        if (!content) return;
        content.querySelectorAll('.tool-card input[type="checkbox"]').forEach(function (cb) {
            cb.checked = false;
        });
        content.querySelectorAll('.tool-card .tool-preset').forEach(function (sel) {
            sel.selectedIndex = 0;
        });
        content.querySelectorAll('.tool-card .tool-repos').forEach(function (div) {
            div.hidden = true;
            var input = div.querySelector('input');
            if (input) input.value = '';
        });
        updateToolsSummary();
    }

    // ---------------------
    // System LLM modal
    // ---------------------
    async function loadSystemLLMModal() {
        try {
            var resp = await api('GET', '/api/v1/admin/settings/llm');
            var eff = await resp.json();
            renderSystemLLMStatus(eff);
        } catch(err) {
            $('#modal-llm-status').innerHTML = '<p style="color:var(--status-error)">Failed to load settings.</p>';
        }
    }

    function renderSystemLLMStatus(eff) {
        var el = $('#modal-llm-status');
        if (!eff.configured) {
            el.innerHTML = '<p style="color:var(--text-muted)">System LLM is not configured. AI features (like the profile builder) are disabled.</p>';
            return;
        }

        function srcBadge(src) {
            if (src === 'env') return '<span class="badge" style="font-size:10px;margin-left:4px;">ENV</span>';
            if (src === 'database') return '<span class="badge badge-running" style="font-size:10px;margin-left:4px;">DB</span>';
            return '<span class="badge" style="font-size:10px;margin-left:4px;">default</span>';
        }

        el.innerHTML = '<div class="session-meta-grid">' +
            '<div class="meta-card"><div class="meta-label">Provider</div><div class="meta-value">' + escapeHtml(eff.provider || '-') + srcBadge(eff.provider_source) + '</div></div>' +
            '<div class="meta-card"><div class="meta-label">Model</div><div class="meta-value">' + escapeHtml(eff.model || '-') + srcBadge(eff.model_source) + '</div></div>' +
            '<div class="meta-card"><div class="meta-label">Region</div><div class="meta-value">' + escapeHtml(eff.region || '-') + srcBadge(eff.region_source) + '</div></div>' +
            '<div class="meta-card"><div class="meta-label">Project ID</div><div class="meta-value">' + escapeHtml(eff.project_id || '-') + srcBadge(eff.project_id_source) + '</div></div>' +
            '</div>';
    }

    // Configure System LLM button
    $('#modal-llm-configure').addEventListener('click', function() {
        var container = $('#modal-llm-form-container');
        show(container);
        initCredentialForm(container, {
            showName: false,
            submitLabel: 'Save System LLM',
            onSubmit: async function(payload) {
                // Send credential data inline to the settings endpoint.
                // The server saves it as a system credential (owner='_system'),
                // keeping it off the user's LLMs page.
                var settingsResp = await api('PUT', '/api/v1/admin/settings/llm', {
                    provider: payload.provider,
                    model: 'claude-sonnet-4-20250514',
                    region: payload.region || 'us-east5',
                    project_id: payload.project_id || '',
                    credential: payload.credential,
                    auth_type: payload.auth_type
                });
                if (!settingsResp.ok) throw new Error((await settingsResp.json().catch(function() { return {}; })).error || 'Failed');

                hide(container);
                loadSystemLLMModal();
            },
            onCancel: function() { hide(container); }
        });
    });

    // ---------------------
    // Utilities
    // ---------------------
    function truncate(str, len) {
        if (!str) return '';
        return str.length > len ? str.substring(0, len) + '...' : str;
    }

    function formatTime(ts) {
        if (!ts) return '-';
        try {
            const d = new Date(ts);
            if (isNaN(d.getTime())) return '-';
            return d.toLocaleString();
        } catch (e) {
            return ts;
        }
    }

    function formatDuration(startedAt, finishedAt, durationField) {
        if (durationField) {
            if (typeof durationField === 'number') {
                return humanDuration(durationField);
            }
            return String(durationField);
        }
        if (!startedAt) return '-';
        const start = new Date(startedAt);
        const end = finishedAt ? new Date(finishedAt) : new Date();
        if (isNaN(start.getTime())) return '-';
        const diffMs = end.getTime() - start.getTime();
        if (diffMs < 0) return '-';
        return humanDuration(Math.floor(diffMs / 1000));
    }

    function humanDuration(seconds) {
        if (seconds < 60) return seconds + 's';
        const m = Math.floor(seconds / 60);
        const s = seconds % 60;
        if (m < 60) return m + 'm ' + s + 's';
        const h = Math.floor(m / 60);
        const rm = m % 60;
        return h + 'h ' + rm + 'm';
    }

    function debounce(fn, delay) {
        let timer;
        return function () {
            clearTimeout(timer);
            timer = setTimeout(fn, delay);
        };
    }

    // ---------------------
    // Task Prerequisites & Warnings
    // ---------------------
    function checkTaskPrerequisites() {
        var container = $('#task-warnings');
        if (!container) return;

        var warnings = [];

        var llmCreds = cachedCredentials.filter(function (c) {
            return c.provider !== 'github' && c.provider !== 'gitlab';
        });
        var scmCreds = cachedCredentials.filter(function (c) {
            return c.provider === 'github' || c.provider === 'gitlab';
        });
        var hasGithubCred = scmCreds.some(function (c) { return c.provider === 'github'; });
        var hasGitlabCred = scmCreds.some(function (c) { return c.provider === 'gitlab'; });

        // Warning: no LLM credential
        if (llmCreds.length === 0) {
            warnings.push({
                type: 'caution',
                text: 'No LLM provider configured. Your task won\'t be able to reach an AI model. <a href="#credentials">Add one on the Credentials page.</a>'
            });
        }

        // Warning: selected profile uses GitHub/GitLab but no matching credential
        selectedProfiles.forEach(function (profileName) {
            var profile = allProfiles.find(function (p) { return p.name === profileName; });
            if (!profile || !profile.tools) return;
            var toolNames = Object.keys(profile.tools);
            var usesGithub = toolNames.some(function (t) { return t.toLowerCase().indexOf('github') !== -1; });
            var usesGitlab = toolNames.some(function (t) { return t.toLowerCase().indexOf('gitlab') !== -1; });

            if (usesGithub && !hasGithubCred) {
                warnings.push({
                    type: 'caution',
                    text: 'Profile "' + escapeHtml(profileName) + '" uses GitHub, but no GitHub credential is configured. <a href="#credentials">Add a GitHub PAT on the Credentials page.</a>'
                });
            }
            if (usesGitlab && !hasGitlabCred) {
                warnings.push({
                    type: 'caution',
                    text: 'Profile "' + escapeHtml(profileName) + '" uses GitLab, but no GitLab credential is configured. <a href="#credentials">Add a GitLab token on the Credentials page.</a>'
                });
            }
        });

        // Info: no profiles selected but some are available
        if (selectedProfiles.length === 0 && allProfiles.length > 0) {
            warnings.push({
                type: 'info',
                text: 'No security profiles selected. The agent won\'t have access to any code platforms.'
            });
        }

        // Disable submit button if no LLM credential
        var submitBtn = document.querySelector('#task-form button[type="submit"]');
        if (submitBtn) {
            if (llmCreds.length === 0) {
                submitBtn.disabled = true;
                submitBtn.textContent = 'Submit Task (LLM credential required)';
                submitBtn.title = 'Add an LLM credential on the Credentials page before submitting tasks.';
            } else {
                submitBtn.disabled = false;
                submitBtn.textContent = 'Submit Task';
                submitBtn.title = '';
            }
        }

        // Render warnings
        if (warnings.length === 0) {
            hide(container);
            container.innerHTML = '';
            return;
        }

        container.innerHTML = warnings.map(function (w) {
            var icon = w.type === 'caution' ? '&#9888;' : '&#8505;';
            return '<div class="task-warning task-warning-' + w.type + '">' +
                '<span class="task-warning-icon">' + icon + '</span>' +
                '<span>' + w.text + '</span>' +
                '</div>';
        }).join('');
        show(container);
    }

    // ---------------------
    // Smart Prompt Analysis
    // ---------------------
    (function () {
        var promptEl = $('#task-prompt');
        if (!promptEl) return;

        var analyzePrompt = debounce(function () {
            var text = promptEl.value.toLowerCase();
            if (!text || text.length < 5) return;

            var container = $('#task-warnings');
            if (!container) return;

            // Remove any previous prompt suggestions
            container.querySelectorAll('.task-warning-prompt-hint').forEach(function (el) {
                el.remove();
            });

            var scmCreds = cachedCredentials.filter(function (c) {
                return c.provider === 'github' || c.provider === 'gitlab';
            });
            var hasGithubCred = scmCreds.some(function (c) { return c.provider === 'github'; });
            var hasGitlabCred = scmCreds.some(function (c) { return c.provider === 'gitlab'; });
            var hasGithubProfile = selectedProfiles.some(function (pName) {
                var p = allProfiles.find(function (pp) { return pp.name === pName; });
                if (!p || !p.tools) return false;
                return Object.keys(p.tools).some(function (t) { return t.toLowerCase().indexOf('github') !== -1; });
            });
            var hasGitlabProfile = selectedProfiles.some(function (pName) {
                var p = allProfiles.find(function (pp) { return pp.name === pName; });
                if (!p || !p.tools) return false;
                return Object.keys(p.tools).some(function (t) { return t.toLowerCase().indexOf('gitlab') !== -1; });
            });

            var suggestions = [];

            // GitHub-related keywords
            if (/\b(github|pull\s*request|\bpr\b)\b/.test(text) && !hasGithubProfile) {
                var msg = 'Your prompt mentions GitHub.';
                if (!hasGithubCred) {
                    msg += ' Consider <a href="#credentials">adding a GitHub credential</a> and';
                } else {
                    msg += ' Consider';
                }
                msg += ' selecting a profile with GitHub access.';
                suggestions.push(msg);
            }

            // GitLab-related keywords
            if (/\b(gitlab|merge\s*request|\bmr\b)\b/.test(text) && !hasGitlabProfile) {
                var glMsg = 'Your prompt mentions GitLab.';
                if (!hasGitlabCred) {
                    glMsg += ' Consider <a href="#credentials">adding a GitLab credential</a> and';
                } else {
                    glMsg += ' Consider';
                }
                glMsg += ' selecting a profile with GitLab access.';
                suggestions.push(glMsg);
            }

            // Clone/repo keywords without any SCM
            if (/\b(clone|repo|repository)\b/.test(text) && scmCreds.length === 0 && !hasGithubProfile && !hasGitlabProfile) {
                suggestions.push('Your prompt mentions repositories. Consider <a href="#credentials">adding SCM credentials</a> for code platform access.');
            }

            if (suggestions.length > 0) {
                show(container);
                suggestions.forEach(function (s) {
                    var div = document.createElement('div');
                    div.className = 'task-warning task-warning-info task-warning-prompt-hint';
                    div.innerHTML = '<span class="task-warning-icon">&#128161;</span><span>' + s + '</span>';
                    container.appendChild(div);
                });
            }
        }, 500);

        promptEl.addEventListener('input', analyzePrompt);
    })();

    // ---------------------
    // Setup Checklist
    // ---------------------
    async function updateSetupChecklist() {
        var checklist = $('#setup-checklist');
        if (!checklist || setupChecklistDismissed) return;

        try {
            var credResp = await api('GET', '/api/v1/credentials');
            var credData = await credResp.json();
            var allCreds = credData.credentials || [];
            cachedCredentials = allCreds;

            var profileResp = await api('GET', '/api/v1/profiles');
            var profileData = await profileResp.json();
            var profiles = Array.isArray(profileData) ? profileData : (profileData.profiles || profileData.items || []);

            var sessResp = await api('GET', '/api/v1/sessions?limit=1');
            var sessData = await sessResp.json();
            var sessions = Array.isArray(sessData) ? sessData : (sessData.sessions || sessData.items || []);

            var llmCreds = allCreds.filter(function (c) {
                return c.provider !== 'github' && c.provider !== 'gitlab';
            });
            var scmCreds = allCreds.filter(function (c) {
                return c.provider === 'github' || c.provider === 'gitlab';
            });

            var hasLlm = llmCreds.length > 0;
            var hasScm = scmCreds.length > 0;
            var hasProfile = profiles.length > 0;
            var hasTask = sessions.length > 0;

            // If everything is done, hide the checklist entirely
            if (hasLlm && hasScm && hasProfile && hasTask) {
                hide(checklist);
                return;
            }

            // Update username
            var usernameEl = $('#setup-username');
            if (usernameEl) usernameEl.textContent = localStorage.getItem('alcove_user') || 'user';

            // Update each item
            updateChecklistItem('setup-item-llm', hasLlm);
            updateChecklistItem('setup-item-scm', hasScm);
            updateChecklistItem('setup-item-profile', hasProfile);
            updateChecklistItem('setup-item-task', hasTask);

            show(checklist);
        } catch (err) {
            // Don't show checklist if we can't fetch data
            if (err.message !== 'unauthorized') {
                hide(checklist);
            }
        }
    }

    function updateChecklistItem(itemId, isDone) {
        var el = $('#' + itemId);
        if (!el) return;
        var check = el.querySelector('.setup-check');
        var action = el.querySelector('.setup-action');

        if (isDone) {
            el.classList.add('setup-item-done');
            check.innerHTML = '&#10003;';
            if (action) action.style.display = 'none';
        } else {
            el.classList.remove('setup-item-done');
            if (itemId === 'setup-item-task') {
                check.innerHTML = '&#9675;';
            } else {
                check.innerHTML = '&#10007;';
            }
            if (action) action.style.display = '';
        }
    }

    // Dismiss checklist button
    (function () {
        var btn = $('#setup-checklist-dismiss');
        if (btn) {
            btn.addEventListener('click', function () {
                setupChecklistDismissed = true;
                localStorage.setItem('alcove_setup_dismissed', 'true');
                hide($('#setup-checklist'));
            });
        }
    })();

    // ---------------------
    // Init
    // ---------------------
    handleRoute();
})();
