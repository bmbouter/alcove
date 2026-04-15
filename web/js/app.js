// Alcove Dashboard — Single Page Application
(function () {
    'use strict';

    // Detect base path for subpath deployments (e.g., /app/alcove/)
    // When served at /, basePath is empty. When at /app/alcove/, basePath is '/app/alcove'.
    var basePath = (function() {
        var path = window.location.pathname;
        // If the pathname ends with index.html, strip it
        path = path.replace(/\/index\.html$/, '');
        // Strip trailing slash
        path = path.replace(/\/$/, '');
        return path;
    })();

    // ---------------------
    // API helper
    // ---------------------
    async function api(method, path, body) {
        const token = localStorage.getItem('alcove_token');
        const headers = { 'Content-Type': 'application/json' };
        if (token && !rhIdentityMode) {
            headers['Authorization'] = 'Bearer ' + token;
        }
        // Include active team header on all requests
        if (activeTeamId) {
            headers['X-Alcove-Team'] = activeTeamId;
        }
        const opts = { method, headers };
        if (body) opts.body = JSON.stringify(body);
        const resp = await fetch(basePath + path, opts);
        if (resp.status === 401) {
            if (!rhIdentityMode) {
                showLogin();
                throw new Error('unauthorized');
            } else {
                // In rh-identity mode, a 401 indicates an auth configuration problem
                // Check if this is a specific rh-identity error
                try {
                    const errorData = await resp.json();
                    if (errorData.error === 'missing X-RH-Identity header' ||
                        errorData.error === 'invalid X-RH-Identity header' ||
                        errorData.error === 'TBR identity not associated with any user') {

                        // Show appropriate error message
                        let userMessage;
                        if (errorData.error === 'missing X-RH-Identity header') {
                            userMessage = 'Authentication failed: no identity header received. Ensure you are accessing Alcove through the SSO proxy (Turnpike).';
                        } else if (errorData.error === 'invalid X-RH-Identity header') {
                            userMessage = 'Authentication failed: identity header is malformed. Contact your administrator.';
                        } else if (errorData.error === 'TBR identity not associated with any user') {
                            userMessage = 'Authentication failed: your Token Based Registry identity is not associated with an SSO account. Visit the Account page to create an association.';
                        } else {
                            userMessage = 'Authentication failed: ' + errorData.error;
                        }

                        showAuthError(userMessage);
                        throw new Error('rh-identity-auth-error');
                    }
                } catch (parseError) {
                    // If we can't parse the error, fall back to generic handling
                }
                throw new Error('unauthorized');
            }
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

    // Format relative time for last accessed timestamps
    function formatRelativeTime(timestamp) {
        if (!timestamp) {
            return 'Never';
        }

        const now = new Date();
        const then = new Date(timestamp);
        const diffMs = now.getTime() - then.getTime();
        const diffMinutes = Math.floor(diffMs / (1000 * 60));
        const diffHours = Math.floor(diffMinutes / 60);
        const diffDays = Math.floor(diffHours / 24);
        const diffMonths = Math.floor(diffDays / 30);

        if (diffMinutes < 1) {
            return 'just now';
        } else if (diffMinutes < 60) {
            return `${diffMinutes} minute${diffMinutes === 1 ? '' : 's'} ago`;
        } else if (diffHours < 24) {
            return `${diffHours} hour${diffHours === 1 ? '' : 's'} ago`;
        } else if (diffDays < 30) {
            return `${diffDays} day${diffDays === 1 ? '' : 's'} ago`;
        } else {
            return `${diffMonths} month${diffMonths === 1 ? '' : 's'} ago`;
        }
    }

    // ---------------------
    // State
    // ---------------------
    let refreshInterval = null;
    let durationInterval = null;
    let currentSessionId = null;
    let currentPage = 1;
    const perPage = 15;
    let editingScheduleId = null;
    let scheduleFromSession = null;
    let selectedProfiles = [];
    let allProfiles = [];
    let systemLLMConfigured = false;
    let editingProfileId = null;
    let cachedCredentials = [];  // cached from last fetch for prerequisite checks
    let rhIdentityMode = false;
    let proxyLogData = [];
    let proxyLogSortField = 'timestamp';
    let proxyLogSortAsc = true;

    // Teams state
    let teamsList = [];
    let activeTeamId = null;
    let viewingTeamId = null;

    // ---------------------
    // Auth
    // ---------------------
    function showLogin() {
        localStorage.removeItem('alcove_token');
        localStorage.removeItem('alcove_user');
        localStorage.removeItem('alcove_is_admin');
        show($('#login-view'));
        hide($('#dashboard-view'));
        hide($('#auth-error-view'));
        hide($('#alcove-footer'));
        stopRefresh();
        stopSSE();
    }

    function showAuthError(errorMessage) {
        localStorage.removeItem('alcove_token');
        localStorage.removeItem('alcove_user');
        localStorage.removeItem('alcove_is_admin');
        $('#auth-error-message').textContent = errorMessage;
        show($('#auth-error-view'));
        hide($('#dashboard-view'));
        hide($('#login-view'));
        hide($('#alcove-footer'));
        stopRefresh();
        stopSSE();
    }

    function showDashboard() {
        hide($('#login-view'));
        hide($('#auth-error-view'));
        show($('#dashboard-view'));

        // Show footer with version if loaded
        const footer = $('#alcove-footer');
        const versionText = $('#version-text');
        if (footer && versionText && versionText.textContent !== '...') {
            show(footer);
        }

        const user = localStorage.getItem('alcove_user') || 'user';
        $('#user-info').textContent = user;
        // Reset loading states to prevent stale spinners after re-login
        hide($('#sessions-loading'));
        hide($('#unified-schedules-loading'));
        hide($('#credentials-loading'));
        hide($('#tools-loading'));
        hide($('#security-loading'));
        hide($('#transcript-loading'));
        hide($('#proxy-log-loading'));
        // Refresh admin status from server and update UI
        api('GET', '/api/v1/auth/me').then(r => r.json()).then(data => {
            localStorage.setItem('alcove_is_admin', data.is_admin ? 'true' : 'false');
            if (data.auth_backend === 'rh-identity') {
                rhIdentityMode = true;
                localStorage.setItem('alcove_user', data.username);
                $('#user-info').textContent = data.username;
            }
            updateAdminUI();
            updateRHIdentityUI();
        }).catch(() => {});
        updateAdminUI(); // also call immediately with cached value
        updateRHIdentityUI();
        // Load teams for the switcher (only on first load, not on every route change)
        if (teamsList.length === 0) {
            loadTeams().catch(function() {});
        }
        startSystemStateCheck();
    }

    function updateRHIdentityUI() {
        // Hide password change and logout buttons in rh-identity mode
        var changePassBtn = $('#change-password-btn');
        var logoutBtn = $('#logout-btn');
        if (changePassBtn) changePassBtn.hidden = rhIdentityMode;
        if (logoutBtn) logoutBtn.hidden = rhIdentityMode;

        // Account tab is always visible — shows TBR associations for
        // rh-identity mode, personal API tokens for postgres mode.
        var accountTab = $('#account-tab');
        if (accountTab) accountTab.hidden = false;

        // Show RH Identity banner in rh-identity mode (unless dismissed for session)
        var banner = $('#rh-identity-banner');
        var bannerDismissed = sessionStorage.getItem('alcove_rh_banner_dismissed') === 'true';
        if (banner) {
            banner.hidden = !rhIdentityMode || bannerDismissed;
        }
    }

    function isLoggedIn() {
        return rhIdentityMode || !!localStorage.getItem('alcove_token');
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
            const resp = await fetch(basePath + '/api/v1/auth/login', {
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

    // Auth error retry button
    $('#auth-error-retry').addEventListener('click', () => {
        window.location.reload();
    });

    // Clear login error on input focus
    $('#login-username').addEventListener('focus', () => hide($('#login-error')));
    $('#login-password').addEventListener('focus', () => hide($('#login-error')));

    // User dropdown toggle
    $('#user-dropdown-toggle').addEventListener('click', (e) => {
        e.stopPropagation();
        const menu = $('#user-dropdown-menu');
        menu.hidden = !menu.hidden;
        // Close team switcher if open
        hide($('#team-switcher-menu'));
    });

    // Close dropdowns when clicking outside
    document.addEventListener('click', () => {
        hide($('#user-dropdown-menu'));
        hide($('#team-switcher-menu'));
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

    // RH Identity banner dismiss
    $('#rh-identity-banner-dismiss').addEventListener('click', () => {
        sessionStorage.setItem('alcove_rh_banner_dismissed', 'true');
        hide($('#rh-identity-banner'));
    });

    // System Info modal
    $('#system-info-btn').addEventListener('click', function() {
        hide($('#user-dropdown-menu'));
        show($('#system-info-modal'));
        loadSystemInfo();
    });

    $('#system-info-close').addEventListener('click', function() {
        hide($('#system-info-modal'));
    });

    $('#system-info-modal').addEventListener('click', function(e) {
        if (e.target === e.currentTarget) hide(e.currentTarget);
    });

    // ---------------------
    // Webhook Configuration modal
    // ---------------------
    $('#webhook-config-btn').addEventListener('click', function() {
        hide($('#user-dropdown-menu'));
        show($('#webhook-modal'));
        $('#webhook-url').textContent = window.location.origin + basePath + '/api/v1/webhooks/github';
        // Fetch current webhook settings
        api('GET', '/api/v1/admin/settings/webhook').then(function(resp) {
            return resp.json();
        }).then(function(data) {
            if (data.secret_configured) {
                $('#webhook-secret-display').textContent = 'Configured (hidden)';
            } else {
                $('#webhook-secret-display').textContent = 'Not configured';
            }
            if (data.status) {
                $('#webhook-status').innerHTML = '<p class="success-message">' + escapeHtml(data.status) + '</p>';
            } else {
                $('#webhook-status').innerHTML = '';
            }
        }).catch(function() {
            $('#webhook-status').innerHTML = '';
        });
    });

    $('#webhook-generate-secret').addEventListener('click', async function() {
        var btn = $('#webhook-generate-secret');
        btn.disabled = true;
        btn.textContent = 'Generating...';
        try {
            // Generate a random secret
            var array = new Uint8Array(32);
            crypto.getRandomValues(array);
            var secret = Array.from(array, function(b) { return b.toString(16).padStart(2, '0'); }).join('');

            var resp = await api('PUT', '/api/v1/admin/settings/webhook', { secret: secret });
            if (resp.ok) {
                $('#webhook-secret-display').textContent = secret;
            } else {
                var data = await resp.json().catch(function() { return {}; });
                alert(data.error || data.message || 'Failed to save webhook secret.');
            }
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                alert('Failed to generate webhook secret.');
            }
        }
        btn.disabled = false;
        btn.textContent = 'Generate Secret';
    });

    $('#webhook-close').addEventListener('click', function() {
        hide($('#webhook-modal'));
    });

    $('#webhook-modal').addEventListener('click', function(e) {
        if (e.target === e.currentTarget) hide(e.currentTarget);
    });

    // ---------------------
    // Agent Repos (inline on Repos page)
    // ---------------------
    var taskReposList = [];

    async function loadTaskRepos() {
        var listEl = $('#task-repos-inline-list');
        if (!listEl) return;
        listEl.innerHTML = '<div class="loading-state"><div class="spinner"></div><p>Loading...</p></div>';
        try {
            var resp = await api('GET', '/api/v1/user/settings/agent-repos');
            if (!resp.ok) {
                listEl.innerHTML = '<p class="error-message">Failed to load agent repos.</p>';
                return;
            }
            var data = await resp.json();
            taskReposList = data.repos || [];
            renderTaskRepos();
        } catch (err) {
            listEl.innerHTML = '<p class="error-message">Failed to load agent repos.</p>';
        }
    }

    function renderTaskRepos() {
        var listEl = $('#task-repos-inline-list');
        if (!listEl) return;
        if (taskReposList.length === 0) {
            listEl.innerHTML = '<p style="color:var(--text-muted);font-size:13px;">No agent repos configured.</p>';
            return;
        }
        var html = '';
        for (var i = 0; i < taskReposList.length; i++) {
            var r = taskReposList[i];
            var isEnabled = r.enabled === undefined || r.enabled === null || r.enabled === true;
            var displayUrl = (r.url || '').replace(/^https?:\/\//, '').replace(/\.git$/, '');
            var toggleTitle = isEnabled ? 'Pause sessions from this repo' : 'Resume sessions from this repo';
            html += '<div class="repo-item ' + (isEnabled ? 'repo-active' : 'repo-paused') + '">';
            html += '<label class="toggle-switch" title="' + toggleTitle + '"><input type="checkbox" class="repo-item-enabled" data-index="' + i + '"' + (isEnabled ? ' checked' : '') + '><span class="toggle-slider"></span></label>';
            html += '<span class="' + (isEnabled ? 'toggle-label-active' : 'toggle-label-paused') + '">' + (isEnabled ? 'Active' : 'Paused') + '</span>';
            html += '<span class="repo-item-url">' + escapeHtml(displayUrl) + '</span>';
            if (r.ref && r.ref !== 'main') html += ' <span class="repo-item-ref">' + escapeHtml(r.ref) + '</span>';
            html += ' <button class="btn btn-small btn-outline repo-item-remove" data-index="' + i + '" style="color:var(--status-error);border-color:var(--status-error);padding:2px 8px;font-size:11px;">Remove</button>';
            if (!isEnabled) html += '<div class="repo-paused-message">Sessions from this repo are paused. Schedules and event-driven sessions will not run.</div>';
            html += '</div>';
        }
        listEl.innerHTML = html;

        listEl.querySelectorAll('.repo-item-enabled').forEach(function(cb) {
            cb.addEventListener('change', async function() {
                var idx = parseInt(cb.getAttribute('data-index'), 10);
                taskReposList[idx].enabled = cb.checked;
                await saveTaskRepos();
                var statusEl = $('#task-repo-add-status-inline');
                if (statusEl) {
                    statusEl.removeAttribute('hidden');
                    statusEl.style.color = 'var(--text-muted)';
                    statusEl.textContent = (cb.checked ? 'Resumed' : 'Paused') + ' ' + (taskReposList[idx].name || taskReposList[idx].url) + '. Changes take effect on next sync.';
                }
                setTimeout(function() { loadUnifiedSchedules(); }, 2000);
            });
        });

        listEl.querySelectorAll('.repo-item-remove').forEach(function(btn) {
            btn.addEventListener('click', async function() {
                var idx = parseInt(btn.getAttribute('data-index'), 10);
                var removed = taskReposList[idx];
                taskReposList.splice(idx, 1);
                await saveTaskRepos();
                var statusEl = $('#task-repo-add-status-inline');
                if (statusEl) {
                    statusEl.removeAttribute('hidden');
                    statusEl.style.color = 'var(--text-muted)';
                    statusEl.textContent = 'Removed ' + (removed.name || removed.url) + '. Agent definitions from this repo have been deleted.';
                }
                // Reload agent definitions after sync cleans up (2s delay for background sync)
                setTimeout(function() { loadUnifiedSchedules(); }, 2000);
            });
        });
    }

    async function saveTaskRepos() {
        try {
            var resp = await api('PUT', '/api/v1/user/settings/agent-repos', { repos: taskReposList });
            if (!resp.ok) {
                alert('Failed to save agent repos.');
            }
            renderTaskRepos();
        } catch (err) {
            alert('Failed to save agent repos.');
        }
    }

    $('#task-repo-add-inline').addEventListener('click', async function() {
        var url = $('#task-repo-url-inline').value.trim();
        if (!url) return;
        var ref = $('#task-repo-ref-inline').value.trim() || 'main';
        var name = '';
        var parts = url.replace(/\.git$/, '').split('/');
        name = parts[parts.length - 1] || 'repo';

        var btn = $('#task-repo-add-inline');
        var statusEl = $('#task-repo-add-status-inline');
        btn.disabled = true;
        btn.textContent = 'Validating...';
        statusEl.removeAttribute('hidden');
        statusEl.style.color = 'var(--text-muted)';
        statusEl.textContent = 'Cloning and validating repository...';

        try {
            var resp = await api('POST', '/api/v1/agent-repos/validate', { url: url, ref: ref, name: name });
            var data = await resp.json();
            if (!data.valid) {
                statusEl.style.color = 'var(--status-error)';
                statusEl.textContent = 'Validation failed: ' + (data.error || 'unknown error');
                btn.disabled = false;
                btn.textContent = 'Add';
                return;
            }
            taskReposList.push({ url: url, ref: ref, name: name });
            $('#task-repo-url-inline').value = '';
            $('#task-repo-ref-inline').value = '';
            statusEl.style.color = 'var(--status-running)';
            statusEl.textContent = 'Found ' + data.agent_definition_count + ' agent definition(s): ' + data.agent_definitions.join(', ');
            await saveTaskRepos();
            // Reload agent definitions after sync completes (auto-triggered by save)
            setTimeout(function() { loadUnifiedSchedules(); }, 2000);
        } catch (err) {
            statusEl.style.color = 'var(--status-error)';
            statusEl.textContent = 'Validation error: ' + err.message;
        } finally {
            btn.disabled = false;
            btn.textContent = 'Add';
        }
    });

    // ---------------------
    // Unified Schedules (agent definitions + manual schedules)
    // ---------------------
    function formatRelativeTime(dateStr) {
        if (!dateStr) return '';
        var d = new Date(dateStr);
        if (isNaN(d.getTime())) return '';
        var now = new Date();
        var diffMs = d - now;
        var absDiff = Math.abs(diffMs);
        var isPast = diffMs < 0;

        if (absDiff < 60000) return isPast ? 'just now' : 'in a moment';
        if (absDiff < 3600000) {
            var mins = Math.round(absDiff / 60000);
            return isPast ? mins + 'm ago' : 'in ' + mins + 'm';
        }
        if (absDiff < 86400000) {
            var hrs = Math.round(absDiff / 3600000);
            return isPast ? hrs + 'h ago' : 'in ' + hrs + 'h';
        }
        var days = Math.round(absDiff / 86400000);
        return isPast ? days + 'd ago' : 'in ' + days + 'd';
    }

    async function loadUnifiedSchedules() {
        var listEl = $('#unified-schedules-list');
        var emptyEl = $('#unified-schedules-empty');
        var loadingEl = $('#unified-schedules-loading');

        listEl.innerHTML = '';
        hide(emptyEl);
        show(loadingEl);

        var allItems = [];

        try {
            var results = await Promise.allSettled([
                api('GET', '/api/v1/agent-definitions'),
                api('GET', '/api/v1/schedules')
            ]);

            // Process agent definitions
            if (results[0].status === 'fulfilled' && results[0].value.ok) {
                var data = await results[0].value.json();
                var defs = Array.isArray(data) ? data : (data.agent_definitions || data.task_definitions || data.definitions || data.items || []);
                defs.forEach(function(d) {
                    allItems.push({ _type: 'task-def', _name: (d.name || 'Unnamed').toLowerCase(), data: d });
                });
            }

            // Process schedules
            if (results[1].status === 'fulfilled' && results[1].value.ok) {
                var data2 = await results[1].value.json();
                var schedules = Array.isArray(data2) ? data2 : (data2.schedules || data2.items || []);
                schedules.forEach(function(s) {
                    // Skip YAML-sourced schedules — they're already shown as agent definition cards.
                    if (s.source === 'yaml') return;
                    allItems.push({ _type: 'schedule', _name: (s.name || '').toLowerCase(), data: s });
                });
            }
        } catch (err) {
            if (err.message === 'unauthorized') {
                hide(loadingEl);
                return;
            }
        }

        hide(loadingEl);

        if (allItems.length === 0) {
            show(emptyEl);
            return;
        }

        // Sort: paused-by-repo items to bottom, then alphabetically by name
        allItems.sort(function(a, b) {
            var aPaused = (a._type === 'task-def' && (a.data.repo_disabled || false)) ? 1 : 0;
            var bPaused = (b._type === 'task-def' && (b.data.repo_disabled || false)) ? 1 : 0;
            if (aPaused !== bPaused) return aPaused - bPaused;
            if (a._name < b._name) return -1;
            if (a._name > b._name) return 1;
            return 0;
        });

        // Update Schedules nav tab with paused count
        var pausedCount = 0;
        allItems.forEach(function(item) {
            if (item._type === 'task-def' && (item.data.repo_disabled || false)) {
                pausedCount++;
            }
        });
        var schedulesTab = document.querySelector('.nav-tab[data-tab="schedules"]');
        if (schedulesTab) {
            schedulesTab.textContent = pausedCount > 0 ? 'Schedules (' + pausedCount + ' paused)' : 'Schedules';
        }

        var html = '';
        for (var i = 0; i < allItems.length; i++) {
            var item = allItems[i];
            if (item._type === 'task-def') {
                html += renderTaskDefCard(item.data);
            } else {
                html += renderScheduleCard(item.data);
            }
        }
        listEl.innerHTML = html;

        // Attach event handlers for agent definition cards
        listEl.querySelectorAll('.agent-def-run').forEach(function(btn) {
            btn.addEventListener('click', async function() {
                var defId = btn.getAttribute('data-id');
                btn.disabled = true;
                btn.textContent = 'Running...';
                try {
                    var resp = await api('POST', '/api/v1/agent-definitions/' + defId + '/run');
                    if (!resp.ok) {
                        var data = await resp.json().catch(function() { return {}; });
                        alert(data.error || data.message || 'Failed to start session.');
                    } else {
                        var session = await resp.json();
                        var sessionId = session.id || session.session_id || '';
                        // Replace button with a "View Session" link
                        var link = document.createElement('a');
                        link.href = '#session/' + sessionId;
                        link.className = 'btn btn-small btn-outline';
                        link.textContent = 'View Session';
                        link.addEventListener('click', function(e) {
                            e.preventDefault();
                            navigate('session/' + sessionId);
                        });
                        btn.replaceWith(link);
                        return;
                    }
                } catch (err) {
                    if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                        alert('Failed to start session.');
                    }
                }
                btn.textContent = 'Run Now';
                btn.disabled = false;
            });
        });

        listEl.querySelectorAll('.agent-def-yaml').forEach(function(btn) {
            btn.addEventListener('click', function() {
                var repo = btn.getAttribute('data-repo') || '';
                var file = btn.getAttribute('data-file') || '';
                var webUrl = repo.replace(/\.git$/, '');
                if (webUrl && file) {
                    window.open(webUrl + '/blob/main/.alcove/tasks/' + file, '_blank');
                } else {
                    showYaml(btn.closest('.agent-def-card').querySelector('.agent-def-run').getAttribute('data-id'));
                }
            });
        });

        // Attach event handlers for schedule cards
        listEl.querySelectorAll('.edit-schedule-btn').forEach(function(btn) {
            btn.addEventListener('click', async function() {
                var id = btn.dataset.id;
                try {
                    var resp = await api('GET', '/api/v1/schedules/' + id);
                    var s = await resp.json();
                    editingScheduleId = id;
                    $('#sched-name').value = s.name || '';
                    $('#sched-cron').value = s.cron || s.cron_expression || '';
                    $('#sched-prompt').value = s.prompt || '';
                    $('#sched-provider').value = s.provider || '';
                    $('#sched-repo').value = s.repo || '';
                    var timeout = s.timeout ? Math.round(s.timeout / 60) : 60;
                    $('#sched-timeout').value = timeout;
                    $('#sched-timeout-value').textContent = timeout;
                    $('#sched-debug').checked = s.debug || false;
                    $('#sched-enabled').checked = s.enabled !== false;
                    var tt = s.trigger_type || 'cron';
                    $('#sched-trigger-type').value = tt;
                    $('#sched-trigger-type').dispatchEvent(new Event('change'));
                    document.querySelectorAll('.event-checkbox').forEach(function(cb) { cb.checked = false; });
                    if (s.event_config && s.event_config.events) {
                        s.event_config.events.forEach(function(evt) {
                            var cb = document.querySelector('.event-checkbox[value="' + evt + '"]');
                            if (cb) cb.checked = true;
                        });
                    }
                    $('#sched-event-repos').value = (s.event_config && s.event_config.repos) ? s.event_config.repos.join(', ') : '';
                    $('#sched-event-branches').value = (s.event_config && s.event_config.branches) ? s.event_config.branches.join(', ') : '';
                    var dm = s.event_config && s.event_config.delivery_mode;
                    document.querySelectorAll('input[name="sched-delivery-mode"]').forEach(function(r) {
                        r.checked = (r.value === (dm || 'polling'));
                    });
                    $('#delivery-polling-info').hidden = (dm === 'webhook');
                    $('#delivery-webhook-info').hidden = (dm !== 'webhook');
                    if (dm === 'webhook') {
                        $('#schedule-webhook-url').textContent = window.location.origin + basePath + '/api/v1/webhooks/github';
                    }
                    $('#schedule-submit-btn').textContent = 'Update Schedule';
                    show($('#schedule-form-container'));
                    $('#sched-name').focus();
                } catch (err) {
                    if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                        alert('Failed to load schedule.');
                    }
                }
            });
        });

        listEl.querySelectorAll('.delete-schedule-btn').forEach(function(btn) {
            btn.addEventListener('click', async function() {
                var id = btn.dataset.id;
                if (!confirm('Are you sure you want to delete this schedule?')) return;
                btn.disabled = true;
                try {
                    var resp = await api('DELETE', '/api/v1/schedules/' + id);
                    if (!resp.ok) {
                        var data = await resp.json().catch(function() { return {}; });
                        alert(data.error || data.message || 'Failed to delete schedule.');
                        btn.disabled = false;
                    } else {
                        loadUnifiedSchedules();
                    }
                } catch (err) {
                    if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                        alert('Failed to delete schedule.');
                    }
                    btn.disabled = false;
                }
            });
        });

        listEl.querySelectorAll('.view-schedule-yaml-btn').forEach(function(btn) {
            btn.addEventListener('click', function() {
                var id = btn.dataset.id;
                showYaml(id);
            });
        });
    }

    function renderTaskDefCard(d) {
        var name = d.name || 'Unnamed';
        var desc = d.description || '';
        var repo = d.source_repo || d.repo || '';
        var id = d.id || '';
        var repoPaused = d.repo_disabled || false;

        var html = '<div class="agent-def-card' + (repoPaused ? ' agent-def-paused-repo' : '') + '">';

        // Paused-by-repo badge
        if (repoPaused) {
            html += '<div style="margin-bottom:8px;"><span class="badge-paused-repo">Paused — repo disabled</span></div>';
        }

        // Header row: name + actions
        html += '<div class="agent-def-header">';
        html += '<div class="agent-def-name">' + escapeHtml(name) + '</div>';
        html += '<div class="agent-def-actions">';
        if (d.sync_error || repoPaused) {
            var disabledTitle = repoPaused ? 'Repo is paused' : escapeHtml(d.sync_error);
            html += '<button class="btn btn-small btn-primary agent-def-run" data-id="' + escapeHtml(id) + '" disabled title="' + disabledTitle + '" style="' + (repoPaused ? 'opacity:0.4;cursor:not-allowed;' : '') + '">Run Now</button>';
        } else {
            html += '<button class="btn btn-small btn-primary agent-def-run" data-id="' + escapeHtml(id) + '">Run Now</button>';
        }
        html += '<button class="btn btn-small btn-outline agent-def-yaml" data-repo="' + escapeHtml(d.source_repo || '') + '" data-file="' + escapeHtml(d.source_file || '') + '">View YAML</button>';
        html += '</div>';
        html += '</div>';

        // Description
        if (desc) {
            html += '<div class="agent-def-desc">' + escapeHtml(desc) + '</div>';
        }

        // Tags row: yaml tag, profiles, repo
        var tags = [];
        tags.push('<span class="agent-def-tag agent-def-tag-yaml">yaml</span>');
        if (d.profiles && d.profiles.length > 0) {
            d.profiles.forEach(function(p) {
                tags.push('<span class="agent-def-tag agent-def-tag-profile">' + escapeHtml(p) + '</span>');
            });
        }
        if (repo) {
            var shortRepo = repo.replace(/^https?:\/\//, '').replace(/\.git$/, '');
            tags.push('<span class="agent-def-tag agent-def-tag-repo">' + escapeHtml(shortRepo) + '</span>');
        }
        html += '<div class="agent-def-tags">' + tags.join('') + '</div>';

        // Schedule and trigger details
        var details = [];
        if (d.schedule && d.schedule.cron) {
            var schedParts = ['<code>' + escapeHtml(d.schedule.cron) + '</code>'];
            if (!d.schedule.enabled) {
                schedParts.push('<span class="agent-def-dim">disabled</span>');
            } else if (d.next_run) {
                schedParts.push('next ' + formatRelativeTime(d.next_run));
            }
            if (d.last_run) {
                schedParts.push('last ran ' + formatRelativeTime(d.last_run));
            }
            details.push('<span class="agent-def-detail-item"><span class="agent-def-label">Schedule</span> ' + schedParts.join(' &middot; ') + '</span>');
        }
        if (d.trigger && d.trigger.github) {
            var gh = d.trigger.github;
            var evts = (gh.events || []).join(', ');
            var acts = (gh.actions || []).length > 0 ? ' (' + gh.actions.join(', ') + ')' : '';
            details.push('<span class="agent-def-detail-item"><span class="agent-def-label">Trigger</span> ' + escapeHtml(evts + acts) + '</span>');
        }
        if (details.length > 0) {
            html += '<div class="agent-def-details">' + details.join('') + '</div>';
        }

        // Sync error
        if (d.sync_error) {
            html += '<div class="agent-def-error">Sync error: ' + escapeHtml(d.sync_error) + '</div>';
        }

        // Paused-by-repo link
        if (repoPaused) {
            html += '<div style="margin-top:8px;"><a href="#repos" class="agent-def-paused-link">Go to Repos to re-enable</a></div>';
        }

        html += '</div>';
        return html;
    }

    function renderScheduleCard(s) {
        var name = s.name || 'Unnamed';
        var id = s.id || '';
        var source = s.source || 'manual';
        var isYaml = source === 'yaml';
        var cron = s.cron || s.cron_expression || '';
        var enabled = s.enabled !== false;
        var triggerType = s.trigger_type || 'cron';

        var html = '<div class="agent-def-card">';

        // Header row: name + actions
        html += '<div class="agent-def-header">';
        html += '<div class="agent-def-name">' + escapeHtml(name) + '</div>';
        html += '<div class="agent-def-actions">';
        if (isYaml) {
            html += '<button class="btn btn-small btn-outline view-schedule-yaml-btn" data-id="' + escapeHtml(id) + '">View</button>';
        } else {
            html += '<button class="btn btn-small btn-outline edit-schedule-btn" data-id="' + escapeHtml(id) + '">Edit</button>';
            html += '<button class="btn btn-small btn-outline delete-schedule-btn" data-id="' + escapeHtml(id) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button>';
        }
        html += '</div>';
        html += '</div>';

        // Tags row: source tag, repo
        var tags = [];
        if (isYaml) {
            tags.push('<span class="agent-def-tag agent-def-tag-yaml">yaml</span>');
        } else {
            tags.push('<span class="agent-def-tag agent-def-tag-manual">manual</span>');
        }
        if (s.repo) {
            var shortRepo = s.repo.replace(/^https?:\/\//, '').replace(/\.git$/, '');
            tags.push('<span class="agent-def-tag agent-def-tag-repo">' + escapeHtml(shortRepo) + '</span>');
        }
        html += '<div class="agent-def-tags">' + tags.join('') + '</div>';

        // Details: cron, trigger type, next run, last run, enabled/disabled
        var details = [];
        if (cron) {
            var cronDesc = describeCron(cron);
            details.push('<span class="agent-def-detail-item"><span class="agent-def-label">Cron</span> <code>' + escapeHtml(cron) + '</code> <small>' + escapeHtml(cronDesc) + '</small></span>');
        }

        // Trigger type
        if (triggerType === 'event') {
            var triggerLabel = 'event';
            if (s.event_config && s.event_config.events && s.event_config.events.length > 0) {
                triggerLabel += ' (' + s.event_config.events.join(', ') + ')';
            }
            details.push('<span class="agent-def-detail-item"><span class="agent-def-label">Trigger</span> ' + escapeHtml(triggerLabel) + '</span>');
        } else if (triggerType === 'cron-and-event') {
            var triggerLabel2 = 'cron + event';
            if (s.event_config && s.event_config.events && s.event_config.events.length > 0) {
                triggerLabel2 += ' (' + s.event_config.events.join(', ') + ')';
            }
            details.push('<span class="agent-def-detail-item"><span class="agent-def-label">Trigger</span> ' + escapeHtml(triggerLabel2) + '</span>');
        }

        var nextRun = s.next_run || s.next_run_at;
        var lastRun = s.last_run || s.last_run_at;
        if (nextRun) {
            details.push('<span class="agent-def-detail-item"><span class="agent-def-label">Next</span> ' + formatRelativeTime(nextRun) + '</span>');
        }
        if (lastRun) {
            details.push('<span class="agent-def-detail-item"><span class="agent-def-label">Last</span> ' + formatRelativeTime(lastRun) + '</span>');
        }

        // Enabled/disabled
        if (!enabled) {
            details.push('<span class="agent-def-detail-item"><span class="agent-def-dim">disabled</span></span>');
        }

        if (details.length > 0) {
            html += '<div class="agent-def-details">' + details.join('') + '</div>';
        }

        html += '</div>';
        return html;
    }

    // Sync agent definitions
    $('#sync-task-defs').addEventListener('click', async function() {
        var btn = $('#sync-task-defs');
        btn.disabled = true;
        btn.textContent = 'Syncing...';
        try {
            var resp = await api('POST', '/api/v1/agent-definitions/sync');
            if (!resp.ok) {
                var data = await resp.json().catch(function() { return {}; });
                alert(data.error || data.message || 'Failed to sync agent definitions.');
            }
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                alert('Failed to sync agent definitions.');
            }
        }
        btn.textContent = 'Sync Now';
        btn.disabled = false;
        loadUnifiedSchedules();
    });

    // ---------------------
    // View YAML modal
    // ---------------------
    var currentYamlContent = '';

    async function showYaml(id) {
        var modal = $('#view-yaml-modal');
        var titleEl = $('#view-yaml-title');
        var contentEl = $('#view-yaml-content');

        titleEl.textContent = 'Agent Definition';
        contentEl.textContent = 'Loading...';
        show(modal);

        try {
            var resp = await api('GET', '/api/v1/agent-definitions/' + id);
            if (!resp.ok) {
                contentEl.textContent = 'Failed to load agent definition.';
                return;
            }
            var data = await resp.json();
            currentYamlContent = data.raw_yaml || data.yaml || '';
            titleEl.textContent = data.name || 'Agent Definition';
            contentEl.textContent = currentYamlContent || '(no YAML content)';
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                contentEl.textContent = 'Failed to load agent definition.';
            }
        }
    }

    $('#view-yaml-copy').addEventListener('click', function() {
        if (currentYamlContent) {
            navigator.clipboard.writeText(currentYamlContent).then(function() {
                var btn = $('#view-yaml-copy');
                btn.textContent = 'Copied!';
                setTimeout(function() { btn.textContent = 'Copy YAML'; }, 1500);
            });
        }
    });

    $('#view-yaml-close').addEventListener('click', function() {
        hide($('#view-yaml-modal'));
    });

    $('#view-yaml-modal').addEventListener('click', function(e) {
        if (e.target === e.currentTarget) hide(e.currentTarget);
    });

    // ---------------------
    // Templates modal
    // ---------------------
    $('#browse-templates').addEventListener('click', function() {
        show($('#templates-modal'));
        loadTemplates();
    });

    async function loadTemplates() {
        var listEl = $('#templates-list');
        listEl.innerHTML = '<div class="loading-state"><div class="spinner"></div><p>Loading templates...</p></div>';

        try {
            var resp = await api('GET', '/api/v1/agent-templates');
            if (!resp.ok) {
                listEl.innerHTML = '<p class="error-message">Failed to load templates.</p>';
                return;
            }
            var data = await resp.json();
            var templates = Array.isArray(data) ? data : (data.templates || data.items || []);
            renderTemplates(templates);
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                listEl.innerHTML = '<p class="error-message">Failed to load templates.</p>';
            }
        }
    }

    function renderTemplates(templates) {
        var listEl = $('#templates-list');
        if (templates.length === 0) {
            listEl.innerHTML = '<p style="color:var(--text-muted);padding:12px 0;">No templates available.</p>';
            return;
        }

        var html = '';
        for (var i = 0; i < templates.length; i++) {
            var t = templates[i];
            html += '<div class="template-card">';
            html += '<div class="template-card-header">';
            html += '<span class="template-card-name">' + escapeHtml(t.name || 'Unnamed') + '</span>';
            html += '<button class="btn btn-small btn-outline template-copy" data-index="' + i + '">Copy YAML</button>';
            html += '</div>';
            if (t.description) {
                html += '<div class="template-card-desc">' + escapeHtml(t.description) + '</div>';
            }
            html += '</div>';
        }
        listEl.innerHTML = html;

        listEl.querySelectorAll('.template-copy').forEach(function(btn) {
            btn.addEventListener('click', function() {
                var idx = parseInt(btn.getAttribute('data-index'), 10);
                var yaml = templates[idx].raw_yaml || templates[idx].yaml || '';
                if (yaml) {
                    navigator.clipboard.writeText(yaml).then(function() {
                        btn.textContent = 'Copied!';
                        setTimeout(function() { btn.textContent = 'Copy YAML'; }, 1500);
                    });
                }
            });
        });
    }

    $('#templates-close').addEventListener('click', function() {
        hide($('#templates-modal'));
    });

    $('#templates-modal').addEventListener('click', function(e) {
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
        const pages = ['sessions', 'task-new', 'schedules', 'repos', 'catalog', 'credentials', 'security', 'tools', 'session-detail', 'users', 'account', 'workflows', 'workflow-detail', 'teams', 'team-detail'];
        pages.forEach((p) => hide($('#page-' + p)));

        // Update active nav tab
        var navRoute = route.startsWith('session/') ? 'sessions' : route;
        if (navRoute === 'tools' || navRoute === 'tools-admin') navRoute = 'security';
        if (route.startsWith('workflow-run/')) navRoute = 'workflows';
        if (route.startsWith('team/')) navRoute = 'teams';
        $$('.nav-tab').forEach((tab) => {
            tab.classList.toggle('active', tab.dataset.tab === navRoute);
        });

        if (route === 'sessions') {
            show($('#page-sessions'));
            loadSessions();
        } else if (route === 'task/new') {
            show($('#page-task-new'));
            hide($('#task-error'));
            hide($('#task-success'));
            hide($('#task-warnings'));
            loadProviders();
            loadTaskProfiles();
        } else if (route === 'schedules') {
            show($('#page-schedules'));
            loadUnifiedSchedules();
            loadScheduleProviders();
            if (scheduleFromSession) {
                openScheduleForm(scheduleFromSession);
                scheduleFromSession = null;
            }
        } else if (route === 'repos') {
            show($('#page-repos'));
            loadTaskRepos();
        } else if (route === 'catalog') {
            show($('#page-catalog'));
            loadCatalogPage();
        } else if (route === 'credentials') {
            show($('#page-credentials'));
            loadCredentials();
        } else if (route === 'security') {
            show($('#page-security'));
            loadSecurityPage();
        } else if (route === 'tools' || route === 'tools-admin') {
            show($('#page-tools'));
            loadToolsPage();
        } else if (route.startsWith('session/')) {
            const id = route.replace('session/', '');
            show($('#page-session-detail'));
            loadSessionDetail(id);
        } else if (route === 'workflows') {
            show($('#page-workflows'));
            loadWorkflowsPage();
        } else if (route.startsWith('workflow-run/')) {
            var wfRunId = route.replace('workflow-run/', '');
            show($('#page-workflow-detail'));
            loadWorkflowRunDetail(wfRunId);
        } else if (route === 'users') {
            if (!isAdmin()) { navigate('sessions'); return; }
            show($('#page-users'));
            loadUsers();
        } else if (route === 'account') {
            show($('#page-account'));
            loadAccountPage();
        } else if (route === 'teams') {
            show($('#page-teams'));
            loadTeamsPage();
        } else if (route.startsWith('team/')) {
            var teamId = route.replace('team/', '');
            show($('#page-team-detail'));
            loadTeamDetail(teamId);
        } else {
            show($('#page-sessions'));
            loadSessions();
        }
    }

    window.addEventListener('hashchange', handleRoute);

    // ---------------------
    // Sessions list
    // ---------------------

    function getTaskType(taskName) {
        if (!taskName) return { label: 'SESSION', color: 'neutral' };
        var name = taskName.toLowerCase();
        if (name.includes('review')) return { label: 'REVIEW', color: 'review' };
        if (name.includes('plan')) return { label: 'PLAN', color: 'plan' };
        if (name.includes('release')) return { label: 'RELEASE', color: 'release' };
        if (name.includes('developer') || name.includes('dev')) return { label: 'DEV', color: 'dev' };
        if (name.includes('retry') || name.includes('ci')) return { label: 'CI FIX', color: 'dev' };
        var firstWord = taskName.split(/[\s-]/)[0].toUpperCase();
        return { label: firstWord.substring(0, 8), color: 'neutral' };
    }

    function formatTriggerRef(triggerContext) {
        if (!triggerContext || triggerContext === 'Manual') return '<span class="text-muted">Manual</span>';
        if (triggerContext === 'Scheduled' || triggerContext === 'cron') return '<span class="text-muted">Scheduled</span>';

        // Strip any prefix before the repo reference (e.g., "event: " or "webhook: ")
        var cleanRef = triggerContext;
        var colonIndex = triggerContext.indexOf(': ');
        if (colonIndex !== -1) {
            cleanRef = triggerContext.substring(colonIndex + 2);
        }

        // Parse "owner/repo#number" format
        var match = cleanRef.match(/^(.+?)#(\d+)$/);
        if (match) {
            var repo = match[1];
            var number = match[2];
            var url = 'https://github.com/' + repo + '/issues/' + number;
            var shortRef = '#' + number;
            return '<a href="' + escapeHtml(url) + '" class="trigger-link" target="_blank" rel="noopener" onclick="event.stopPropagation()">' + escapeHtml(shortRef) + '</a>';
        }
        return '<span class="text-muted">' + escapeHtml(triggerContext) + '</span>';
    }

    function formatSubmitter(submitter) {
        if (!submitter) return '-';
        // Strip @domain from email addresses
        var atIdx = submitter.indexOf('@');
        if (atIdx > 0) return submitter.substring(0, atIdx);
        return submitter;
    }

    function startRunningTimers() {
        if (window._runningTimer) clearInterval(window._runningTimer);
        window._runningTimer = setInterval(function () {
            document.querySelectorAll('[data-started-at]').forEach(function (el) {
                var started = new Date(el.dataset.startedAt);
                var elapsed = Math.floor((Date.now() - started) / 1000);
                el.textContent = humanDuration(elapsed);
            });
        }, 1000);
    }

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
            const statusFilter = $('#filter-status').value;
            let runningSessions = [];
            let paginated = {};

            // Always fetch running sessions separately to pin them at the top
            if (!statusFilter || statusFilter === 'running') {
                const runningResp = await api('GET', '/api/v1/sessions?status=running&per_page=100');
                const runningData = await runningResp.json();
                runningSessions = Array.isArray(runningData) ? runningData : (runningData.sessions || runningData.items || []);
            }

            // Fetch paginated sessions based on filter
            let paginatedUrl = '/api/v1/sessions?page=' + currentPage + '&per_page=' + perPage;
            if (statusFilter) {
                paginatedUrl += '&status=' + encodeURIComponent(statusFilter);
            } else {
                // When not filtering, exclude running sessions from pagination since we show them separately
                paginatedUrl += '&status=completed,error,cancelled,timeout';
            }

            const paginatedResp = await api('GET', paginatedUrl);
            paginated = await paginatedResp.json();
            hide(loading);

            const paginatedSessions = Array.isArray(paginated) ? paginated : (paginated.sessions || paginated.items || []);

            // If we're filtering for non-running statuses, don't show the separate running section
            if (statusFilter && statusFilter !== 'running') {
                runningSessions = [];
            }

            // Check if we have any sessions at all
            if (runningSessions.length === 0 && paginatedSessions.length === 0) {
                renderEmptyState();
                return;
            }

            renderSessionsWithPinnedRunning(runningSessions, paginatedSessions);
            renderPagination(paginated.page, paginated.pages, paginated.total);
            startAutoRefresh();
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:var(--status-error);padding:24px;">Failed to load sessions. Check your connection and try again.</td></tr>';
            }
        }
    }

    function renderEmptyState() {
        var tbody = $('#sessions-tbody');
        var empty = $('#sessions-empty');
        var table = $('#sessions-table');
        tbody.innerHTML = '';
        if (table) table.hidden = true;
        empty.innerHTML = '<div class="empty-state-redesign">' +
            '<h2>No sessions yet</h2>' +
            '<p>Sessions appear here when they run -- triggered by events, on a schedule, or started manually.</p>' +
            '<div class="empty-state-cards">' +
                '<a href="#task/new" class="empty-card">' +
                    '<div class="empty-card-icon">&gt;_</div>' +
                    '<div class="empty-card-title">Start a session</div>' +
                    '<div class="empty-card-desc">Submit a prompt to an AI agent</div>' +
                '</a>' +
                '<a href="#repos" class="empty-card">' +
                    '<div class="empty-card-icon">&#9201;</div>' +
                    '<div class="empty-card-title">Set up automation</div>' +
                    '<div class="empty-card-desc">Connect an agent repo for event-driven agents</div>' +
                '</a>' +
                '<a href="#credentials" class="empty-card">' +
                    '<div class="empty-card-icon">&#128273;</div>' +
                    '<div class="empty-card-title">Add credentials</div>' +
                    '<div class="empty-card-desc">Configure LLM and platform credentials</div>' +
                '</a>' +
            '</div>' +
        '</div>';
        show(empty);
    }

    function renderSessionRow(s) {
        var status = s.status || 'unknown';
        var taskName = s.task_name || 'Manual Session';
        var taskType = getTaskType(s.task_name);
        var submitter = formatSubmitter(s.submitter);
        var when = formatRelativeTime(s.started_at);
        var triggerHtml = formatTriggerRef(s.trigger_context);
        var isRunning = status === 'running';

        // Duration: for running tasks, show live counter
        var durationHtml;
        if (isRunning && s.started_at) {
            var elapsed = Math.floor((Date.now() - new Date(s.started_at).getTime()) / 1000);
            durationHtml = '<span class="duration-live" data-started-at="' + escapeHtml(s.started_at) + '">' + humanDuration(elapsed) + '</span>';
        } else {
            durationHtml = escapeHtml(formatDuration(s.started_at, s.finished_at, s.duration));
        }

        return '<tr class="clickable session-row session-row-' + escapeHtml(status) + '" data-session-id="' + escapeHtml(s.id) + '" tabindex="0" role="link">' +
            '<td><span class="status-dot status-dot-' + escapeHtml(status) + '" title="' + escapeHtml(status) + '"></span></td>' +
            '<td><span class="agent-type-pill agent-type-' + escapeHtml(taskType.color) + '">' + escapeHtml(taskType.label) + '</span>' + escapeHtml(taskName) + '</td>' +
            '<td>' + escapeHtml(submitter) + '</td>' +
            '<td>' + escapeHtml(when) + '</td>' +
            '<td class="mono">' + durationHtml + '</td>' +
            '<td>' + triggerHtml + '</td>' +
            '</tr>';
    }

    function renderSessionsWithPinnedRunning(runningSessions, paginatedSessions) {
        var tbody = $('#sessions-tbody');
        var table = $('#sessions-table');
        var searchFilter = $('#filter-search').value.toLowerCase();

        if (table) table.hidden = false;

        // Apply search filter to both running and paginated sessions
        var filteredRunning = runningSessions.filter(function (s) {
            if (searchFilter) {
                var text = (s.id + ' ' + (s.task_name || '') + ' ' + (s.trigger_context || '') + ' ' + (s.prompt || '')).toLowerCase();
                if (!text.includes(searchFilter)) return false;
            }
            return true;
        });

        var filteredPaginated = paginatedSessions.filter(function (s) {
            if (searchFilter) {
                var text = (s.id + ' ' + (s.task_name || '') + ' ' + (s.trigger_context || '') + ' ' + (s.prompt || '')).toLowerCase();
                if (!text.includes(searchFilter)) return false;
            }
            return true;
        });

        var empty = $('#sessions-empty');
        if (filteredRunning.length === 0 && filteredPaginated.length === 0) {
            tbody.innerHTML = '';
            empty.innerHTML = '<p>No sessions match your filters.</p>';
            show(empty);
            return;
        }
        hide(empty);

        var html = '';

        // Pinned running section - always visible when there are running sessions
        if (filteredRunning.length > 0) {
            html += '<tr><td colspan="6" class="section-label section-label-running">RUNNING (' + filteredRunning.length + ')</td></tr>';
            filteredRunning.forEach(function (s) {
                html += renderSessionRow(s);
            });
        }

        // Recent/paginated section
        if (filteredPaginated.length > 0) {
            var statusFilter = $('#filter-status').value;
            var sectionTitle = 'RECENT';
            if (statusFilter === 'completed') sectionTitle = 'COMPLETED';
            else if (statusFilter === 'error') sectionTitle = 'ERROR';
            else if (statusFilter === 'cancelled') sectionTitle = 'CANCELLED';
            else if (statusFilter === 'timeout') sectionTitle = 'TIMEOUT';

            if (filteredRunning.length > 0) {
                html += '<tr><td colspan="6" class="section-label">' + sectionTitle + '</td></tr>';
            }
            filteredPaginated.forEach(function (s) {
                html += renderSessionRow(s);
            });
        }

        tbody.innerHTML = html;

        // Start live timers for running tasks
        if (filteredRunning.length > 0) {
            startRunningTimers();
        } else if (window._runningTimer) {
            clearInterval(window._runningTimer);
            window._runningTimer = null;
        }

        // Click and keyboard handlers
        tbody.querySelectorAll('tr.clickable').forEach(function (row) {
            row.addEventListener('click', function () {
                navigate('session/' + row.dataset.sessionId);
            });
            row.addEventListener('keydown', function (e) {
                if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    navigate('session/' + row.dataset.sessionId);
                }
            });
        });
    }

    function renderSessions(sessions) {
        var tbody = $('#sessions-tbody');
        var table = $('#sessions-table');
        var statusFilter = $('#filter-status').value;
        var searchFilter = $('#filter-search').value.toLowerCase();

        if (table) table.hidden = false;

        var filtered = sessions.filter(function (s) {
            if (statusFilter && s.status !== statusFilter) return false;
            if (searchFilter) {
                var text = (s.id + ' ' + (s.task_name || '') + ' ' + (s.trigger_context || '') + ' ' + (s.prompt || '')).toLowerCase();
                if (!text.includes(searchFilter)) return false;
            }
            return true;
        });

        var empty = $('#sessions-empty');
        if (filtered.length === 0) {
            tbody.innerHTML = '';
            empty.innerHTML = '<p>No sessions match your filters.</p>';
            show(empty);
            return;
        }
        hide(empty);

        // Split into running and recent
        var running = filtered.filter(function (s) { return s.status === 'running'; });
        var recent = filtered.filter(function (s) { return s.status !== 'running'; });

        var html = '';

        // Running section
        if (running.length > 0) {
            html += '<tr><td colspan="6" class="section-label section-label-running">RUNNING (' + running.length + ')</td></tr>';
            running.forEach(function (s) {
                html += renderSessionRow(s);
            });
        }

        // Recent section
        if (recent.length > 0) {
            if (running.length > 0) {
                html += '<tr><td colspan="6" class="section-label">RECENT</td></tr>';
            }
            recent.forEach(function (s) {
                html += renderSessionRow(s);
            });
        }

        tbody.innerHTML = html;

        // Start live timers for running tasks
        if (running.length > 0) {
            startRunningTimers();
        } else if (window._runningTimer) {
            clearInterval(window._runningTimer);
            window._runningTimer = null;
        }

        // Click and keyboard handlers
        tbody.querySelectorAll('tr.clickable').forEach(function (row) {
            row.addEventListener('click', function () {
                navigate('session/' + row.dataset.sessionId);
            });
            row.addEventListener('keydown', function (e) {
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
        if (window._runningTimer) {
            clearInterval(window._runningTimer);
            window._runningTimer = null;
        }
    }

    // ---------------------
    // New Session
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
                return c.provider !== 'github' && c.provider !== 'gitlab' && c.provider !== 'jira' && c.provider !== 'splunk' && c.provider !== 'generic';
            });
            select.innerHTML = '<option value="">Select a provider</option>';
            llmCreds.forEach((c) => {
                const label = c.name + ' (' + (c.provider === 'google-vertex' ? 'Vertex AI' : c.provider === 'claude-oauth' ? 'Claude Pro/Max' : 'Anthropic') + ')';
                select.innerHTML += '<option value="' + escapeHtml(c.name) + '">' + escapeHtml(label) + '</option>';
            });
            if (llmCreds.length === 1) select.selectedIndex = 1;
            checkTaskPrerequisites();
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                select.innerHTML = '<option value="">Failed to load providers</option>';
            }
        }
    }

    // Timeout slider
    $('#task-timeout').addEventListener('input', (e) => {
        $('#timeout-value').textContent = e.target.value;
    });

    // Start session
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
            return c.provider !== 'github' && c.provider !== 'gitlab' && c.provider !== 'jira' && c.provider !== 'splunk' && c.provider !== 'generic';
        });
        if (llmCreds.length === 0) {
            errEl.textContent = 'No LLM provider configured. Add an LLM credential on the Credentials page before starting sessions.';
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
        btn.textContent = 'Starting...';

        try {
            const resp = await api('POST', '/api/v1/sessions', payload);
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                throw new Error(data.error || data.message || 'Failed to start session.');
            }
            const data = await resp.json();
            const sessionId = data.session_id || data.id || '';

            successEl.innerHTML = 'Session started! Session ID: <span class="mono">' +
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Start Session';
        }
    });

    // ---------------------
    // Credentials
    // ---------------------
    async function loadCredentials() {
        const tbodyLlm = $('#credentials-tbody-llm');
        const tbodyScm = $('#credentials-tbody-scm');
        const tbodyGeneric = $('#credentials-tbody-generic');
        const sectionLlm = $('#credentials-section-llm');
        const sectionScm = $('#credentials-section-scm');
        const sectionGeneric = $('#credentials-section-generic');
        const loading = $('#credentials-loading');
        const empty = $('#credentials-empty');

        tbodyLlm.innerHTML = '';
        tbodyScm.innerHTML = '';
        tbodyGeneric.innerHTML = '';
        hide(sectionLlm);
        hide(sectionScm);
        hide(sectionGeneric);
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

            // Split credentials into LLM, SCM, and generic groups
            var llmCreds = [];
            var scmCreds = [];
            var genericCreds = [];
            credentials.forEach(function (c) {
                if (c.provider === 'generic') {
                    genericCreds.push(c);
                } else if (c.provider === 'github' || c.provider === 'gitlab' || c.provider === 'jira' || c.provider === 'splunk') {
                    scmCreds.push(c);
                } else {
                    llmCreds.push(c);
                }
            });

            function renderLlmRow(c) {
                const name = c.name || '-';
                const provider = c.provider === 'google-vertex' ? 'Vertex AI' : (c.provider === 'claude-oauth' ? 'Claude Pro/Max' : (c.provider === 'anthropic' ? 'Anthropic' : escapeHtml(c.provider || '-')));
                var authBadge = '';
                if (c.auth_type === 'api_key') {
                    authBadge = '<span class="badge">API Key</span>';
                } else if (c.auth_type === 'service_account') {
                    authBadge = '<span class="badge badge-running">Service Account</span>';
                } else if (c.auth_type === 'adc') {
                    authBadge = '<span class="badge badge-completed">ADC</span>';
                } else if (c.auth_type === 'oauth_token') {
                    authBadge = '<span class="badge badge-completed">OAuth</span>';
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
                const provider = c.provider === 'github' ? 'GitHub' : (c.provider === 'gitlab' ? 'GitLab' : (c.provider === 'jira' ? 'Jira' : (c.provider === 'splunk' ? 'Splunk' : escapeHtml(c.provider || '-'))));
                var authBadge = c.provider === 'splunk' ? '<span class="badge">API Key</span>' : '<span class="badge">PAT</span>';
                var host = '-';
                if (c.provider === 'gitlab' && c.gitlab_host) {
                    host = escapeHtml(c.gitlab_host);
                } else if (c.provider === 'jira' && c.api_host) {
                    host = escapeHtml(c.api_host);
                } else if (c.provider === 'splunk' && c.api_host) {
                    host = escapeHtml(c.api_host);
                } else if (c.provider === 'github') {
                    host = 'github.com';
                } else if (c.provider === 'gitlab') {
                    host = 'gitlab.com';
                } else if (c.provider === 'jira') {
                    host = 'atlassian.net';
                } else if (c.provider === 'splunk') {
                    host = 'Splunk Cloud';
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

            // Render generic secrets
            if (genericCreds.length > 0) {
                show(sectionGeneric);
                tbodyGeneric.innerHTML = genericCreds.map(function(c) {
                    var name = c.name || '-';
                    var created = formatTime(c.created_at || c.created);
                    var id = c.id || '';
                    return '<tr>' +
                        '<td>' + escapeHtml(name) + '</td>' +
                        '<td><span class="badge">secret</span></td>' +
                        '<td>' + escapeHtml(created) + '</td>' +
                        '<td><button class="btn btn-small btn-outline delete-credential-btn" data-id="' + escapeHtml(id) + '" data-name="' + escapeHtml(name) + '" style="color:var(--status-error);border-color:var(--status-error);">Delete</button></td>' +
                        '</tr>';
                }).join('');
            } else {
                hide(sectionGeneric);
                tbodyGeneric.innerHTML = '';
            }

            // Delete handlers for all sections
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
                        if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                            alert('Failed to delete credential.');
                        }
                        btn.disabled = false;
                    }
                });
            });
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
        var claudeOauthFields = q('cred-claude-oauth-fields');
        var vertexFields = q('cred-vertex-fields');
        var scmFields = q('cred-scm-fields');
        var splunkFields = q('cred-splunk-fields');
        var genericFields = q('cred-generic-fields');
        var gitlabHostGroup = q('cred-gitlab-host-group');
        var jiraHostGroup = q('jira-host-group');
        var jiraEmailGroup = q('jira-email-group');
        var patLabel = scmFields ? scmFields.querySelector('label') : null;

        providerSelect.addEventListener('change', function() {
            var val = this.value;

            // Hide all
            if (anthropicFields) anthropicFields.hidden = true;
            if (claudeOauthFields) claudeOauthFields.hidden = true;
            if (vertexFields) vertexFields.hidden = true;
            if (scmFields) scmFields.hidden = true;
            if (splunkFields) splunkFields.hidden = true;
            if (genericFields) genericFields.hidden = true;
            if (jiraHostGroup) jiraHostGroup.hidden = true;
            if (jiraEmailGroup) jiraEmailGroup.hidden = true;

            if (val === 'anthropic') {
                if (anthropicFields) anthropicFields.hidden = false;
            } else if (val === 'claude-oauth') {
                if (claudeOauthFields) claudeOauthFields.hidden = false;
            } else if (val === 'google-vertex') {
                if (vertexFields) vertexFields.hidden = false;
            } else if (val === 'splunk') {
                if (splunkFields) splunkFields.hidden = false;
            } else if (val === 'generic') {
                if (genericFields) genericFields.hidden = false;
            } else if (val === 'github' || val === 'gitlab' || val === 'jira') {
                if (scmFields) scmFields.hidden = false;
                // Show GitLab host field only for GitLab
                if (gitlabHostGroup) gitlabHostGroup.hidden = (val !== 'gitlab');
                // Show Jira-specific fields
                if (jiraHostGroup) jiraHostGroup.hidden = (val !== 'jira');
                if (jiraEmailGroup) jiraEmailGroup.hidden = (val !== 'jira');
                // Update PAT label for Jira
                if (patLabel) {
                    patLabel.innerHTML = val === 'jira'
                        ? 'API Token <span class="required">*</span>'
                        : 'Personal Access Token <span class="required">*</span>';
                }
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

        // Wire up form save via button click (more reliable than form submit
        // in modal/overlay contexts where the form element may not properly
        // associate with its submit button after template cloning)
        var form = container.querySelector('form');
        var errorEl = q('credential-form-error');
        var submitBtnEl = q('cred-submit');

        if (form) {
            form.addEventListener('submit', function(e) { e.preventDefault(); });
        }

        (submitBtnEl || form).addEventListener('click', async function(e) {
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
            } else if (provider === 'claude-oauth') {
                var oauthToken = (q('cred-oauth-token') || {}).value?.trim();
                if (!oauthToken) { if (errorEl) { errorEl.textContent = 'Setup token is required. Run "claude setup-token" in your terminal.'; show(errorEl); } return; }
                payload.auth_type = 'oauth_token';
                payload.credential = oauthToken;
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
            } else if (provider === 'jira') {
                var pat = (q('cred-pat') || {}).value?.trim();
                var jiraEmail = (q('cred-jira-email') || {}).value?.trim();
                var jiraHost = (q('cred-jira-host') || {}).value?.trim();

                if (!pat || !jiraEmail || !jiraHost) {
                    if (errorEl) { errorEl.textContent = 'Jira instance URL, email, and API token are all required.'; show(errorEl); }
                    return;
                }

                payload.auth_type = 'pat';
                payload.credential = jiraEmail + ':' + pat;
                payload.api_host = jiraHost;
            } else if (provider === 'splunk') {
                var splunkToken = (q('cred-splunk-token') || {}).value?.trim();
                if (!splunkToken) {
                    if (errorEl) { errorEl.textContent = 'Splunk API token is required.'; show(errorEl); }
                    return;
                }
                payload.auth_type = 'api_key';
                payload.credential = splunkToken;
                var splunkHost = (q('cred-splunk-host') || {}).value?.trim();
                if (splunkHost) {
                    payload.api_host = splunkHost;
                }
            } else if (provider === 'generic') {
                var genericValue = (q('cred-generic-value') || {}).value;
                if (!genericValue || !genericValue.trim()) {
                    if (errorEl) { errorEl.textContent = 'Secret value is required.'; show(errorEl); }
                    return;
                }
                payload.auth_type = 'secret';
                payload.credential = genericValue.trim();
            }

            var btn = q('cred-submit');
            if (btn) { btn.disabled = true; btn.textContent = 'Saving...'; }

            try {
                await options.onSubmit(payload);
                form.reset();
                container.hidden = true;
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

                if (resp.status === 409) {
                    var conflict = await resp.json();
                    var replace = confirm(
                        'You already have an LLM credential: "' + conflict.existing_credential +
                        '" (' + (conflict.existing_provider === 'google-vertex' ? 'Google Vertex AI' :
                                 conflict.existing_provider === 'claude-oauth' ? 'Claude Pro/Max' : 'Anthropic') +
                        ').\n\nReplace it with this new credential?'
                    );
                    if (!replace) {
                        return;
                    }
                    await api('DELETE', '/api/v1/credentials/' + conflict.existing_id);
                    resp = await api('POST', '/api/v1/credentials', payload);
                }

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
                        if (s.status === 'running') {
                            showLiveIndicator();
                        }
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
                if (!confirm('Are you sure you want to cancel this session? The running session will be terminated.')) return;
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
                    if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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

        // Show Live indicator while session is running
        if (status === 'running') {
            showLiveIndicator();
        } else {
            hideLiveIndicator();
        }

        try {
            const resp = await api('GET', '/api/v1/sessions/' + id + '/transcript');
            hide(loading);

            if (!resp.ok) {
                if (!silent) {
                    content.innerHTML = '<div class="transcript-system">No transcript available.</div>';
                }
                return;
            }

            const data = await resp.json();
            const events = Array.isArray(data) ? data : (data.transcript || data.events || []);

            if (events.length === 0) {
                if (!silent) {
                    content.innerHTML = '<div class="transcript-system">Waiting for output...</div>';
                }
                return;
            }

            // Re-render all events (simple and reliable)
            content.innerHTML = '';
            for (var i = 0; i < events.length; i++) {
                appendTranscriptEvent(content, events[i]);
            }
            // Auto-scroll to bottom
            content.scrollTop = content.scrollHeight;
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized' && !silent) {
                content.innerHTML = '<div class="transcript-system" style="color:var(--status-error)">Failed to load transcript.</div>';
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
    // Enhanced helper functions for transcript improvements
    function detectContentType(content) {
        if (!content || typeof content !== 'string') return 'unknown';

        // Try to parse as JSON first
        try {
            var parsed = JSON.parse(content);
            // Check if it looks like an API response
            if (typeof parsed === 'object' && parsed !== null) {
                if (parsed.url || parsed.status || parsed.headers || parsed.response) {
                    return 'api-response';
                }
                if (parsed.error || parsed.message || parsed.code) {
                    return 'error-response';
                }
                // GitHub API specific patterns
                if (parsed.html_url && (parsed.number || parsed.id)) {
                    return 'api-response';
                }
                // Issue/PR data
                if (parsed.title && (parsed.body || parsed.state)) {
                    return 'api-response';
                }
            }
            return 'json';
        } catch (e) {
            // Not JSON, check for other patterns
        }

        // Check for command output patterns
        if (content.match(/^Exit code \d+/m)) {
            return 'command-result';
        }

        // Check for HTTP response patterns
        if (content.match(/^HTTP\/\d+\.\d+ \d+/) || content.includes('Content-Type:') || content.includes('User-Agent:')) {
            return 'api-response';
        }

        // Check for error patterns
        if (content.match(/^(Error|Exception|Fatal|Critical):/mi) || content.includes('Traceback')) {
            return 'error-response';
        }

        // Check for code patterns - enhanced detection
        if (content.includes('function ') || content.includes('class ') ||
            content.includes('def ') || content.includes('import ') ||
            content.includes('package ') || content.includes('#!/') ||
            content.includes('const ') || content.includes('let ') ||
            content.includes('var ') || content.includes('=>') ||
            content.includes('func ') || content.includes('struct ')) {
            return 'code';
        }

        // Check for shell output patterns
        if (content.match(/^\$ /m) || content.match(/^.*@.*:\$/) || content.match(/^bash.*\$ /)) {
            return 'shell';
        }

        // Check for file paths
        if (content.match(/^\/[^\n]+$/) || content.match(/^[a-zA-Z]:[\\\/][^\n]+$/)) {
            return 'filepath';
        }

        // Check for Git output
        if (content.includes('commit ') && content.includes('Author:') ||
            content.includes('diff --git') || content.includes('@@')) {
            return 'code';
        }

        // Check for log patterns
        if (content.match(/^\d{4}-\d{2}-\d{2}.*\[.*\]/) || content.includes('INFO:') || content.includes('DEBUG:')) {
            return 'shell';
        }

        return 'text';
    }

    function generateToolResultSummary(content, isError, toolName) {
        if (!content) return 'Empty output';

        var contentType = detectContentType(content);
        var size = content.length;
        var lines = content.split('\n').length;

        var summary = '';

        if (isError) {
            summary = '❌ Error';
            // Try to extract meaningful error message from first line
            var firstLine = content.split('\n')[0].trim();
            if (firstLine && firstLine.length < 80) {
                summary += ': ' + firstLine;
            }
        } else if (contentType === 'api-response') {
            summary = '🌐 API response';
            try {
                var parsed = JSON.parse(content);
                if (parsed.status) {
                    summary += ' (' + parsed.status + ')';
                } else if (parsed.url) {
                    summary += ' from ' + parsed.url.split('/').pop();
                } else if (parsed.html_url) {
                    summary += ' (GitHub API)';
                } else if (parsed.message) {
                    var msg = parsed.message.substring(0, 40);
                    if (parsed.message.length > 40) msg += '...';
                    summary += ': ' + msg;
                }
            } catch (e) { /* ignore */ }
        } else if (contentType === 'error-response') {
            summary = '⚠️ Error response';
            try {
                var parsed = JSON.parse(content);
                if (parsed.message) {
                    var msg = parsed.message.substring(0, 50);
                    if (parsed.message.length > 50) msg += '...';
                    summary += ': ' + msg;
                }
            } catch (e) { /* ignore */ }
        } else if (contentType === 'command-result') {
            summary = '💻 Command output';
            // Show exit code if present
            var exitCodeMatch = content.match(/Exit code (\d+)/);
            if (exitCodeMatch) {
                var exitCode = exitCodeMatch[1];
                if (exitCode === '0') {
                    summary += ' ✅ (success)';
                } else {
                    summary += ' ❌ (exit ' + exitCode + ')';
                }
            }
            // Show first line of output as preview if it's short
            var outputLines = content.split('\n').filter(function(line) {
                return !line.match(/^Exit code \d+/) && line.trim() !== '';
            });
            if (outputLines.length > 0 && outputLines[0].length <= 60) {
                summary += ' - ' + outputLines[0];
            }
        } else if (contentType === 'json') {
            summary = '📄 JSON data';
            // For JSON, try to show key information
            try {
                var parsed = JSON.parse(content);
                if (Array.isArray(parsed)) {
                    summary += ' (' + parsed.length + ' items)';
                } else if (typeof parsed === 'object' && parsed !== null) {
                    var keys = Object.keys(parsed);
                    if (keys.length <= 3) {
                        summary += ' (' + keys.join(', ') + ')';
                    } else {
                        summary += ' (' + keys.length + ' keys)';
                    }
                    // Show specific useful fields if present
                    if (parsed.title || parsed.name) {
                        summary += ' - ' + (parsed.title || parsed.name);
                    } else if (parsed.id) {
                        summary += ' (id: ' + parsed.id + ')';
                    }
                }
            } catch (e) {
                // Fall through to size info
            }
        } else if (contentType === 'code') {
            summary = '🔧 Code output';
            // Try to identify programming language
            if (content.includes('function ') || content.includes('=>')) {
                summary = '🔧 JavaScript code';
            } else if (content.includes('def ') || content.includes('import ')) {
                summary = '🔧 Python code';
            } else if (content.includes('package ') || content.includes('func ')) {
                summary = '🔧 Go code';
            }
        } else if (contentType === 'shell') {
            summary = '💻 Shell output';
        } else if (contentType === 'filepath') {
            summary = '📁 File path';
            var filename = content.trim().split(/[\/\\]/).pop();
            if (filename.length < 40) {
                summary += ': ' + filename;
            }
        } else {
            summary = '📝 Text output';
            // For text, show a brief preview if it's short enough
            if (lines === 1 && size <= 100) {
                var preview = content.trim();
                if (preview.length <= 50) {
                    summary += ': ' + preview;
                }
            } else if (lines <= 3 && size <= 150) {
                var preview = content.trim().replace(/\n/g, ' ').substring(0, 60);
                if (content.length > 60) preview += '...';
                summary += ': ' + preview;
            }
        }

        // Add size information for larger content
        if (lines > 5 || size > 500) {
            if (lines > 1) {
                summary += ' (' + lines + ' line' + (lines === 1 ? '' : 's');
                if (size > 1000) {
                    summary += ', ' + (size / 1000).toFixed(1) + 'k chars';
                }
                summary += ')';
            } else if (size > 200) {
                summary += ' (' + size.toLocaleString() + ' chars)';
            }
        }

        return summary;
    }

    function shouldAutoCollapse(content, contentType) {
        if (!content) return false;

        var lines = content.split('\n').length;
        var size = content.length;

        // Always auto-collapse JSON data, API responses, and error responses for better readability
        if (contentType === 'json' || contentType === 'api-response' || contentType === 'error-response') {
            return true;
        }

        // Auto-collapse large code blocks and command output
        if (contentType === 'code' && (lines > 10 || size > 500)) {
            return true;
        }

        if (contentType === 'command-result' && (lines > 5 || size > 300)) {
            return true;
        }

        // For file paths, don't auto-collapse single lines
        if (contentType === 'filepath' && lines === 1) {
            return false;
        }

        // Auto-collapse if more than 3 lines or more than 200 characters
        // Enhanced thresholds for better balance between readability and information density
        return lines > 3 || size > 200;
    }

    function applySyntaxHighlighting(content, contentType) {
        if (contentType === 'json') {
            try {
                var parsed = JSON.parse(content);
                var formatted = JSON.stringify(parsed, null, 2);
                return syntaxHighlightJSON(formatted);
            } catch (e) {
                return escapeHtml(content);
            }
        } else if (contentType === 'command-result') {
            // Highlight exit codes and common patterns in command output
            var escaped = escapeHtml(content);
            return escaped
                .replace(/(Exit code \d+)/g, '<span class="cmd-exit-code">$1</span>')
                .replace(/^(\$.*)/gm, '<span class="cmd-prompt">$1</span>')
                .replace(/(error:|failed:|warning:)/gi, '<span class="cmd-error">$1</span>');
        } else if (contentType === 'code') {
            var escaped = escapeHtml(content);
            return escaped
                .replace(/\b(function|const|let|var|if|else|for|while|return|class|def|import|package)\b/g, '<span class="code-keyword">$1</span>')
                .replace(/(".*?"|'.*?')/g, '<span class="code-string">$1</span>')
                .replace(/\/\/.*$/gm, '<span class="code-comment">$&</span>')
                .replace(/#.*$/gm, '<span class="code-comment">$&</span>');
        } else {
            return escapeHtml(content);
        }
    }

    function syntaxHighlightJSON(jsonString) {
        return jsonString
            .replace(/(".*?")/g, '<span class="json-string">$1</span>')
            .replace(/(\d+\.?\d*)/g, '<span class="json-number">$1</span>')
            .replace(/(true|false|null)/g, '<span class="json-literal">$1</span>')
            .replace(/("[^"]*")(\s*:)/g, '<span class="json-key">$1</span>$2');
    }

    function toolInputSummary(name, input) {
        if (!input) return '';
        if (typeof input === 'string') {
            try { input = JSON.parse(input); } catch(e) { return input.substring(0, 120); }
        }
        switch (name) {
            case 'Bash':
                var cmd = input.command || '';
                // Truncate very long commands for readability
                if (cmd.length > 80) {
                    cmd = cmd.substring(0, 77) + '...';
                }
                return cmd ? '$ ' + cmd : '';
            case 'Read':
                var path = input.file_path || '';
                if (input.limit && input.offset) {
                    return path + ' (lines ' + input.offset + '-' + (input.offset + input.limit) + ')';
                } else if (input.limit) {
                    return path + ' (first ' + input.limit + ' lines)';
                }
                return path;
            case 'Edit':
                var path = input.file_path || '';
                var changes = '';
                if (input.old_string && input.new_string) {
                    var oldPreview = input.old_string.substring(0, 40);
                    var newPreview = input.new_string.substring(0, 40);
                    if (input.old_string.length > 40) oldPreview += '...';
                    if (input.new_string.length > 40) newPreview += '...';
                    changes = ' (replace: "' + oldPreview + '" → "' + newPreview + '")';
                }
                return path + changes;
            case 'Write':
                var path = input.file_path || '';
                var content = input.content || '';
                if (content) {
                    var lines = content.split('\n').length;
                    return path + ' (' + lines + ' line' + (lines === 1 ? '' : 's') + ')';
                }
                return path;
            case 'Grep':
                var pattern = input.pattern || '';
                var path = input.path || '';
                var result = pattern ? '/' + pattern + '/' : '';
                if (path) result += ' in ' + path;
                if (input.recursive && !path.endsWith('/')) result += ' (recursive)';
                return result;
            case 'Glob':
                var pattern = input.pattern || '';
                if (input.recursive) pattern += ' (recursive)';
                return pattern;
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
            headerParts.push(isError ? '<span class="tx-result-icon">&#10007;</span> Session failed' : '<span class="tx-result-icon">&#10003;</span> Session completed');
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
            var stderr = '';

            // Format 2: tool_use_result envelope (the problematic format)
            if (ev.tool_use_result) {
                output = ev.tool_use_result.stdout || '';
                stderr = ev.tool_use_result.stderr || '';
            }
            // Format 1: direct content (string or array)
            else if (typeof ev.content === 'string') {
                output = ev.content;
            } else if (Array.isArray(ev.content)) {
                output = ev.content
                    .filter(function(b) { return b.type === 'text'; })
                    .map(function(b) { return b.text; })
                    .join('\n');
            } else if (ev.content && typeof ev.content === 'object') {
                // Check if content itself has stdout/stderr
                if (ev.content.stdout !== undefined) {
                    output = ev.content.stdout || '';
                    stderr = ev.content.stderr || '';
                } else {
                    output = JSON.stringify(ev.content, null, 2);
                }
            } else if (ev.output) {
                output = typeof ev.output === 'string' ? ev.output : JSON.stringify(ev.output, null, 2);
            }

            if (!output && !stderr) return; // skip empty tool results

            // Combine stderr + stdout for display
            if (stderr) {
                output = '\u26A0 stderr:\n' + stderr + (output ? '\n\n' + output : '');
            }

            var isError = ev.is_error || false;
            var contentType = detectContentType(output);

            // Try to pair with preceding tool_use card
            var toolUseId = ev.tool_use_id || '';
            var pairedCard = toolUseId ? container.querySelector('[data-tool-use-id="' + toolUseId + '"]') : null;

            if (pairedCard) {
                // Append output into the existing tool card
                var outputDiv = document.createElement('div');
                outputDiv.className = 'tx-tool-output-inline';

                var autoCollapse = output.split('\n').length > 8 || output.length > 500;
                var highlightedContent = applySyntaxHighlighting(output, contentType);
                var contentClass = 'tx-tool-output-pre tx-content-' + contentType;
                if (isError) contentClass += ' tx-tool-output-error';

                if (autoCollapse) {
                    var lineCount = output.split('\n').length;
                    var preview = output.split('\n').slice(0, 4).join('\n');
                    var previewHighlighted = applySyntaxHighlighting(preview, contentType);
                    outputDiv.innerHTML =
                        '<div class="tx-tool-output-preview"><pre class="' + contentClass + '">' + previewHighlighted + '</pre></div>' +
                        '<details class="tx-tool-output-expand">' +
                        '<summary class="tx-tool-expand-toggle">Show ' + (lineCount - 4) + ' more lines</summary>' +
                        '<pre class="' + contentClass + '">' + highlightedContent + '</pre>' +
                        '</details>';
                } else {
                    outputDiv.innerHTML = '<pre class="' + contentClass + '">' + highlightedContent + '</pre>';
                }

                pairedCard.appendChild(outputDiv);
                return;
            }

            // Fallback: render as standalone block (for unpaired results)
            var div = document.createElement('div');
            div.className = 'tx-tool-output-block tx-hierarchy-tool';
            var autoCollapse = shouldAutoCollapse(output, contentType);

            // Generate intelligent summary
            var summary = generateToolResultSummary(output, isError);

            if (autoCollapse) {
                // Use enhanced collapsible with syntax highlighting
                var highlightedContent = applySyntaxHighlighting(output, contentType);
                var contentClass = 'tx-tool-output-pre tx-content-' + contentType;
                if (isError) contentClass += ' tx-tool-output-error';

                div.innerHTML = '<details class="tx-tool-output-details">' +
                    '<summary class="tx-tool-result-summary' + (isError ? ' tx-tool-output-error' : '') + '">' +
                    '<span class="tx-tool-result-badge">' + summary + '</span>' +
                    '</summary>' +
                    '<pre class="' + contentClass + '">' + highlightedContent + '</pre>' +
                    '</details>';
            } else {
                // Short content - show directly with highlighting
                var highlightedContent = applySyntaxHighlighting(output, contentType);
                var contentClass = 'tx-tool-output-pre tx-content-' + contentType;
                if (isError) contentClass += ' tx-tool-output-error';

                div.innerHTML = '<pre class="' + contentClass + '">' + highlightedContent + '</pre>';
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
                    div.className = 'tx-msg tx-hierarchy-main';
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
                    div.className = 'tx-msg tx-hierarchy-main';
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
                    div.className = 'tx-tool tx-hierarchy-tool';
                    if (block.id) {
                        div.setAttribute('data-tool-use-id', block.id);
                    }

                    // Step counter for tool calls
                    if (!container._stepCount) container._stepCount = 0;
                    container._stepCount++;
                    var stepNum = container._stepCount;

                    var toolIcon = '&#x1F527;';

                    var headerHtml = '<div class="tx-tool-header">' +
                        '<span class="tx-step-num">Step ' + stepNum + '</span>' +
                        '<span class="tx-tool-icon">' + toolIcon + '</span>' +
                        '<span class="tx-tool-name">' + escapeHtml(toolName) + '</span>' +
                        '</div>';

                    var bodyHtml = '';
                    if (summary) {
                        bodyHtml = '<div class="tx-tool-summary"><pre class="tx-tool-cmd">' + escapeHtml(summary) + '</pre></div>';
                    }

                    // Show full input as collapsible for complex tools or when needed for transparency
                    var inputStr = typeof input === 'string' ? input : JSON.stringify(input, null, 2);
                    var inputType = detectContentType(inputStr);
                    var shouldCollapseInput = shouldAutoCollapse(inputStr, inputType);

                    // Always show collapsible input for better transparency, but with intelligent summaries
                    // Per issue #75: More aggressive collapsing of JSON data for better readability
                    var paramCount = Object.keys(input).length;
                    var inputSummary = 'Show details';

                    if (toolName === 'Edit') {
                        inputSummary = 'Show edit parameters';
                        // For Edit tool, always collapse the input parameters
                        shouldCollapseInput = true;
                    } else if (toolName === 'Bash') {
                        // For Bash, only show details if there are additional parameters beyond command
                        if (paramCount <= 1 && input.command) {
                            // Don't show collapsible section for simple bash commands
                        } else {
                            inputSummary = 'Show command parameters (' + paramCount + ' field' + (paramCount === 1 ? '' : 's') + ')';
                            shouldCollapseInput = true;
                        }
                    } else if (toolName === 'Read') {
                        if (paramCount <= 1) {
                            // Simple file read, don't need details section
                        } else {
                            inputSummary = 'Show read parameters (offset: ' + (input.offset || 0) + ', limit: ' + (input.limit || 'all') + ')';
                            shouldCollapseInput = true;
                        }
                    } else if (inputType === 'json') {
                        inputSummary = 'Show parameters (' + paramCount + ' field' + (paramCount === 1 ? '' : 's') + ')';
                        // Always collapse JSON input for better readability per issue #75
                        shouldCollapseInput = true;
                    } else {
                        inputSummary = 'Show input (' + inputStr.length + ' chars)';
                        shouldCollapseInput = shouldAutoCollapse(inputStr, inputType);
                    }

                    // Only add collapsible section if we determined it should be collapsed
                    if (shouldCollapseInput) {
                        var highlightedInput = applySyntaxHighlighting(inputStr, inputType);
                        bodyHtml += '<details class="tx-tool-input-details">' +
                            '<summary class="tx-tool-input-toggle">' + inputSummary + '</summary>' +
                            '<pre class="tx-tool-input-pre tx-content-' + inputType + '">' + highlightedInput + '</pre>' +
                            '</details>';
                    }

                    div.innerHTML = headerHtml + bodyHtml;
                    container.appendChild(div);
                    continue;
                }

                // Unknown block type — render as JSON with collapsible for better readability
                var jsonContent = JSON.stringify(block, null, 2);
                var contentType = detectContentType(jsonContent);
                var shouldCollapse = shouldAutoCollapse(jsonContent, contentType);

                var div = document.createElement('div');
                div.className = 'tx-system';

                if (shouldCollapse) {
                    var summary = generateToolResultSummary(jsonContent, false);
                    var highlightedContent = applySyntaxHighlighting(jsonContent, contentType);
                    div.innerHTML = '<details class="tx-tool-output-details">' +
                        '<summary class="tx-tool-result-summary">' +
                        '<span class="tx-tool-result-badge">' + summary + '</span>' +
                        '</summary>' +
                        '<pre class="tx-tool-input-pre tx-content-' + contentType + '">' + highlightedContent + '</pre>' +
                        '</details>';
                } else {
                    div.innerHTML = '<pre class="tx-tool-input-pre">' + escapeHtml(jsonContent) + '</pre>';
                }
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
            div.className = 'tx-tool tx-hierarchy-tool';
            var headerHtml = '<div class="tx-tool-header">' +
                '<span class="tx-tool-icon">&#x1F527;</span>' +
                '<span class="tx-tool-name">' + escapeHtml(toolName) + '</span>' +
                '</div>';
            var bodyHtml = '';
            if (summary) {
                bodyHtml = '<div class="tx-tool-summary"><pre class="tx-tool-cmd">' + escapeHtml(summary) + '</pre></div>';
            }
            if (output) {
                var contentType = detectContentType(output);
                var autoCollapse = shouldAutoCollapse(output, contentType);
                var outputSummary = generateToolResultSummary(output, false, toolName);
                var highlightedOutput = applySyntaxHighlighting(output, contentType);

                if (autoCollapse) {
                    bodyHtml += '<details class="tx-tool-output-details">' +
                        '<summary class="tx-tool-output-summary tx-tool-result-summary">' +
                        '<span class="tx-tool-result-badge">' + outputSummary + '</span>' +
                        '</summary>' +
                        '<pre class="tx-tool-output-pre tx-content-' + contentType + '">' + highlightedOutput + '</pre>' +
                        '</details>';
                } else {
                    bodyHtml += '<pre class="tx-tool-output-pre tx-content-' + contentType + '">' + highlightedOutput + '</pre>';
                }
            }
            div.innerHTML = headerHtml + bodyHtml;
            container.appendChild(div);
            return;
        }

        // --- user / human ---
        if (type === 'user' || type === 'human') {
            var text = '';
            var hasToolResults = false;

            // First, check for text content in the standard locations
            if (typeof ev.content === 'string') text = ev.content;
            else if (Array.isArray(ev.content)) {
                text = ev.content.filter(function(b) { return b.type === 'text'; }).map(function(b) { return b.text; }).join('\n');
            } else if (ev.message && typeof ev.message === 'string') text = ev.message;
            else if (ev.text) text = ev.text;

            // Check for tool_result blocks in ev.message.content[]
            var messageContent = ev.message && Array.isArray(ev.message.content) ? ev.message.content : [];
            var toolResultBlocks = messageContent.filter(function(block) { return block.type === 'tool_result'; });

            if (toolResultBlocks.length > 0) {
                hasToolResults = true;
                // Render each tool_result block using the existing tool_result logic
                toolResultBlocks.forEach(function(toolResultBlock) {
                    // Create a synthetic event that matches the tool_result handler expectations
                    var syntheticEvent = {
                        type: 'tool_result',
                        content: toolResultBlock.content,
                        tool_use_id: toolResultBlock.tool_use_id,
                        is_error: toolResultBlock.is_error || false,
                        tool_use_result: toolResultBlock.tool_use_result
                    };

                    // Call the existing tool_result handler logic
                    appendTranscriptEvent(container, syntheticEvent);
                });
            }

            // If we have text content, render it
            if (text) {
                var div = document.createElement('div');
                div.className = 'tx-msg tx-msg-user tx-hierarchy-main';
                div.innerHTML = '<div class="tx-msg-label">User</div>' +
                    '<div class="tx-msg-body">' + renderMarkdown(text) + '</div>';
                container.appendChild(div);
            }

            // Only fall back to JSON.stringify if we have neither text nor tool results
            if (!text && !hasToolResults) {
                var div = document.createElement('div');
                div.className = 'tx-msg tx-msg-user tx-hierarchy-main';
                div.innerHTML = '<div class="tx-msg-label">User</div>' +
                    '<div class="tx-msg-body"><pre>' + JSON.stringify(ev, null, 2) + '</pre></div>';
                container.appendChild(div);
            }

            return;
        }

        // --- Fallback for unknown types ---
        var div = document.createElement('div');
        div.className = 'tx-system';
        var body = ev.content || ev.text || ev.message || '';
        if (typeof body !== 'string') body = JSON.stringify(body || ev, null, 2);

        // Apply collapsible logic for large or JSON content in fallback cases too
        var contentType = detectContentType(body);
        var shouldCollapse = shouldAutoCollapse(body, contentType);

        if (shouldCollapse) {
            var summary = generateToolResultSummary(body, false);
            var highlightedContent = applySyntaxHighlighting(body, contentType);
            div.innerHTML = '<span class="tx-system-icon">&#9679;</span> ' + escapeHtml(type) + ': ' +
                '<details class="tx-tool-output-details">' +
                '<summary class="tx-tool-result-summary">' +
                '<span class="tx-tool-result-badge">' + summary + '</span>' +
                '</summary>' +
                '<pre class="tx-tool-input-pre tx-content-' + contentType + '">' + highlightedContent + '</pre>' +
                '</details>';
        } else {
            div.innerHTML = '<span class="tx-system-icon">&#9679;</span> ' + escapeHtml(type) + ': ' + escapeHtml(body);
        }
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
        // No-op: streaming removed, transcript uses polling
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
            // Reset sort/filter state on fresh load
            proxyLogSortField = 'timestamp';
            proxyLogSortAsc = true;
            $('#proxy-log-filter-service').value = '';
            $('#proxy-log-filter-decision').value = '';
        }

        try {
            const resp = await api('GET', '/api/v1/sessions/' + id + '/proxy-log');
            hide(loading);

            if (!resp.ok) {
                proxyLogData = [];
                hide($('#proxy-log-filters'));
                tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--text-muted)">No proxy log available.</td></tr>';
                return;
            }

            const data = await resp.json();
            const entries = Array.isArray(data) ? data : (data.proxy_log || data.entries || data.logs || []);
            proxyLogData = entries;
            renderProxyLog();
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                proxyLogData = [];
                hide($('#proxy-log-filters'));
                tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--status-error)">Failed to load proxy log.</td></tr>';
            }
        }
    }

    function renderProxyLog() {
        const tbody = $('#proxy-log-tbody');
        const filtersBar = $('#proxy-log-filters');
        const summaryEl = $('#proxy-log-summary');
        const serviceFilter = $('#proxy-log-filter-service');
        const decisionFilter = $('#proxy-log-filter-decision');

        if (proxyLogData.length === 0) {
            hide(filtersBar);
            tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--text-muted)">No proxy log entries.</td></tr>';
            return;
        }

        // Populate service filter dropdown (preserve current selection)
        const currentService = serviceFilter.value;
        const uniqueServices = [...new Set(proxyLogData.map(e => (e.service || '').toUpperCase()).filter(s => s))].sort();
        serviceFilter.innerHTML = '<option value="">All Services</option>' +
            uniqueServices.map(s => '<option value="' + escapeHtml(s) + '"' + (s === currentService ? ' selected' : '') + '>' + escapeHtml(s) + '</option>').join('');

        // Filter
        const svcVal = serviceFilter.value;
        const decVal = decisionFilter.value;
        let filtered = proxyLogData;
        if (svcVal) {
            filtered = filtered.filter(e => (e.service || '').toUpperCase() === svcVal);
        }
        if (decVal) {
            filtered = filtered.filter(e => (e.decision || e.action || '').toLowerCase() === decVal);
        }

        // Sort
        const sortField = proxyLogSortField;
        const sortAsc = proxyLogSortAsc;
        filtered = filtered.slice().sort((a, b) => {
            let va, vb;
            if (sortField === 'timestamp') {
                va = new Date(a.timestamp || 0).getTime();
                vb = new Date(b.timestamp || 0).getTime();
            } else if (sortField === 'status_code') {
                va = a.status_code !== undefined && a.status_code !== null ? Number(a.status_code) : -1;
                vb = b.status_code !== undefined && b.status_code !== null ? Number(b.status_code) : -1;
            } else if (sortField === 'url') {
                va = (a.url || a.path || '').toLowerCase();
                vb = (b.url || b.path || '').toLowerCase();
            } else if (sortField === 'decision') {
                va = (a.decision || a.action || '').toLowerCase();
                vb = (b.decision || b.action || '').toLowerCase();
            } else if (sortField === 'service') {
                va = (a.service || '').toLowerCase();
                vb = (b.service || '').toLowerCase();
            } else {
                va = (a[sortField] || '').toString().toLowerCase();
                vb = (b[sortField] || '').toString().toLowerCase();
            }
            if (va < vb) return sortAsc ? -1 : 1;
            if (va > vb) return sortAsc ? 1 : -1;
            return 0;
        });

        // Render rows
        tbody.innerHTML = filtered.map(function(e) {
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

        if (filtered.length === 0) {
            tbody.innerHTML = '<tr><td colspan="7" style="text-align:center;color:var(--text-muted)">No entries match filters.</td></tr>';
        }

        // Summary counts
        const allowCount = filtered.filter(e => (e.decision || e.action || '').toLowerCase() === 'allow').length;
        const denyCount = filtered.filter(e => (e.decision || e.action || '').toLowerCase() === 'deny').length;
        summaryEl.textContent = filtered.length + ' entries (' + allowCount + ' allow, ' + denyCount + ' deny)';

        // Update sort indicators
        $$('#detail-proxy-log .sortable').forEach(function(th) {
            const indicator = th.querySelector('.sort-indicator');
            if (th.dataset.sort === proxyLogSortField) {
                indicator.textContent = proxyLogSortAsc ? '\u25B2' : '\u25BC';
                indicator.classList.add('active');
            } else {
                indicator.textContent = '';
                indicator.classList.remove('active');
            }
        });

        // Show filter bar
        show(filtersBar);
    }

    // Sort click handlers
    $$('#detail-proxy-log .sortable').forEach(function(th) {
        th.addEventListener('click', function() {
            const field = th.dataset.sort;
            if (proxyLogSortField === field) {
                proxyLogSortAsc = !proxyLogSortAsc;
            } else {
                proxyLogSortField = field;
                proxyLogSortAsc = true;
            }
            renderProxyLog();
        });
    });

    // Filter change handlers
    $('#proxy-log-filter-service').addEventListener('change', function() { renderProxyLog(); });
    $('#proxy-log-filter-decision').addEventListener('change', function() { renderProxyLog(); });

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
                const sessionCount = u.session_count || 0;
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
                    '<td>' + sessionCount + '</td>' +
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
                        if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
                        if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                            alert('Failed to delete user.');
                        }
                        btn.disabled = false;
                    }
                });
            });

            // Reset password click handlers
            tbody.querySelectorAll('.reset-pw-btn').forEach((btn) => {
                btn.addEventListener('click', () => {
                    const username = btn.dataset.username;
                    const modal = $('#admin-reset-password-modal');
                    $('#reset-pw-username').textContent = username;
                    $('#reset-pw-new').value = '';
                    $('#reset-pw-confirm').value = '';
                    hide($('#reset-pw-error'));
                    modal.dataset.username = username;
                    show(modal);
                    $('#reset-pw-new').focus();
                });
            });
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;color:var(--status-error);">Failed to load users.</td></tr>';
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
        const confirmPassword = $('#new-user-password-confirm').value;
        const isAdminChecked = $('#new-user-admin').checked;

        if (!username || !password) {
            errEl.textContent = 'Username and password are required.';
            show(errEl);
            return;
        }

        if (password.length < 8) {
            errEl.textContent = 'Password must be at least 8 characters.';
            show(errEl);
            return;
        }

        if (password !== confirmPassword) {
            errEl.textContent = 'Passwords do not match.';
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Create User';
        }
    });

    // Admin reset password modal
    $('#reset-pw-cancel').addEventListener('click', () => {
        hide($('#admin-reset-password-modal'));
    });

    $('#admin-reset-password-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const errEl = $('#reset-pw-error');
        hide(errEl);

        const modal = $('#admin-reset-password-modal');
        const username = modal.dataset.username;
        const newPw = $('#reset-pw-new').value;
        const confirmPw = $('#reset-pw-confirm').value;

        if (newPw.length < 8) {
            errEl.textContent = 'Password must be at least 8 characters.';
            show(errEl);
            return;
        }

        if (newPw !== confirmPw) {
            errEl.textContent = 'Passwords do not match.';
            show(errEl);
            return;
        }

        const btn = e.target.querySelector('button[type="submit"]');
        btn.disabled = true;
        btn.textContent = 'Resetting...';

        try {
            const resp = await api('PUT', '/api/v1/users/' + encodeURIComponent(username) + '/password', {password: newPw});
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                throw new Error(data.error || 'Failed to reset password.');
            }
            hide(modal);
            loadUsers();
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Reset Password';
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

    async function loadScheduleProviders() {
        const select = $('#sched-provider');
        try {
            const resp = await api('GET', '/api/v1/credentials');
            const data = await resp.json();
            const creds = data.credentials || [];
            select.innerHTML = '<option value="">Select a provider</option>';
            creds.forEach(function (c) {
                const label = c.name + ' (' + (c.provider === 'google-vertex' ? 'Vertex AI' : c.provider === 'claude-oauth' ? 'Claude Pro/Max' : 'Anthropic') + ')';
                select.innerHTML += '<option value="' + escapeHtml(c.name) + '">' + escapeHtml(label) + '</option>';
            });
            if (creds.length === 1) select.selectedIndex = 1;
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
        // Reset trigger type and event config
        $('#sched-trigger-type').value = 'cron';
        $('#sched-trigger-type').dispatchEvent(new Event('change'));
        document.querySelectorAll('.event-checkbox').forEach(function(cb) { cb.checked = false; });
        $('#sched-event-repos').value = '';
        $('#sched-event-branches').value = '';
        document.querySelectorAll('input[name="sched-delivery-mode"]').forEach(function(r) {
            r.checked = (r.value === 'polling');
        });
        $('#delivery-polling-info').hidden = false;
        $('#delivery-webhook-info').hidden = true;
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
        hide($('#event-config-section'));
        $('#sched-trigger-type').value = 'cron';
        editingScheduleId = null;
        $('#schedule-submit-btn').textContent = 'Create Schedule';
    });

    // Schedule timeout slider
    $('#sched-timeout').addEventListener('input', function (e) {
        $('#sched-timeout-value').textContent = e.target.value;
    });

    // Trigger type toggle
    $('#sched-trigger-type').addEventListener('change', function () {
        var type = this.value;
        var cronField = $('#sched-cron').closest('.form-group-half') || $('#sched-cron').parentElement;
        var naturalField = $('#sched-natural').parentElement;
        var cronHelp = document.querySelector('#schedule-form .cron-help');
        var eventSection = $('#event-config-section');

        if (type === 'cron') {
            cronField.hidden = false;
            if (naturalField) naturalField.hidden = false;
            if (cronHelp) cronHelp.hidden = false;
            hide(eventSection);
            $('#sched-cron').required = true;
        } else if (type === 'event') {
            cronField.hidden = true;
            if (naturalField) naturalField.hidden = true;
            if (cronHelp) cronHelp.hidden = true;
            show(eventSection);
            $('#sched-cron').required = false;
        } else {
            // cron-and-event
            cronField.hidden = false;
            if (naturalField) naturalField.hidden = false;
            if (cronHelp) cronHelp.hidden = false;
            show(eventSection);
            $('#sched-cron').required = true;
        }
    });

    // Delivery mode toggle (polling vs webhook)
    document.addEventListener('change', function(e) {
        if (e.target.name === 'sched-delivery-mode') {
            var isPolling = e.target.value === 'polling';
            $('#delivery-polling-info').hidden = !isPolling;
            $('#delivery-webhook-info').hidden = isPolling;
            if (!isPolling) {
                $('#schedule-webhook-url').textContent = window.location.origin + basePath + '/api/v1/webhooks/github';
            }
        }
    });

    // Submit schedule form
    $('#schedule-form').addEventListener('submit', async function (e) {
        e.preventDefault();
        const errEl = $('#schedule-form-error');
        hide(errEl);

        const name = $('#sched-name').value.trim();
        const cron = $('#sched-cron').value.trim();
        const prompt = $('#sched-prompt').value.trim();
        const triggerType = $('#sched-trigger-type').value;

        if (!name || !prompt) {
            errEl.textContent = 'Name and prompt are required.';
            show(errEl);
            return;
        }

        if ((triggerType === 'cron' || triggerType === 'cron-and-event') && !cron) {
            errEl.textContent = 'Cron expression is required for cron-based triggers.';
            show(errEl);
            return;
        }

        const payload = {
            name: name,
            cron: (triggerType === 'event') ? undefined : cron,
            prompt: prompt,
            provider: $('#sched-provider').value || undefined,
            repo: $('#sched-repo').value.trim() || undefined,
            timeout: parseInt($('#sched-timeout').value, 10) * 60,
            debug: $('#sched-debug').checked,
            enabled: $('#sched-enabled').checked,
            trigger_type: triggerType
        };

        // Add event config if applicable
        if (triggerType === 'event' || triggerType === 'cron-and-event') {
            var selectedEvents = [];
            document.querySelectorAll('.event-checkbox:checked').forEach(function(cb) {
                selectedEvents.push(cb.value);
            });
            var eventRepos = $('#sched-event-repos').value.trim();
            var eventBranches = $('#sched-event-branches').value.trim();
            payload.event_config = {
                events: selectedEvents.length > 0 ? selectedEvents : undefined,
                repos: eventRepos ? eventRepos.split(',').map(function(s) { return s.trim(); }) : undefined,
                branches: eventBranches ? eventBranches.split(',').map(function(s) { return s.trim(); }) : undefined
            };
            var deliveryMode = document.querySelector('input[name="sched-delivery-mode"]:checked');
            if (deliveryMode) {
                payload.event_config.delivery_mode = deliveryMode.value;
            }
            // Clean undefined fields from event_config
            Object.keys(payload.event_config).forEach(function(k) {
                if (payload.event_config[k] === undefined) delete payload.event_config[k];
            });
            if (Object.keys(payload.event_config).length === 0) delete payload.event_config;
        }

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
            loadUnifiedSchedules();
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Create Schedule';
        }
    });

    // ---------------------
    // Security page
    // ---------------------
    async function loadSecurityPage() {
        var container = $('#security-profiles-list');
        var loading = $('#security-loading');
        var empty = $('#security-empty');

        container.innerHTML = '';
        show(loading);
        hide(empty);

        try {
            var resp = await api('GET', '/api/v1/security-profiles');
            var data = await resp.json();
            hide(loading);

            var profiles = Array.isArray(data) ? data : (data.profiles || data.items || []);
            allProfiles = profiles;

            // Sort: YAML profiles first, then user profiles, alphabetical within each group
            profiles.sort(function (a, b) {
                var aIsYaml = a.source === 'yaml' ? 0 : 1;
                var bIsYaml = b.source === 'yaml' ? 0 : 1;
                if (aIsYaml !== bIsYaml) return aIsYaml - bIsYaml;
                return (a.name || '').localeCompare(b.name || '');
            });

            if (profiles.length === 0) {
                show(empty);
            } else {
                hide(empty);
                container.innerHTML = profiles.map(function (p) { return renderProfileCard(p, p.source === 'yaml'); }).join('');
            }

            attachProfileCardHandlers();
        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                container.innerHTML = '<p style="color:var(--status-error);font-size:13px;">Failed to load profiles. Check your connection and try again.</p>';
            }
        }

        // Check system LLM status and disable AI Builder if not configured
        try {
            var sysResp = await api('GET', '/api/v1/system-info');
            var sysInfo = await sysResp.json();
            var generateBtn = $('#profile-ai-generate');
            var aiHint = $('#ai-builder-hint');
            if (generateBtn) {
                if (!sysInfo.system_llm || !sysInfo.system_llm.configured) {
                    systemLLMConfigured = false;
                    generateBtn.disabled = true;
                    generateBtn.title = 'AI Builder requires a system LLM. Ask your administrator to configure system_llm in alcove.yaml.';
                    generateBtn.style.opacity = '0.5';
                    generateBtn.style.cursor = 'not-allowed';
                    if (aiHint) show(aiHint);
                    setProfileMode('manual');
                } else {
                    systemLLMConfigured = true;
                    generateBtn.disabled = false;
                    generateBtn.title = '';
                    generateBtn.style.opacity = '';
                    generateBtn.style.cursor = '';
                    if (aiHint) hide(aiHint);
                    setProfileMode('ai');
                }
            }
        } catch (e) {
            // Silently ignore — the 503 fallback on the generate click still works
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

        var typeBadge;
        var isReadOnly = p.source === 'yaml';
        if (p.source === 'yaml') {
            typeBadge = '<span class="badge badge-yaml">yaml</span>';
        } else {
            typeBadge = '<span class="badge badge-custom">custom</span>';
        }

        var actions = '<button class="btn btn-small btn-primary profile-use-btn" data-name="' + escapeHtml(name) + '">Use in New Session</button> ';
        actions += '<button class="btn btn-small btn-outline profile-duplicate-btn" data-name="' + escapeHtml(name) + '">Duplicate</button>';
        if (!isReadOnly) {
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
        // Use in New Session
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
                    var resp = await api('GET', '/api/v1/security-profiles/' + encodeURIComponent(name));
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
                    var resp = await api('GET', '/api/v1/security-profiles/' + encodeURIComponent(name));
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
                    var resp = await api('DELETE', '/api/v1/security-profiles/' + encodeURIComponent(name));
                    if (!resp.ok) {
                        var data = await resp.json().catch(function () { return {}; });
                        alert(data.error || data.message || 'Failed to delete profile.');
                        btn.disabled = false;
                    } else {
                        loadSecurityPage();
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
        setProfileMode(systemLLMConfigured ? 'ai' : 'manual');
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
            var resp = await api('POST', '/api/v1/security-profiles/build', { description: desc });
            if (resp.status === 503) {
                var errEl = $('#profile-ai-error');
                errEl.textContent = 'AI Builder requires a system LLM. Ask your administrator to configure system_llm in alcove.yaml.';
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
                resp = await api('PUT', '/api/v1/security-profiles/' + encodeURIComponent(editingProfileId), payload);
            } else {
                resp = await api('POST', '/api/v1/security-profiles', payload);
            }

            if (!resp.ok) {
                var data = await resp.json().catch(function () { return {}; });
                throw new Error(data.error || data.message || 'Failed to save profile.');
            }

            hide($('#profile-form-container'));
            $('#profile-form').reset();
            editingProfileId = null;
            loadSecurityPage();
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Save Profile';
        }
    });

    // Back to Security from tools admin
    $('#back-to-profiles').addEventListener('click', function () {
        navigate('security');
    });

    // Tools admin link from Security page
    $('#security-tools-admin-link').addEventListener('click', function (e) {
        e.preventDefault();
        navigate('tools-admin');
    });

    // ---------------------
    // Session Profile Selector
    // ---------------------
    async function loadTaskProfiles() {
        var select = $('#task-profile-add');
        select.innerHTML = '<option value="">+ Add profile...</option>';

        try {
            var resp = await api('GET', '/api/v1/security-profiles');
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = err.message;
                show(errEl);
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Add Tool';
        }
    });

    // ---------------------
    // Session Tools (New Session form)
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
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
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
    // System Info modal
    // ---------------------
    async function loadSystemInfo() {
        var el = $('#system-info-content');
        el.innerHTML = '<div class="loading-state"><div class="spinner"></div><p>Loading...</p></div>';
        try {
            var resp = await api('GET', '/api/v1/system-info');
            var info = await resp.json();

            var llmText = info.system_llm && info.system_llm.configured
                ? 'Configured (' + escapeHtml(info.system_llm.provider) + ')'
                : '<span style="color:var(--text-muted)">Not configured</span>';

            el.innerHTML = '<div class="session-meta-grid">' +
                '<div class="meta-card"><div class="meta-label">Version</div><div class="meta-value">' + escapeHtml(info.version || 'dev') + '</div></div>' +
                '<div class="meta-card"><div class="meta-label">Runtime</div><div class="meta-value">' + escapeHtml(info.runtime || '-') + '</div></div>' +
                '<div class="meta-card"><div class="meta-label">Auth Backend</div><div class="meta-value">' + escapeHtml(info.auth_backend || '-') + '</div></div>' +
                '<div class="meta-card"><div class="meta-label">System LLM</div><div class="meta-value">' + llmText + '</div></div>' +
                '</div>';
        } catch(err) {
            el.innerHTML = '<p style="color:var(--status-error)">Failed to load system info.</p>';
        }
    }

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

    function parseGoDuration(durationStr) {
        // Parse Go duration strings like "7m15.54144s", "2m21.385475s", "13m46.715466s"
        // Returns total seconds as integer
        if (!durationStr || typeof durationStr !== 'string') {
            return 0;
        }

        let totalSeconds = 0;

        // Extract hours (e.g., "1h23m45s" -> 1)
        const hoursMatch = durationStr.match(/(\d+(?:\.\d+)?)h/);
        if (hoursMatch) {
            totalSeconds += parseFloat(hoursMatch[1]) * 3600;
        }

        // Extract minutes (e.g., "7m15.54144s" -> 7)
        // Use word boundary to avoid matching "ms"
        const minutesMatch = durationStr.match(/(\d+(?:\.\d+)?)m(?!s)/);
        if (minutesMatch) {
            totalSeconds += parseFloat(minutesMatch[1]) * 60;
        }

        // Extract seconds (e.g., "7m15.54144s" -> 15.54144)
        // Make sure we don't match "ms" or "µs"/"us" or "ns"
        const secondsMatch = durationStr.match(/(\d+(?:\.\d+)?)s(?![aeiou])/);
        if (secondsMatch) {
            totalSeconds += parseFloat(secondsMatch[1]);
        }

        // Extract milliseconds (e.g., "500ms")
        const msMatch = durationStr.match(/(\d+(?:\.\d+)?)ms/);
        if (msMatch) {
            totalSeconds += parseFloat(msMatch[1]) / 1000;
        }

        // Extract microseconds (e.g., "500µs" or "500us")
        const usMatch = durationStr.match(/(\d+(?:\.\d+)?)[µu]s/);
        if (usMatch) {
            totalSeconds += parseFloat(usMatch[1]) / 1000000;
        }

        // Extract nanoseconds (e.g., "500ns")
        const nsMatch = durationStr.match(/(\d+(?:\.\d+)?)ns/);
        if (nsMatch) {
            totalSeconds += parseFloat(nsMatch[1]) / 1000000000;
        }

        // Round to whole seconds as requested
        return Math.round(totalSeconds);
    }

    function formatDuration(startedAt, finishedAt, durationField) {
        if (durationField) {
            if (typeof durationField === 'number') {
                return humanDuration(durationField);
            }
            // Parse Go duration string (e.g., "7m15.54144s" -> seconds)
            const seconds = parseGoDuration(String(durationField));
            return humanDuration(seconds);
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
        // Handle edge cases
        if (seconds < 1) return '<1s';
        if (seconds < 60) return seconds + 's';

        const m = Math.floor(seconds / 60);
        const s = seconds % 60;

        if (m < 60) {
            // Format as "7m 16s" with space between units
            return m + 'm ' + s + 's';
        }

        const h = Math.floor(m / 60);
        const rm = m % 60;

        // For durations over 1 hour: show "1h 23m" (drop seconds)
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
    // Session Prerequisites & Warnings
    // ---------------------
    function checkTaskPrerequisites() {
        var container = $('#task-warnings');
        if (!container) return;

        var warnings = [];

        var llmCreds = cachedCredentials.filter(function (c) {
            return c.provider !== 'github' && c.provider !== 'gitlab' && c.provider !== 'jira' && c.provider !== 'splunk' && c.provider !== 'generic';
        });
        var scmCreds = cachedCredentials.filter(function (c) {
            return c.provider === 'github' || c.provider === 'gitlab' || c.provider === 'jira' || c.provider === 'splunk';
        });
        var hasGithubCred = scmCreds.some(function (c) { return c.provider === 'github'; });
        var hasGitlabCred = scmCreds.some(function (c) { return c.provider === 'gitlab'; });
        var hasJiraCred = scmCreds.some(function (c) { return c.provider === 'jira'; });
        var hasSplunkCred = scmCreds.some(function (c) { return c.provider === 'splunk'; });

        // Warning: no LLM credential
        if (llmCreds.length === 0) {
            warnings.push({
                type: 'caution',
                text: 'No LLM provider configured. Your session won\'t be able to reach an AI model. <a href="#credentials">Add one on the Credentials page.</a>'
            });
        }

        // Warning: selected profile uses GitHub/GitLab/Jira/Splunk but no matching credential
        selectedProfiles.forEach(function (profileName) {
            var profile = allProfiles.find(function (p) { return p.name === profileName; });
            if (!profile || !profile.tools) return;
            var toolNames = Object.keys(profile.tools);
            var usesGithub = toolNames.some(function (t) { return t.toLowerCase().indexOf('github') !== -1; });
            var usesGitlab = toolNames.some(function (t) { return t.toLowerCase().indexOf('gitlab') !== -1; });
            var usesJira = toolNames.some(function (t) { return t.toLowerCase().indexOf('jira') !== -1; });
            var usesSplunk = toolNames.some(function (t) { return t.toLowerCase().indexOf('splunk') !== -1; });

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
            if (usesJira && !hasJiraCred) {
                warnings.push({
                    type: 'caution',
                    text: 'Profile "' + escapeHtml(profileName) + '" uses Jira, but no Jira credential is configured. <a href="#credentials">Add a Jira credential on the Credentials page.</a>'
                });
            }
            if (usesSplunk && !hasSplunkCred) {
                warnings.push({
                    type: 'caution',
                    text: 'Profile "' + escapeHtml(profileName) + '" uses Splunk, but no Splunk credential is configured. <a href="#credentials">Add a Splunk credential on the Credentials page.</a>'
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
                submitBtn.textContent = 'Start Session (LLM credential required)';
                submitBtn.title = 'Add an LLM credential on the Credentials page before starting sessions.';
            } else {
                submitBtn.disabled = false;
                submitBtn.textContent = 'Start Session';
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
            return '<div class="session-warning session-warning-' + w.type + '">' +
                '<span class="session-warning-icon">' + icon + '</span>' +
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
            container.querySelectorAll('.session-warning-prompt-hint').forEach(function (el) {
                el.remove();
            });

            var scmCreds = cachedCredentials.filter(function (c) {
                return c.provider === 'github' || c.provider === 'gitlab' || c.provider === 'jira' || c.provider === 'splunk';
            });
            var hasGithubCred = scmCreds.some(function (c) { return c.provider === 'github'; });
            var hasGitlabCred = scmCreds.some(function (c) { return c.provider === 'gitlab'; });
            var hasJiraCred = scmCreds.some(function (c) { return c.provider === 'jira'; });
            var hasSplunkCred = scmCreds.some(function (c) { return c.provider === 'splunk'; });
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
            var hasJiraProfile = selectedProfiles.some(function (pName) {
                var p = allProfiles.find(function (pp) { return pp.name === pName; });
                if (!p || !p.tools) return false;
                return Object.keys(p.tools).some(function (t) { return t.toLowerCase().indexOf('jira') !== -1; });
            });
            var hasSplunkProfile = selectedProfiles.some(function (pName) {
                var p = allProfiles.find(function (pp) { return pp.name === pName; });
                if (!p || !p.tools) return false;
                return Object.keys(p.tools).some(function (t) { return t.toLowerCase().indexOf('splunk') !== -1; });
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

            // Jira-related keywords
            if (/\b(jira|sprint|epic|story\s*point)\b/.test(text) && !hasJiraProfile) {
                var jiraMsg = 'Your prompt mentions Jira.';
                if (!hasJiraCred) {
                    jiraMsg += ' Consider <a href="#credentials">adding a Jira credential</a> and';
                } else {
                    jiraMsg += ' Consider';
                }
                jiraMsg += ' selecting a profile with Jira access.';
                suggestions.push(jiraMsg);
            }

            // Splunk-related keywords
            if (/\b(splunk|spl|search\s*head)\b/.test(text) && !hasSplunkProfile) {
                var splunkMsg = 'Your prompt mentions Splunk.';
                if (!hasSplunkCred) {
                    splunkMsg += ' Consider <a href="#credentials">adding a Splunk credential</a> and';
                } else {
                    splunkMsg += ' Consider';
                }
                splunkMsg += ' selecting a profile with Splunk access.';
                suggestions.push(splunkMsg);
            }

            // Clone/repo keywords without any SCM
            if (/\b(clone|repo|repository)\b/.test(text) && scmCreds.length === 0 && !hasGithubProfile && !hasGitlabProfile) {
                suggestions.push('Your prompt mentions repositories. Consider <a href="#credentials">adding SCM credentials</a> for code platform access.');
            }

            if (suggestions.length > 0) {
                show(container);
                suggestions.forEach(function (s) {
                    var div = document.createElement('div');
                    div.className = 'session-warning session-warning-info session-warning-prompt-hint';
                    div.innerHTML = '<span class="session-warning-icon">&#128161;</span><span>' + s + '</span>';
                    container.appendChild(div);
                });
            }
        }, 500);

        promptEl.addEventListener('input', analyzePrompt);
    })();

    // ---------------------
    // Account Page / TBR Associations
    // ---------------------
    async function loadAccountPage() {
        // Show account tab for both auth backends
        show($('#account-tab'));

        // Show TBR section only for rh-identity mode
        if (rhIdentityMode) {
            show($('#tbr-associations-section'));
            loadTBRAssociations();
        } else {
            hide($('#tbr-associations-section'));
        }

        // Show personal API tokens section only for postgres mode
        if (!rhIdentityMode) {
            show($('#personal-api-tokens-section'));
            loadPersonalAPITokens();
        } else {
            hide($('#personal-api-tokens-section'));
        }
    }

    async function loadTBRAssociations() {
        const tbody = $('#tbr-associations-tbody');
        const table = $('#tbr-associations-table');
        const empty = $('#tbr-associations-empty');

        try {
            const resp = await api('GET', '/api/v1/auth/tbr-associations');
            const data = await resp.json();
            const associations = data.associations || [];

            tbody.innerHTML = '';

            if (associations.length === 0) {
                show(empty);
                hide(table);
                return;
            }

            hide(empty);
            show(table);

            associations.forEach(assoc => {
                const row = document.createElement('tr');
                const createdDate = new Date(assoc.created_at).toLocaleDateString();
                const lastActive = formatRelativeTime(assoc.last_accessed_at);

                row.innerHTML = `
                    <td>${escapeHtml(assoc.tbr_org_id)}</td>
                    <td>${escapeHtml(assoc.tbr_username)}</td>
                    <td>${createdDate}</td>
                    <td>${lastActive}</td>
                    <td>
                        <button class="btn btn-small btn-outline btn-danger" onclick="deleteTBRAssociation('${assoc.id}')">
                            Remove
                        </button>
                    </td>
                `;
                tbody.appendChild(row);
            });

        } catch (error) {
            console.error('Error loading TBR associations:', error);
            tbody.innerHTML = '<tr><td colspan="5" class="error">Error loading associations</td></tr>';
            show(table);
            hide(empty);
        }
    }

    async function deleteTBRAssociation(id) {
        if (!confirm('Are you sure you want to remove this TBR identity association?')) {
            return;
        }

        try {
            const resp = await api('DELETE', `/api/v1/auth/tbr-associations/${id}`);
            if (resp.ok) {
                showTBRMessage('Association removed successfully.', 'success');
                loadTBRAssociations();
            } else {
                const error = await resp.json();
                showTBRMessage(error.error || 'Failed to remove association', 'error');
            }
        } catch (error) {
            console.error('Error deleting TBR association:', error);
            showTBRMessage('Error removing association', 'error');
        }
    }

    function showTBRMessage(message, type) {
        const errorEl = $('#tbr-association-error');
        const successEl = $('#tbr-association-success');

        hide(errorEl);
        hide(successEl);

        if (type === 'error') {
            errorEl.textContent = message;
            show(errorEl);
        } else {
            successEl.textContent = message;
            show(successEl);
        }

        // Hide message after 5 seconds
        setTimeout(() => {
            hide(errorEl);
            hide(successEl);
        }, 5000);
    }

    // TBR Association Form Handler
    const tbrForm = $('#tbr-association-form');
    if (tbrForm) {
        tbrForm.addEventListener('submit', async function(e) {
            e.preventDefault();

            const formData = new FormData(tbrForm);
            const data = {
                tbr_org_id: formData.get('tbr_org_id').trim(),
                tbr_username: formData.get('tbr_username').trim()
            };

            if (!data.tbr_org_id || !data.tbr_username) {
                showTBRMessage('Both Organization ID and TBR Username are required.', 'error');
                return;
            }

            try {
                const resp = await api('POST', '/api/v1/auth/tbr-associations', data);
                if (resp.ok) {
                    tbrForm.reset();
                    showTBRMessage('TBR identity association created successfully.', 'success');
                    loadTBRAssociations();
                } else {
                    const error = await resp.json();
                    showTBRMessage(error.error || 'Failed to create association', 'error');
                }
            } catch (error) {
                console.error('Error creating TBR association:', error);
                showTBRMessage('Error creating association', 'error');
            }
        });
    }

    // Make deleteTBRAssociation available globally for onclick handlers
    window.deleteTBRAssociation = deleteTBRAssociation;

    // ---------------------
    // Personal API Tokens
    // ---------------------
    async function loadPersonalAPITokens() {
        const tbody = $('#personal-api-tokens-tbody');
        const table = $('#personal-api-tokens-table');
        const empty = $('#personal-api-tokens-empty');

        try {
            const resp = await api('GET', '/api/v1/auth/api-tokens');
            const tokens = await resp.json();

            tbody.innerHTML = '';

            if (tokens.length === 0) {
                show(empty);
                hide(table);
            } else {
                hide(empty);
                show(table);

                tokens.forEach(token => {
                    const row = document.createElement('tr');
                    row.innerHTML = `
                        <td>${escapeHtml(token.name)}</td>
                        <td>${new Date(token.created_at).toLocaleDateString()}</td>
                        <td>${token.last_accessed_at ? new Date(token.last_accessed_at).toLocaleDateString() : 'Never'}</td>
                        <td>
                            <button onclick="deletePersonalAPIToken('${token.id}')" class="btn btn-small btn-outline btn-danger">Delete</button>
                        </td>
                    `;
                    tbody.appendChild(row);
                });
            }
        } catch (error) {
            console.error('Error loading personal API tokens:', error);
            showAPITokenMessage('Error loading tokens', 'error');
        }
    }

    async function deletePersonalAPIToken(tokenId) {
        if (!confirm('Are you sure you want to delete this token? This action cannot be undone.')) {
            return;
        }

        try {
            const resp = await api('DELETE', `/api/v1/auth/api-tokens/${tokenId}`);
            if (resp.ok) {
                showAPITokenMessage('Token deleted successfully', 'success');
                loadPersonalAPITokens();
            } else {
                const error = await resp.json();
                showAPITokenMessage(error.error || 'Failed to delete token', 'error');
            }
        } catch (error) {
            console.error('Error deleting API token:', error);
            showAPITokenMessage('Error deleting token', 'error');
        }
    }

    function showAPITokenMessage(message, type) {
        const errorEl = $('#personal-api-token-error');
        const successEl = $('#personal-api-token-success');

        if (type === 'error') {
            errorEl.textContent = message;
            show(errorEl);
            hide(successEl);
        } else {
            successEl.textContent = message;
            show(successEl);
            hide(errorEl);
        }

        // Auto-hide after 5 seconds
        setTimeout(() => {
            hide(errorEl);
            hide(successEl);
        }, 5000);
    }

    function showAPITokenDisplay(tokenData) {
        const modal = $('#api-token-display-modal');
        const tokenValueInput = $('#display-token-value');
        const tokenNameSpan = $('#display-token-name');
        const tokenExampleSpan = $('#display-token-example');

        tokenValueInput.value = tokenData.token;
        tokenNameSpan.textContent = tokenData.name;
        tokenExampleSpan.textContent = tokenData.token;

        show(modal);

        // Focus and select the token for easy copying
        tokenValueInput.focus();
        tokenValueInput.select();
    }

    function hideAPITokenDisplay() {
        const modal = $('#api-token-display-modal');
        const tokenValueInput = $('#display-token-value');

        hide(modal);
        tokenValueInput.value = ''; // Clear for security
    }

    // Personal API Token Form Handler
    const tokenForm = $('#personal-api-token-form');
    if (tokenForm) {
        tokenForm.addEventListener('submit', async function(e) {
            e.preventDefault();

            const formData = new FormData(tokenForm);
            const data = {
                name: formData.get('name').trim()
            };

            if (!data.name) {
                showAPITokenMessage('Token name is required.', 'error');
                return;
            }

            try {
                const resp = await api('POST', '/api/v1/auth/api-tokens', data);
                if (resp.ok) {
                    const tokenData = await resp.json();
                    tokenForm.reset();
                    showAPITokenMessage('Token created successfully.', 'success');
                    showAPITokenDisplay(tokenData);
                    loadPersonalAPITokens();
                } else {
                    const error = await resp.json();
                    showAPITokenMessage(error.error || 'Failed to create token', 'error');
                }
            } catch (error) {
                console.error('Error creating API token:', error);
                showAPITokenMessage('Error creating token', 'error');
            }
        });
    }

    // Token display modal handlers
    const copyTokenBtn = $('#copy-token-btn');
    const tokenDisplayClose = $('#api-token-display-close');

    if (copyTokenBtn) {
        copyTokenBtn.addEventListener('click', function() {
            const tokenInput = $('#display-token-value');
            tokenInput.select();
            document.execCommand('copy');

            // Visual feedback
            const originalText = copyTokenBtn.textContent;
            copyTokenBtn.textContent = 'Copied!';
            copyTokenBtn.classList.add('btn-success');
            setTimeout(() => {
                copyTokenBtn.textContent = originalText;
                copyTokenBtn.classList.remove('btn-success');
            }, 2000);
        });
    }

    if (tokenDisplayClose) {
        tokenDisplayClose.addEventListener('click', hideAPITokenDisplay);
    }

    // Make functions available globally for onclick handlers
    window.deletePersonalAPIToken = deletePersonalAPIToken;

    // ---------------------
    // System State (Maintenance Mode)
    // ---------------------
    var systemStateInterval = null;

    function checkSystemState() {
        api('GET', '/api/v1/admin/system-state')
            .then(function(resp) { return resp.json(); })
            .then(function(data) {
                var banner = document.getElementById('maintenance-banner');
                if (!banner) {
                    banner = document.createElement('div');
                    banner.id = 'maintenance-banner';
                    banner.className = 'maintenance-banner';
                    document.body.insertBefore(banner, document.body.firstChild);
                }
                if (data.mode === 'paused') {
                    banner.style.display = 'block';
                    banner.innerHTML = '<span class="maintenance-text">System paused for maintenance. New sessions will not be dispatched. ' + data.running_sessions + ' session(s) still running.</span>' +
                        (isAdmin() ? ' <button class="btn btn-sm" onclick="resumeSystem()">Resume</button>' : '');
                } else {
                    banner.style.display = 'none';
                }
            })
            .catch(function() {}); // silently fail for non-admins
    }

    function resumeSystem() {
        api('PUT', '/api/v1/admin/system-state', {mode: 'active'})
            .then(function() { checkSystemState(); })
            .catch(function(err) { alert('Failed to resume: ' + err); });
    }
    window.resumeSystem = resumeSystem;

    function startSystemStateCheck() {
        if (systemStateInterval) clearInterval(systemStateInterval);
        checkSystemState();
        systemStateInterval = setInterval(checkSystemState, 30000);
    }

    // ---------------------
    // Workflows
    // ---------------------

    async function loadWorkflowsPage() {
        var definitionsList = $('#workflow-definitions-list');
        var definitionsEmpty = $('#workflow-definitions-empty');
        var definitionsLoading = $('#workflow-definitions-loading');

        var runsList = $('#workflow-runs-list');
        var runsEmpty = $('#workflow-runs-empty');
        var runsLoading = $('#workflow-runs-loading');

        // Clear both sections
        definitionsList.innerHTML = '';
        runsList.innerHTML = '';
        hide(definitionsEmpty);
        hide(runsEmpty);
        show(definitionsLoading);
        show(runsLoading);

        try {
            // Fetch both workflows and workflow runs in parallel
            var results = await Promise.allSettled([
                api('GET', '/api/v1/workflows'),
                api('GET', '/api/v1/workflow-runs' + (($('#workflow-filter-status') && $('#workflow-filter-status').value) ? '?status=' + encodeURIComponent($('#workflow-filter-status').value) : ''))
            ]);

            // Process workflow definitions
            if (results[0].status === 'fulfilled' && results[0].value.ok) {
                var workflowsData = await results[0].value.json();
                var workflows = Array.isArray(workflowsData) ? workflowsData : (workflowsData.workflows || workflowsData.definitions || []);
                hide(definitionsLoading);

                if (workflows.length === 0) {
                    show(definitionsEmpty);
                } else {
                    workflows.forEach(function (workflow) {
                        renderWorkflowDefinitionCard(workflow, definitionsList);
                    });
                }
            } else {
                hide(definitionsLoading);
                definitionsList.innerHTML = '<div class="error-message">Failed to load workflow definitions.</div>';
            }

            // Process workflow runs
            if (results[1].status === 'fulfilled' && results[1].value.ok) {
                var runsData = await results[1].value.json();
                var runs = runsData.workflow_runs || [];
                hide(runsLoading);

                if (runs.length === 0) {
                    show(runsEmpty);
                } else {
                    runs.forEach(function (run) {
                        renderWorkflowRunCard(run, runsList);
                    });
                }
            } else {
                hide(runsLoading);
                runsList.innerHTML = '<div class="error-message">Failed to load workflow runs.</div>';
            }

        } catch (err) {
            hide(definitionsLoading);
            hide(runsLoading);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                definitionsList.innerHTML = '<div class="error-message">Failed to load workflow definitions.</div>';
                runsList.innerHTML = '<div class="error-message">Failed to load workflow runs.</div>';
            }
        }
    }

    function renderWorkflowDefinitionCard(workflow, container) {
        var card = document.createElement('div');
        card.className = 'workflow-def-card';

        // Convert workflow array to steps map for rendering
        var stepsMap = {};
        var stepsArray = workflow.workflow || [];
        if (Array.isArray(stepsArray)) {
            stepsArray.forEach(function(s) { if (s.id) stepsMap[s.id] = s; });
        }
        var stepCount = Object.keys(stepsMap).length;
        var dag = buildMiniDAG(stepsMap);
        var triggerInfo = buildTriggerInfo(workflow.trigger);
        var lastSynced = workflow.last_synced ? new Date(workflow.last_synced).toLocaleString() : 'Never';
        var sourceRepo = workflow.source_repo || 'Unknown';

        var syncError = '';
        if (workflow.sync_error) {
            syncError = '<div class="workflow-def-error">⚠️ Sync Error: ' + escapeHtml(workflow.sync_error) + '</div>';
        }

        card.innerHTML =
            '<div class="workflow-def-header">' +
                '<div>' +
                    '<div class="workflow-def-name">' + escapeHtml(workflow.name || 'Unnamed Workflow') + '</div>' +
                    '<div class="workflow-def-step-count">' + stepCount + ' step' + (stepCount === 1 ? '' : 's') + '</div>' +
                '</div>' +
                '<div class="workflow-def-actions">' +
                    '<button class="btn btn-small btn-outline" disabled>Trigger Manually</button>' +
                '</div>' +
            '</div>' +
            dag +
            '<div class="workflow-def-meta">' +
                '<span>📁 ' + escapeHtml(sourceRepo) + '</span>' +
                '<span>🔄 Last synced: ' + escapeHtml(lastSynced) + '</span>' +
                (triggerInfo ? '<span class="workflow-def-trigger-info">' + triggerInfo + '</span>' : '') +
            '</div>' +
            syncError;

        container.appendChild(card);
    }

    function workflowStatusBadgeClass(status) {
        if (status === 'awaiting_approval') return 'badge-cancelled';
        if (status === 'failed') return 'badge-error';
        if (status === 'max_iterations_exceeded') return 'badge-max_iterations_exceeded';
        return 'badge-' + status;
    }

    function workflowStatusLabel(status) {
        if (status === 'max_iterations_exceeded') return 'Max Iterations';
        if (status === 'awaiting_approval') return 'Awaiting Approval';
        return status;
    }

    function renderWorkflowRunCard(run, container) {
        var card = document.createElement('div');
        card.className = 'workflow-run-card';
        card.onclick = function () { navigate('workflow-run/' + run.id); };

        var statusClass = workflowStatusBadgeClass(run.status);
        var statusLabel = workflowStatusLabel(run.status);
        var startTime = run.started_at ? new Date(run.started_at).toLocaleString() : 'Not started';

        card.innerHTML =
            '<div class="workflow-run-header">' +
                '<span class="workflow-run-name">Run ' + escapeHtml(run.id.substring(0, 8)) + '</span>' +
                '<span class="badge ' + statusClass + '">' + escapeHtml(statusLabel) + '</span>' +
            '</div>' +
            '<div class="workflow-run-meta">' +
                '<span>Started: ' + escapeHtml(startTime) + '</span>' +
                (run.trigger_type ? '<span>Trigger: ' + escapeHtml(run.trigger_type) + '</span>' : '') +
                (run.current_step ? '<span>Current: ' + escapeHtml(run.current_step) + '</span>' : '') +
            '</div>';

        container.appendChild(card);
    }

    function buildMiniDAG(steps) {
        if (!steps || Object.keys(steps).length === 0) {
            return '<div class="workflow-dag"><span class="workflow-dag-step">No steps defined</span></div>';
        }

        // Build dependency graph
        var stepNames = Object.keys(steps);
        var dependencyGraph = {};
        var inDegree = {};

        stepNames.forEach(function(stepName) {
            dependencyGraph[stepName] = [];
            inDegree[stepName] = 0;
        });

        stepNames.forEach(function(stepName) {
            var step = steps[stepName];
            // Support both old 'needs' list and new 'depends' expression
            var needs = step.needs || [];
            if (typeof needs === 'string') needs = [needs];
            // If depends is set (expression string), extract step names from it
            if (!needs.length && step.depends) {
                var depMatches = step.depends.match(/\b([A-Za-z_][A-Za-z0-9_-]*)\./g);
                if (depMatches) {
                    needs = depMatches.map(function(m) { return m.replace('.', ''); });
                    // Deduplicate
                    needs = needs.filter(function(v, i, a) { return a.indexOf(v) === i; });
                }
            }
            needs.forEach(function(dependency) {
                if (dependencyGraph[dependency]) {
                    dependencyGraph[dependency].push(stepName);
                    inDegree[stepName]++;
                }
            });
        });

        // Topological sort to get execution order
        var executionOrder = [];
        var queue = [];

        stepNames.forEach(function(stepName) {
            if (inDegree[stepName] === 0) {
                queue.push(stepName);
            }
        });

        while (queue.length > 0) {
            var current = queue.shift();
            executionOrder.push(current);

            dependencyGraph[current].forEach(function(dependent) {
                inDegree[dependent]--;
                if (inDegree[dependent] === 0) {
                    queue.push(dependent);
                }
            });
        }

        // If there are remaining steps, add them (handles cycles)
        stepNames.forEach(function(stepName) {
            if (executionOrder.indexOf(stepName) === -1) {
                executionOrder.push(stepName);
            }
        });

        var dagHtml = '<div class="workflow-dag">';
        executionOrder.forEach(function(stepName, index) {
            var step = steps[stepName];
            var hasApproval = step.approval === 'required';
            var isBridge = step.type === 'bridge';
            var approvalIcon = hasApproval ? '<span class="approval-icon">🔒</span>' : '';
            var bridgeBadge = isBridge ? '<span class="bridge-badge">bridge</span>' : '';
            var stepTypeIcon = isBridge ? '<span class="step-type-icon-mini">&#9881;</span>' : '';

            // Credential indicator with tooltip
            var credBadge = '';
            if (step.credentials && Object.keys(step.credentials).length > 0) {
                var credParts = [];
                for (var envVar in step.credentials) {
                    credParts.push(escapeHtml(envVar) + '=' + escapeHtml(step.credentials[envVar]));
                }
                credBadge = '<span class="step-cred-icon" title="Credentials: ' + credParts.join(', ') + '">&#128273;</span>';
            }

            dagHtml += '<span class="workflow-dag-step' + (hasApproval ? ' has-approval' : '') + (isBridge ? ' dag-step-bridge' : '') + '">' +
                       stepTypeIcon + escapeHtml(stepName) + approvalIcon + bridgeBadge + credBadge + '</span>';

            if (index < executionOrder.length - 1) {
                dagHtml += '<span class="workflow-dag-arrow">→</span>';
            }
        });
        dagHtml += '</div>';

        return dagHtml;
    }

    function buildTriggerInfo(trigger) {
        if (!trigger) return '🔧 Manual only';

        var info = [];
        if (trigger.events && trigger.events.length > 0) {
            info.push('📡 Events: ' + trigger.events.join(', '));
        }
        if (trigger.labels && trigger.labels.length > 0) {
            info.push('🏷️ Labels: ' + trigger.labels.join(', '));
        }
        if (trigger.repos && trigger.repos.length > 0) {
            info.push('📦 Repos: ' + trigger.repos.join(', '));
        }
        if (trigger.schedule) {
            info.push('⏰ Schedule: ' + trigger.schedule);
        }

        return info.length > 0 ? info.join(' • ') : '🔧 Manual only';
    }

    // Attach filter change handler
    (function() {
        var filterEl = $('#workflow-filter-status');
        if (filterEl) {
            filterEl.addEventListener('change', function () {
                loadWorkflowsPage();
            });
        }
    })();

    async function loadWorkflowRunDetail(runId) {
        var meta = $('#workflow-meta');
        var stepsList = $('#workflow-steps-list');
        var title = $('#workflow-detail-title');

        meta.innerHTML = '';
        stepsList.innerHTML = '';
        title.textContent = 'Workflow Run';

        try {
            var resp = await api('GET', '/api/v1/workflow-runs/' + runId);
            var data = await resp.json();

            var run = data.workflow_run;
            var steps = data.steps || [];

            title.textContent = 'Workflow Run ' + run.id.substring(0, 8);

            // Build meta cards
            var statusClass = workflowStatusBadgeClass(run.status);
            var statusLabel = workflowStatusLabel(run.status);
            meta.innerHTML =
                '<div class="meta-card"><div class="meta-label">Status</div><div class="meta-value"><span class="badge ' + statusClass + '">' + escapeHtml(statusLabel) + '</span></div></div>' +
                '<div class="meta-card"><div class="meta-label">Started</div><div class="meta-value">' + (run.started_at ? new Date(run.started_at).toLocaleString() : 'Not started') + '</div></div>' +
                '<div class="meta-card"><div class="meta-label">Finished</div><div class="meta-value">' + (run.finished_at ? new Date(run.finished_at).toLocaleString() : '-') + '</div></div>' +
                '<div class="meta-card"><div class="meta-label">Trigger</div><div class="meta-value">' + escapeHtml(run.trigger_type || 'manual') + '</div></div>';

            // Build steps list with connectors
            steps.forEach(function (step, idx) {
                if (idx > 0) {
                    var connector = document.createElement('div');
                    connector.className = 'workflow-step-connector';
                    stepsList.appendChild(connector);
                }

                var isBridge = step.type === 'bridge';
                var isAgent = !isBridge;

                var item = document.createElement('div');
                item.className = 'workflow-step-item ' + (isBridge ? 'step-bridge' : 'step-agent');

                var dotClass = 'workflow-step-dot workflow-step-dot-' + step.status;
                var statusBadgeClass = workflowStatusBadgeClass(step.status);
                var stepStatusLabel = workflowStatusLabel(step.status);

                // Type icon: gear for bridge, circle for agent
                var typeIcon = isBridge
                    ? '<div class="workflow-step-type-icon" title="Bridge step">&#9881;</div>'
                    : '<div class="workflow-step-type-icon" title="Agent step">&#9679;</div>';

                var actionsHtml = '';
                if (step.status === 'awaiting_approval') {
                    actionsHtml =
                        '<div class="workflow-step-actions">' +
                            '<button class="btn btn-small btn-primary" onclick="window._approveStep(\'' + escapeHtml(runId) + '\',\'' + escapeHtml(step.step_id) + '\')">Approve</button>' +
                            '<button class="btn btn-small btn-outline" style="color:var(--status-error);border-color:var(--status-error);" onclick="window._rejectStep(\'' + escapeHtml(runId) + '\',\'' + escapeHtml(step.step_id) + '\')">Reject</button>' +
                        '</div>';
                }

                // Session link only for agent steps
                var sessionLink = '';
                if (isAgent && step.session_id) {
                    sessionLink = ' <a href="#session/' + escapeHtml(step.session_id) + '" class="trigger-link" onclick="event.stopPropagation()">View Session</a>';
                }

                // Action badge for bridge steps
                var actionBadge = '';
                if (isBridge && step.action) {
                    actionBadge = '<span class="workflow-step-action">' + escapeHtml(step.action) + '</span>';
                }

                // Iteration display (only for steps with max_iterations > 1)
                var iterationBadge = '';
                if (step.max_iterations && step.max_iterations > 1) {
                    var currentIteration = step.iteration || 0;
                    var isExceeded = step.status === 'max_iterations_exceeded';
                    iterationBadge = '<span class="workflow-step-iteration' + (isExceeded ? ' iteration-exceeded' : '') + '">' +
                        'Iteration ' + currentIteration + '/' + step.max_iterations +
                        (isExceeded ? ' — limit reached' : '') +
                        '</span>';
                }

                // Depends expression
                var dependsHtml = '';
                if (step.depends) {
                    dependsHtml = '<span class="workflow-step-depends">' + escapeHtml(step.depends) + '</span>';
                }

                // Step credentials
                var credentialsHtml = '';
                if (step.credentials && Object.keys(step.credentials).length > 0) {
                    var credParts = [];
                    for (var envVar in step.credentials) {
                        credParts.push(escapeHtml(envVar) + '=' + escapeHtml(step.credentials[envVar]));
                    }
                    credentialsHtml = '<div class="step-credentials">' +
                        '<span class="step-credentials-label">credentials:</span> ' +
                        credParts.join(', ') +
                        '</div>';
                }

                item.innerHTML =
                    '<div class="' + dotClass + '"></div>' +
                    typeIcon +
                    '<div class="workflow-step-info">' +
                        '<div class="workflow-step-name">' + escapeHtml(step.step_id) + actionBadge + iterationBadge + '</div>' +
                        '<div class="workflow-step-agent">' +
                            '<span class="badge ' + statusBadgeClass + '">' + escapeHtml(stepStatusLabel) + '</span>' +
                            sessionLink +
                        '</div>' +
                        dependsHtml +
                        credentialsHtml +
                    '</div>' +
                    actionsHtml;

                stepsList.appendChild(item);
            });

        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                stepsList.innerHTML = '<div class="error-message">Failed to load workflow run detail.</div>';
            }
        }
    }

    // Back button for workflow detail
    (function() {
        var backBtn = $('#back-to-workflows');
        if (backBtn) {
            backBtn.addEventListener('click', function () {
                navigate('workflows');
            });
        }
    })();

    // Expose approve/reject functions to window for inline onclick handlers
    window._approveStep = async function (runId, stepId) {
        try {
            var resp = await api('POST', '/api/v1/workflow-runs/' + runId + '/approve/' + stepId);
            if (resp.ok) {
                loadWorkflowRunDetail(runId);
            } else {
                var data = await resp.json();
                alert('Failed to approve step: ' + (data.error || 'Unknown error'));
            }
        } catch (err) {
            alert('Failed to approve step: ' + err.message);
        }
    };

    window._rejectStep = async function (runId, stepId) {
        if (!confirm('Are you sure you want to reject this step? The workflow will be marked as failed.')) return;
        try {
            var resp = await api('POST', '/api/v1/workflow-runs/' + runId + '/reject/' + stepId);
            if (resp.ok) {
                loadWorkflowRunDetail(runId);
            } else {
                var data = await resp.json();
                alert('Failed to reject step: ' + (data.error || 'Unknown error'));
            }
        } catch (err) {
            alert('Failed to reject step: ' + err.message);
        }
    };

    // ---------------------
    // Version Footer
    // ---------------------
    async function loadVersionFooter() {
        try {
            const resp = await fetch(basePath + '/api/v1/health');
            if (resp.ok) {
                const data = await resp.json();
                if (data.version) {
                    const versionText = $('#version-text');
                    const footer = $('#alcove-footer');
                    if (versionText && footer) {
                        versionText.textContent = data.version;
                        show(footer);
                    }
                }
            }
        } catch (e) {
            // Silently fail - footer is not critical
            console.debug('Failed to load version for footer:', e);
        }
    }

    // ---------------------
    // Teams
    // ---------------------

    async function loadTeams() {
        try {
            // Temporarily clear activeTeamId so this request is not scoped
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('GET', '/api/v1/teams');
            activeTeamId = savedTeamId;
            if (!resp.ok) return;
            var data = await resp.json();
            teamsList = data.teams || [];

            // Restore active team from localStorage or default to personal
            var savedId = localStorage.getItem('alcove_active_team');
            var found = teamsList.find(function(t) { return t.id === savedId; });
            if (found) {
                activeTeamId = found.id;
            } else {
                // Default to personal team
                var personal = teamsList.find(function(t) { return t.is_personal; });
                activeTeamId = personal ? personal.id : (teamsList.length > 0 ? teamsList[0].id : null);
            }
            if (activeTeamId) {
                localStorage.setItem('alcove_active_team', activeTeamId);
            }
            renderTeamSwitcher();
        } catch (err) {
            // Teams API may not be available yet; fail silently
            console.debug('Failed to load teams:', err);
        }
    }

    function getActiveTeamName() {
        if (!activeTeamId || teamsList.length === 0) return 'My Workspace';
        var team = teamsList.find(function(t) { return t.id === activeTeamId; });
        if (!team) return 'My Workspace';
        return team.is_personal ? 'My Workspace' : team.name;
    }

    function renderTeamSwitcher() {
        var nameEl = $('#active-team-name');
        if (nameEl) nameEl.textContent = getActiveTeamName();

        var listEl = $('#team-switcher-list');
        if (!listEl) return;

        var html = '';
        for (var i = 0; i < teamsList.length; i++) {
            var t = teamsList[i];
            var displayName = t.is_personal ? 'My Workspace' : escapeHtml(t.name);
            var isActive = t.id === activeTeamId;
            html += '<button class="team-switcher-item' + (isActive ? ' active' : '') + '" data-team-id="' + escapeHtml(t.id) + '">' + displayName + '</button>';
        }
        html += '<button class="team-switcher-manage" id="team-switcher-manage-btn">Manage Teams</button>';
        listEl.innerHTML = html;

        // Event listeners for team items
        listEl.querySelectorAll('.team-switcher-item').forEach(function(btn) {
            btn.addEventListener('click', function() {
                var teamId = btn.getAttribute('data-team-id');
                if (teamId !== activeTeamId) {
                    activeTeamId = teamId;
                    localStorage.setItem('alcove_active_team', teamId);
                    renderTeamSwitcher();
                    hide($('#team-switcher-menu'));
                    // Reload the current view with new team context
                    handleRoute();
                } else {
                    hide($('#team-switcher-menu'));
                }
            });
        });

        var manageBtn = $('#team-switcher-manage-btn');
        if (manageBtn) {
            manageBtn.addEventListener('click', function() {
                hide($('#team-switcher-menu'));
                navigate('teams');
            });
        }
    }

    // Team switcher toggle
    $('#team-switcher-toggle').addEventListener('click', function(e) {
        e.stopPropagation();
        var menu = $('#team-switcher-menu');
        menu.hidden = !menu.hidden;
        // Close user dropdown if open
        hide($('#user-dropdown-menu'));
    });

    // Prevent team menu clicks from closing via document handler
    $('#team-switcher-menu').addEventListener('click', function(e) {
        e.stopPropagation();
    });

    // Teams list page
    async function loadTeamsPage() {
        var listEl = $('#teams-list');
        var emptyEl = $('#teams-empty');
        var loadingEl = $('#teams-loading');

        show(loadingEl);
        hide(emptyEl);
        listEl.innerHTML = '';

        try {
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('GET', '/api/v1/teams');
            activeTeamId = savedTeamId;
            if (!resp.ok) {
                hide(loadingEl);
                listEl.innerHTML = '<p class="error-message">Failed to load teams.</p>';
                return;
            }
            var data = await resp.json();
            var teams = data.teams || [];
            hide(loadingEl);

            if (teams.length === 0) {
                show(emptyEl);
                return;
            }

            var html = '';
            for (var i = 0; i < teams.length; i++) {
                var t = teams[i];
                var displayName = t.is_personal ? 'My Workspace (Personal)' : escapeHtml(t.name);
                var badge = t.is_personal ? '<span class="team-card-badge">Personal</span>' : '';
                var isActive = t.id === activeTeamId;
                var activeIndicator = isActive ? '<span class="team-card-badge" style="background:rgba(46,204,113,0.15);color:var(--status-completed);">Active</span>' : '';
                var createdDate = t.created_at ? new Date(t.created_at).toLocaleDateString() : '';
                html += '<div class="team-card" data-team-id="' + escapeHtml(t.id) + '">';
                html += '<div class="team-card-header">';
                html += '<span class="team-card-name">' + displayName + '</span>';
                html += '<span style="display:flex;gap:6px;">' + activeIndicator + badge + '</span>';
                html += '</div>';
                if (createdDate) html += '<div class="team-card-meta">Created ' + escapeHtml(createdDate) + '</div>';
                html += '</div>';
            }
            listEl.innerHTML = html;

            // Click handlers for team cards
            listEl.querySelectorAll('.team-card').forEach(function(card) {
                card.addEventListener('click', function() {
                    var teamId = card.getAttribute('data-team-id');
                    navigate('team/' + teamId);
                });
            });
        } catch (err) {
            hide(loadingEl);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                listEl.innerHTML = '<p class="error-message">Failed to load teams.</p>';
            }
        }
    }

    // Create team
    $('#show-create-team').addEventListener('click', function() {
        show($('#create-team-form-container'));
        hide($('#create-team-error'));
        $('#new-team-name').value = '';
        $('#new-team-name').focus();
    });

    $('#cancel-create-team').addEventListener('click', function() {
        hide($('#create-team-form-container'));
    });

    $('#create-team-form').addEventListener('submit', async function(e) {
        e.preventDefault();
        var name = $('#new-team-name').value.trim();
        if (!name) return;

        var errEl = $('#create-team-error');
        hide(errEl);

        try {
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('POST', '/api/v1/teams', { name: name });
            activeTeamId = savedTeamId;
            if (!resp.ok) {
                var data = await resp.json().catch(function() { return {}; });
                errEl.textContent = data.error || data.message || 'Failed to create team.';
                show(errEl);
                return;
            }
            hide($('#create-team-form-container'));
            // Refresh teams list and switcher
            await loadTeams();
            loadTeamsPage();
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = 'Failed to create team.';
                show(errEl);
            }
        }
    });

    // Team detail page
    async function loadTeamDetail(teamId) {
        viewingTeamId = teamId;

        try {
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('GET', '/api/v1/teams/' + teamId);
            activeTeamId = savedTeamId;
            if (!resp.ok) {
                $('#team-detail-content').innerHTML = '<p class="error-message">Failed to load team details.</p>';
                return;
            }
            var team = await resp.json();

            // Set title
            var displayName = team.is_personal ? 'My Workspace' : team.name;
            $('#team-detail-title').textContent = displayName;

            // Team name editing
            var nameInput = $('#team-detail-name');
            nameInput.value = team.name;
            if (team.is_personal) {
                nameInput.disabled = true;
                $('#team-save-name').hidden = true;
            } else {
                nameInput.disabled = false;
                $('#team-save-name').hidden = false;
            }
            hide($('#team-name-error'));
            hide($('#team-name-success'));

            // Delete section
            var deleteSection = $('#team-delete-section');
            if (team.is_personal) {
                deleteSection.hidden = true;
            } else {
                deleteSection.hidden = false;
            }

            // Members
            renderTeamMembers(team.members || [], team.is_personal);

            // Add member section visibility
            var addMemberSection = $('#team-add-member-section');
            if (team.is_personal) {
                addMemberSection.hidden = true;
            } else {
                addMemberSection.hidden = false;
            }
            hide($('#team-member-error'));
            hide($('#team-member-success'));

        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                $('#team-detail-content').innerHTML = '<p class="error-message">Failed to load team details.</p>';
            }
        }
    }

    function renderTeamMembers(members, isPersonal) {
        var listEl = $('#team-members-list');
        if (!members || members.length === 0) {
            listEl.innerHTML = '<p style="color:var(--text-muted);font-size:13px;">No members.</p>';
            return;
        }
        var html = '';
        for (var i = 0; i < members.length; i++) {
            var m = members[i];
            var username = m.username || m;
            html += '<div class="team-member-item">';
            html += '<span class="team-member-name">' + escapeHtml(username) + '</span>';
            if (!isPersonal) {
                html += '<button class="team-member-remove" data-username="' + escapeHtml(username) + '" title="Remove member">x</button>';
            }
            html += '</div>';
        }
        listEl.innerHTML = html;

        // Remove member handlers
        if (!isPersonal) {
            listEl.querySelectorAll('.team-member-remove').forEach(function(btn) {
                btn.addEventListener('click', async function() {
                    var username = btn.getAttribute('data-username');
                    await removeTeamMember(viewingTeamId, username);
                });
            });
        }
    }

    // Save team name
    $('#team-save-name').addEventListener('click', async function() {
        var name = $('#team-detail-name').value.trim();
        if (!name) return;

        var errEl = $('#team-name-error');
        var successEl = $('#team-name-success');
        hide(errEl);
        hide(successEl);

        try {
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('PUT', '/api/v1/teams/' + viewingTeamId, { name: name });
            activeTeamId = savedTeamId;
            if (!resp.ok) {
                var data = await resp.json().catch(function() { return {}; });
                errEl.textContent = data.error || data.message || 'Failed to update team name.';
                show(errEl);
                return;
            }
            successEl.textContent = 'Team name updated.';
            show(successEl);
            $('#team-detail-title').textContent = name;
            // Refresh team list in switcher
            await loadTeams();
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = 'Failed to update team name.';
                show(errEl);
            }
        }
    });

    // Add team member
    $('#team-add-member-btn').addEventListener('click', async function() {
        var username = $('#team-add-member-username').value.trim();
        if (!username) return;

        var errEl = $('#team-member-error');
        var successEl = $('#team-member-success');
        hide(errEl);
        hide(successEl);

        try {
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('POST', '/api/v1/teams/' + viewingTeamId + '/members', { username: username });
            activeTeamId = savedTeamId;
            if (!resp.ok) {
                var data = await resp.json().catch(function() { return {}; });
                errEl.textContent = data.error || data.message || 'Failed to add member.';
                show(errEl);
                return;
            }
            $('#team-add-member-username').value = '';
            successEl.textContent = 'Member added.';
            show(successEl);
            // Reload team detail
            loadTeamDetail(viewingTeamId);
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = 'Failed to add member.';
                show(errEl);
            }
        }
    });

    async function removeTeamMember(teamId, username) {
        if (!confirm('Remove ' + username + ' from this team?')) return;

        try {
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('DELETE', '/api/v1/teams/' + teamId + '/members/' + encodeURIComponent(username));
            activeTeamId = savedTeamId;
            if (!resp.ok) {
                var data = await resp.json().catch(function() { return {}; });
                alert(data.error || data.message || 'Failed to remove member.');
                return;
            }
            loadTeamDetail(teamId);
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                alert('Failed to remove member.');
            }
        }
    }

    // Delete team
    $('#team-delete-btn').addEventListener('click', function() {
        var team = teamsList.find(function(t) { return t.id === viewingTeamId; });
        if (!team) return;
        $('#delete-team-name').textContent = team.name;
        hide($('#delete-team-error'));
        show($('#delete-team-modal'));
    });

    $('#cancel-delete-team').addEventListener('click', function() {
        hide($('#delete-team-modal'));
    });

    $('#delete-team-modal').addEventListener('click', function(e) {
        if (e.target === e.currentTarget) hide(e.currentTarget);
    });

    $('#confirm-delete-team').addEventListener('click', async function() {
        var errEl = $('#delete-team-error');
        hide(errEl);

        try {
            var savedTeamId = activeTeamId;
            activeTeamId = null;
            var resp = await api('DELETE', '/api/v1/teams/' + viewingTeamId);
            activeTeamId = savedTeamId;
            if (!resp.ok) {
                var data = await resp.json().catch(function() { return {}; });
                errEl.textContent = data.error || data.message || 'Failed to delete team.';
                show(errEl);
                return;
            }
            hide($('#delete-team-modal'));
            // If deleting the active team, switch to personal team
            if (viewingTeamId === activeTeamId) {
                localStorage.removeItem('alcove_active_team');
            }
            await loadTeams();
            navigate('teams');
        } catch (err) {
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                errEl.textContent = 'Failed to delete team.';
                show(errEl);
            }
        }
    });

    // Back to teams
    $('#back-to-teams').addEventListener('click', function() {
        navigate('teams');
    });

    // ---------------------
    // Catalog
    // ---------------------

    var catalogData = [];
    var catalogTeamState = {};
    var catalogCustomPlugins = [];
    var catalogCategoryFilter = '';
    var catalogSearchFilter = '';

    async function loadCatalogPage() {
        var grid = $('#catalog-grid');
        var loading = $('#catalog-loading');
        var empty = $('#catalog-empty');
        grid.innerHTML = '';
        show(loading);
        hide(empty);

        try {
            var results = await Promise.allSettled([
                api('GET', '/api/v1/catalog'),
                activeTeamId ? api('GET', '/api/v1/teams/' + activeTeamId + '/catalog') : Promise.resolve(null)
            ]);

            if (results[0].status === 'fulfilled' && results[0].value && results[0].value.ok) {
                var data = await results[0].value.json();
                catalogData = data.entries || [];
            }

            if (results[1].status === 'fulfilled' && results[1].value && results[1].value.ok) {
                var teamData = await results[1].value.json();
                // Build enabled map from entries
                catalogTeamState = {};
                (teamData.entries || []).forEach(function(e) {
                    catalogTeamState[e.id] = e.enabled;
                });
                catalogCustomPlugins = teamData.custom_plugins || [];
            } else {
                catalogTeamState = {};
                catalogCustomPlugins = [];
            }
        } catch (err) {
            if (err.message === 'unauthorized') { hide(loading); return; }
        }

        hide(loading);
        renderCatalogCategoryPills();
        renderCatalogGrid();
        renderCatalogCustomPlugins();
    }

    function renderCatalogCategoryPills() {
        var pillsEl = $('#catalog-category-pills');
        var categories = [];
        catalogData.forEach(function(e) {
            if (e.category && categories.indexOf(e.category) === -1) categories.push(e.category);
        });
        categories.sort();

        var html = '<button class="catalog-pill' + (!catalogCategoryFilter ? ' active' : '') + '" data-category="">All</button>';
        var categoryLabels = {
            'plugins': 'Plugins',
            'language-servers': 'Language Servers',
            'integrations': 'Integrations',
            'agent-templates': 'Agent Templates',
            'security': 'Security',
            'testing': 'Testing',
            'content': 'Content',
            'documentation': 'Documentation'
        };
        categories.forEach(function(cat) {
            var label = categoryLabels[cat] || cat;
            var active = catalogCategoryFilter === cat ? ' active' : '';
            html += '<button class="catalog-pill' + active + '" data-category="' + escapeHtml(cat) + '">' + escapeHtml(label) + '</button>';
        });
        pillsEl.innerHTML = html;

        pillsEl.querySelectorAll('.catalog-pill').forEach(function(pill) {
            pill.addEventListener('click', function() {
                catalogCategoryFilter = pill.getAttribute('data-category');
                renderCatalogCategoryPills();
                renderCatalogGrid();
            });
        });
    }

    function renderCatalogGrid() {
        var grid = $('#catalog-grid');
        var empty = $('#catalog-empty');

        var filtered = catalogData.filter(function(e) {
            if (catalogCategoryFilter && e.category !== catalogCategoryFilter) return false;
            if (catalogSearchFilter) {
                var q = catalogSearchFilter.toLowerCase();
                var searchable = (e.name + ' ' + e.description + ' ' + (e.tags || []).join(' ')).toLowerCase();
                if (searchable.indexOf(q) === -1) return false;
            }
            return true;
        });

        if (filtered.length === 0) {
            grid.innerHTML = '';
            show(empty);
            return;
        }
        hide(empty);

        var iconColors = {
            'plugins': '#3498db',
            'language-servers': '#2ecc71',
            'integrations': '#9b59b6',
            'agent-templates': '#e67e22',
            'security': '#e74c3c',
            'testing': '#1abc9c',
            'content': '#f39c12',
            'documentation': '#34495e'
        };

        var html = '';
        filtered.forEach(function(entry) {
            var enabled = catalogTeamState[entry.id] || false;
            var color = iconColors[entry.category] || '#607d8b';
            var letter = entry.name.charAt(0).toUpperCase();

            var tagsHtml = '';
            (entry.tags || []).forEach(function(tag) {
                tagsHtml += '<span class="catalog-card-tag">' + escapeHtml(tag) + '</span>';
            });

            html += '<div class="catalog-card">' +
                '<div class="catalog-card-icon" style="background:' + color + '">' + letter + '</div>' +
                '<div class="catalog-card-info">' +
                    '<div class="catalog-card-name">' + escapeHtml(entry.name) + '</div>' +
                    '<div class="catalog-card-desc">' + escapeHtml(entry.description) + '</div>' +
                    '<div class="catalog-card-tags">' + tagsHtml + '</div>' +
                '</div>' +
                '<div class="catalog-card-toggle">' +
                    '<label class="toggle-switch" title="' + (enabled ? 'Disable' : 'Enable') + '">' +
                        '<input type="checkbox" class="catalog-toggle" data-entry-id="' + escapeHtml(entry.id) + '"' + (enabled ? ' checked' : '') + '>' +
                        '<span class="toggle-slider"></span>' +
                    '</label>' +
                '</div>' +
            '</div>';
        });
        grid.innerHTML = html;

        // Wire toggle events
        grid.querySelectorAll('.catalog-toggle').forEach(function(cb) {
            cb.addEventListener('change', async function() {
                var entryId = cb.getAttribute('data-entry-id');
                var enabled = cb.checked;
                catalogTeamState[entryId] = enabled;
                try {
                    var resp = await api('PUT', '/api/v1/teams/' + activeTeamId + '/catalog/' + entryId, { enabled: enabled });
                    if (!resp.ok) {
                        cb.checked = !enabled;
                        catalogTeamState[entryId] = !enabled;
                    }
                } catch (e) {
                    cb.checked = !enabled;
                    catalogTeamState[entryId] = !enabled;
                }
            });
        });
    }

    function renderCatalogCustomPlugins() {
        var list = $('#catalog-custom-list');
        if (!catalogCustomPlugins || catalogCustomPlugins.length === 0) {
            list.innerHTML = '<p class="form-help" style="color:var(--text-muted);">No custom plugins configured.</p>';
            return;
        }
        var html = '';
        catalogCustomPlugins.forEach(function(plugin, idx) {
            html += '<div class="catalog-custom-item">' +
                '<span class="custom-url">' + escapeHtml(plugin.url || '') + '</span>' +
                '<span class="custom-ref">@' + escapeHtml(plugin.ref || 'main') + '</span>' +
                '<button class="btn btn-small btn-danger catalog-custom-remove" data-index="' + idx + '">Remove</button>' +
            '</div>';
        });
        list.innerHTML = html;

        list.querySelectorAll('.catalog-custom-remove').forEach(function(btn) {
            btn.addEventListener('click', async function() {
                var idx = parseInt(btn.getAttribute('data-index'));
                try {
                    var resp = await api('DELETE', '/api/v1/teams/' + activeTeamId + '/catalog/custom/' + idx);
                    if (resp.ok) {
                        var data = await resp.json();
                        catalogCustomPlugins = data.custom_plugins || [];
                        renderCatalogCustomPlugins();
                    }
                } catch (e) {}
            });
        });
    }

    // Search input handler
    $('#catalog-search').addEventListener('input', debounce(function() {
        catalogSearchFilter = $('#catalog-search').value;
        renderCatalogGrid();
    }, 300));

    // Custom plugin add handler
    $('#custom-plugin-add').addEventListener('click', async function() {
        var url = $('#custom-plugin-url').value.trim();
        var ref = $('#custom-plugin-ref').value.trim() || 'main';
        if (!url) return;
        try {
            var resp = await api('POST', '/api/v1/teams/' + activeTeamId + '/catalog/custom', { url: url, ref: ref });
            if (resp.ok) {
                var data = await resp.json();
                catalogCustomPlugins = data.custom_plugins || [];
                renderCatalogCustomPlugins();
                $('#custom-plugin-url').value = '';
                $('#custom-plugin-ref').value = 'main';
            }
        } catch (e) {}
    });

    // ---------------------
    // Init
    // ---------------------
    // Try to detect rh-identity mode by calling /api/v1/auth/me without a token.
    // If the backend is rh-identity, Turnpike will have set the X-RH-Identity header
    // and the middleware will authenticate automatically.
    (async function init() {
        if (!localStorage.getItem('alcove_token')) {
            try {
                const resp = await fetch(basePath + '/api/v1/auth/me', {
                    headers: { 'Content-Type': 'application/json' }
                });

                if (resp.ok) {
                    const data = await resp.json();
                    if (data.auth_backend === 'rh-identity') {
                        rhIdentityMode = true;

                        // Check for authentication errors
                        if (data.auth_error) {
                            showAuthError(data.auth_error_message || 'Authentication failed');
                            return;
                        }

                        if (data.username) {
                            localStorage.setItem('alcove_user', data.username);
                            localStorage.setItem('alcove_is_admin', data.is_admin ? 'true' : 'false');
                        }
                    }
                } else if (resp.status === 401) {
                    // Check if this is an rh-identity auth error
                    try {
                        const errorData = await resp.json();
                        // If we get a 401 with specific rh-identity error messages, show auth error
                        if (errorData.error === 'missing X-RH-Identity header' ||
                            errorData.error === 'invalid X-RH-Identity header' ||
                            errorData.error === 'TBR identity not associated with any user') {
                            rhIdentityMode = true;

                            // Map backend error messages to user-friendly messages
                            let userMessage;
                            if (errorData.error === 'missing X-RH-Identity header') {
                                userMessage = 'Authentication failed: no identity header received. Ensure you are accessing Alcove through the SSO proxy (Turnpike).';
                            } else if (errorData.error === 'invalid X-RH-Identity header') {
                                userMessage = 'Authentication failed: identity header is malformed. Contact your administrator.';
                            } else if (errorData.error === 'TBR identity not associated with any user') {
                                userMessage = 'Authentication failed: your Token Based Registry identity is not associated with an SSO account. Visit the Account page to create an association.';
                            } else {
                                userMessage = 'Authentication failed: ' + errorData.error;
                            }

                            showAuthError(userMessage);
                            return;
                        }
                    } catch (e) {
                        // If we can't parse the error response, fall through to normal login
                    }
                }
            } catch (e) {
                // Network error — fall through to normal login flow
            }
        }

        // Load version footer
        await loadVersionFooter();

        // Load teams for the team switcher (if logged in)
        if (isLoggedIn()) {
            await loadTeams();
        }

        handleRoute();
    })();
})();
