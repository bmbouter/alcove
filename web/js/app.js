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

    // ---------------------
    // State
    // ---------------------
    let refreshInterval = null;
    let durationInterval = null;
    let currentSessionId = null;
    let currentPage = 1;
    const perPage = 15;
    let selectedProfiles = [];
    let allProfiles = [];
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
        hide($('#unified-agents-loading'));
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
        var listEl = $('#unified-agents-list');
        var emptyEl = $('#unified-agents-empty');
        var loadingEl = $('#unified-agents-loading');

        listEl.innerHTML = '';
        hide(emptyEl);
        show(loadingEl);

        var allItems = [];

        // workflowMap: agent name -> list of workflow names that reference it
        var workflowMap = {};

        try {
            var results = await Promise.allSettled([
                api('GET', '/api/v1/agent-definitions'),
                api('GET', '/api/v1/schedules'),
                api('GET', '/api/v1/workflows')
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

            // Process workflows to build agent -> workflow membership map
            if (results[2].status === 'fulfilled' && results[2].value.ok) {
                var data3 = await results[2].value.json();
                var workflows = Array.isArray(data3) ? data3 : (data3.workflows || data3.definitions || []);
                workflows.forEach(function(wf) {
                    var wfName = wf.name || 'Unnamed Workflow';
                    var steps = wf.workflow || [];
                    steps.forEach(function(step) {
                        if (step.agent) {
                            // Agent references may be "source/item" or just "name"
                            var agentName = step.agent;
                            if (!workflowMap[agentName]) workflowMap[agentName] = [];
                            if (workflowMap[agentName].indexOf(wfName) === -1) {
                                workflowMap[agentName].push(wfName);
                            }
                        }
                    });
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
        var agentsTab = document.querySelector('.nav-tab[data-tab="agents"]');
        if (agentsTab) {
            agentsTab.textContent = pausedCount > 0 ? 'Agents (' + pausedCount + ' paused)' : 'Agents';
        }

        var html = '';
        for (var i = 0; i < allItems.length; i++) {
            var item = allItems[i];
            if (item._type === 'task-def') {
                html += renderTaskDefCard(item.data, workflowMap);
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
                    window.open(webUrl + '/blob/main/.alcove/agents/' + file, '_blank');
                } else {
                    showYaml(btn.closest('.agent-def-card').querySelector('.agent-def-run').getAttribute('data-id'));
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

    function renderTaskDefCard(d, workflowMap) {
        var name = d.name || 'Unnamed';
        var desc = d.description || '';
        var repo = d.source_repo || '';
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

        // Tags row: yaml tag, profiles, source repo
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

        // Target repos list
        if (d.repos && d.repos.length > 0) {
            html += '<div class="agent-def-repos" style="margin-top:4px;font-size:12px;color:var(--text-muted);">';
            html += '<span class="agent-def-label">Clones:</span> ';
            html += d.repos.map(function(r) {
                var url = r.url || r.URL || '';
                var ref = r.ref || '';
                var display = escapeHtml(url.replace(/^https?:\/\//, '').replace(/\.git$/, ''));
                if (ref) display += ' <span class="agent-def-dim">(' + escapeHtml(ref) + ')</span>';
                return display;
            }).join(', ');
            html += '</div>';
        }

        // --- Triggers section ---
        var hasTriggers = (d.schedule && d.schedule.cron) || (d.trigger && d.trigger.github);
        html += '<div class="agent-def-triggers">';

        if (!hasTriggers) {
            // Manual-only agent
            html += '<span class="trigger-pill trigger-pill-manual">Manual only</span>';
        }

        // Schedule trigger
        if (d.schedule && d.schedule.cron) {
            html += '<div class="trigger-block trigger-block-schedule">';
            html += '<span class="trigger-pill trigger-pill-schedule">';
            html += 'Schedule';
            if (!d.schedule.enabled) {
                html += ' <span class="trigger-pill-status trigger-pill-disabled">disabled</span>';
            }
            html += '</span>';
            html += '<div class="trigger-block-details">';
            var cronHuman = describeCron(d.schedule.cron);
            html += '<span class="trigger-detail-item"><code>' + escapeHtml(d.schedule.cron) + '</code>';
            if (cronHuman !== d.schedule.cron) {
                html += ' <span class="trigger-detail-human">' + escapeHtml(cronHuman) + '</span>';
            }
            html += '</span>';
            if (d.schedule.enabled && d.next_run) {
                html += '<span class="trigger-detail-item">Next: ' + formatRelativeTime(d.next_run) + '</span>';
            }
            if (d.last_run) {
                html += '<span class="trigger-detail-item">Last: ' + formatRelativeTime(d.last_run) + '</span>';
            }
            html += '</div>';
            html += '</div>';
        }

        // GitHub event trigger
        if (d.trigger && d.trigger.github) {
            var gh = d.trigger.github;
            html += '<div class="trigger-block trigger-block-github">';
            html += '<span class="trigger-pill trigger-pill-github">GitHub';
            var deliveryMode = gh.delivery_mode || 'polling';
            html += ' <span class="trigger-pill-mode">' + escapeHtml(deliveryMode) + '</span>';
            html += '</span>';
            html += '<div class="trigger-block-details">';

            // Events + actions
            if (gh.events && gh.events.length > 0) {
                var eventParts = gh.events.map(function(evt) {
                    return '<span class="trigger-event-badge">' + escapeHtml(evt) + '</span>';
                });
                html += '<div class="trigger-detail-row">';
                html += '<span class="trigger-detail-label">Events</span>';
                html += '<span class="trigger-detail-badges">' + eventParts.join(' ') + '</span>';
                html += '</div>';
            }

            if (gh.actions && gh.actions.length > 0) {
                var actionParts = gh.actions.map(function(act) {
                    return '<span class="trigger-action-badge">' + escapeHtml(act) + '</span>';
                });
                html += '<div class="trigger-detail-row">';
                html += '<span class="trigger-detail-label">Actions</span>';
                html += '<span class="trigger-detail-badges">' + actionParts.join(' ') + '</span>';
                html += '</div>';
            }

            // Labels
            if (gh.labels && gh.labels.length > 0) {
                var labelParts = gh.labels.map(function(lbl) {
                    return '<span class="trigger-label-badge">' + escapeHtml(lbl) + '</span>';
                });
                html += '<div class="trigger-detail-row">';
                html += '<span class="trigger-detail-label">Labels</span>';
                html += '<span class="trigger-detail-badges">' + labelParts.join(' ') + '</span>';
                html += '</div>';
            }

            // Repos filter
            if (gh.repos && gh.repos.length > 0) {
                html += '<div class="trigger-detail-row">';
                html += '<span class="trigger-detail-label">Repos</span>';
                html += '<span class="trigger-detail-value">' + gh.repos.map(function(r) { return escapeHtml(r); }).join(', ') + '</span>';
                html += '</div>';
            }

            // Branches filter
            if (gh.branches && gh.branches.length > 0) {
                html += '<div class="trigger-detail-row">';
                html += '<span class="trigger-detail-label">Branches</span>';
                html += '<span class="trigger-detail-value">' + gh.branches.map(function(b) { return escapeHtml(b); }).join(', ') + '</span>';
                html += '</div>';
            }

            // Users filter
            if (gh.users && gh.users.length > 0) {
                html += '<div class="trigger-detail-row">';
                html += '<span class="trigger-detail-label">Users</span>';
                html += '<span class="trigger-detail-value">' + gh.users.map(function(u) { return escapeHtml(u); }).join(', ') + '</span>';
                html += '</div>';
            }

            html += '</div>';
            html += '</div>';
        }

        html += '</div>'; // end agent-def-triggers

        // Dev container info
        if (d.dev_container && d.dev_container.image) {
            html += '<div class="agent-def-devcontainer">';
            html += '<span class="agent-def-label">Dev Container</span> ';
            html += '<code>' + escapeHtml(d.dev_container.image) + '</code>';
            if (d.dev_container.network_access) {
                html += ' <span class="agent-def-dim">(' + escapeHtml(d.dev_container.network_access) + ')</span>';
            }
            html += '</div>';
        }

        // Workflow membership
        if (workflowMap) {
            // Check both the raw name and potential source_key-based references
            var wfNames = (workflowMap[name] || []).slice();
            // Also check source_key style references (e.g., "source/agent-name")
            if (d.source_key) {
                var keyRef = d.source_key;
                if (workflowMap[keyRef] && workflowMap[keyRef].length > 0) {
                    workflowMap[keyRef].forEach(function(wn) {
                        if (wfNames.indexOf(wn) === -1) wfNames.push(wn);
                    });
                }
            }
            if (wfNames.length > 0) {
                html += '<div class="agent-def-workflow-membership">';
                html += '<span class="agent-def-label">Part of</span> ';
                html += wfNames.map(function(wn) {
                    return '<a href="#workflows" class="trigger-link">' + escapeHtml(wn) + '</a>';
                }).join(', ');
                html += '</div>';
            }
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
        if (s.repos && s.repos.length > 0) {
            var repoSummary = formatReposSummary(s.repos);
            tags.push('<span class="agent-def-tag agent-def-tag-repo">' + escapeHtml(repoSummary) + '</span>');
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

    // Sync Now button on Repos page
    $('#sync-now-btn').addEventListener('click', async function() {
        var btn = this;
        btn.disabled = true;
        btn.textContent = 'Syncing...';
        try {
            var resp = await api('POST', '/api/v1/agent-definitions/sync');
            var data = await resp.json().catch(function() { return {}; });
            if (!resp.ok || data.error) {
                btn.textContent = 'Sync Failed';
                setTimeout(function() { btn.textContent = 'Sync Now'; btn.disabled = false; }, 3000);
            } else {
                btn.textContent = 'Synced!';
                setTimeout(function() { btn.textContent = 'Sync Now'; btn.disabled = false; }, 2000);
                // Reload the current view to show updated data
                handleRoute();
            }
        } catch (err) {
            btn.textContent = 'Sync Failed';
            setTimeout(function() { btn.textContent = 'Sync Now'; btn.disabled = false; }, 3000);
        }
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
        const pages = ['sessions', 'task-new', 'agents', 'repos', 'catalog', 'credentials', 'security', 'tools', 'session-detail', 'users', 'account', 'workflows', 'workflow-detail', 'teams', 'team-detail'];
        pages.forEach((p) => hide($('#page-' + p)));

        // Update active nav tab
        var navRoute = route.startsWith('session/') ? 'sessions' : route;
        if (navRoute === 'tools' || navRoute === 'tools-admin') navRoute = 'security';
        if (navRoute === 'schedules') navRoute = 'agents';
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
        } else if (route === 'agents' || route === 'schedules') {
            if (route === 'schedules') { window.location.hash = '#agents'; return; }
            show($('#page-agents'));
            loadUnifiedSchedules();
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

    function formatReposSummary(repos) {
        if (!repos || repos.length === 0) return '';
        if (repos.length === 1) {
            var url = repos[0].url || repos[0].URL || '';
            return url.replace(/^https?:\/\//, '').replace(/\.git$/, '');
        }
        return repos.length + ' repos';
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

        var repoLabel = '';
        var repoSummary = formatReposSummary(s.repos);
        if (repoSummary) {
            repoLabel = ' <span style="color:var(--text-muted);font-size:12px;">' + escapeHtml(repoSummary) + '</span>';
        }

        return '<tr class="clickable session-row session-row-' + escapeHtml(status) + '" data-session-id="' + escapeHtml(s.id) + '" tabindex="0" role="link">' +
            '<td><span class="status-dot status-dot-' + escapeHtml(status) + '" title="' + escapeHtml(status) + '"></span></td>' +
            '<td><span class="agent-type-pill agent-type-' + escapeHtml(taskType.color) + '">' + escapeHtml(taskType.label) + '</span>' + escapeHtml(taskName) + repoLabel + '</td>' +
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

        var directOutbound = document.getElementById('direct-outbound-toggle');
        if (directOutbound && directOutbound.checked) {
            payload.direct_outbound = true;
        }

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
            if (document.getElementById('direct-outbound-toggle')) {
                document.getElementById('direct-outbound-toggle').checked = false;
            }
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

            // Render runtime config tab
            if (session.runtime_config) {
                renderRuntimeConfig(session.runtime_config);
            } else {
                $('#runtime-config-content').innerHTML = '<p class="form-help" style="text-align:center;padding:24px;">Runtime configuration not available for this session.</p>';
            }

            // Render environment tab
            if (session.env_snapshot) {
                renderEnvSnapshot(session.env_snapshot);
            } else {
                $('#env-snapshot-content').innerHTML = '<p class="form-help" style="text-align:center;padding:24px;">Environment snapshot not available for this session.</p>';
            }

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

        if (s.repos && s.repos.length > 0) {
            var repoNames = s.repos.map(function(r) {
                var url = r.url || r.URL || '';
                var name = r.name || r.Name || '';
                var display = url.replace(/^https?:\/\//, '').replace(/\.git$/, '');
                if (name) display = name + ' (' + display + ')';
                return display;
            });
            fields.push({ label: 'Repos', value: repoNames.join(', ') });
        }

        if (s.direct_outbound) {
            fields.push({ label: 'Network', value: 'Direct Outbound', badge: false });
        }

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

        // (Schedule This button removed — schedules are defined via YAML)

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

    function renderRuntimeConfig(config) {
        var el = $('#runtime-config-content');
        if (!config || typeof config !== 'object') {
            el.innerHTML = '<p class="form-help" style="text-align:center;padding:24px;">No runtime configuration data.</p>';
            return;
        }

        var html = '<div class="runtime-config-grid">';

        // Model & Network
        html += '<div class="rc-section"><h4>General</h4><table class="data-table"><tbody>';
        if (config.model) html += '<tr><td>Model</td><td>' + escapeHtml(config.model) + '</td></tr>';
        html += '<tr><td>Network</td><td>' + (config.direct_outbound ? '<span class="badge" style="background:rgba(231,76,60,0.2);color:#e74c3c;">Direct Outbound</span>' : '<span class="badge">Gate Proxied</span>') + '</td></tr>';
        html += '</tbody></table></div>';

        // Dev Container
        if (config.dev_container && config.dev_container.image) {
            html += '<div class="rc-section"><h4>Dev Container</h4><table class="data-table"><tbody>';
            html += '<tr><td>Image</td><td><code>' + escapeHtml(config.dev_container.image) + '</code></td></tr>';
            html += '</tbody></table></div>';
        }

        // Security Profiles
        if (config.profiles && config.profiles.length > 0) {
            html += '<div class="rc-section"><h4>Security Profiles</h4><div class="rc-badges">';
            config.profiles.forEach(function(p) { html += '<span class="badge">' + escapeHtml(p) + '</span> '; });
            html += '</div></div>';
        }

        // Scope
        if (config.scope && config.scope.services) {
            var services = Object.keys(config.scope.services);
            if (services.length > 0) {
                html += '<div class="rc-section"><h4>Scope</h4><table class="data-table"><thead><tr><th>Service</th><th>Operations</th></tr></thead><tbody>';
                services.forEach(function(svc) {
                    var ops = (config.scope.services[svc].operations || []).join(', ') || 'all';
                    html += '<tr><td>' + escapeHtml(svc) + '</td><td>' + escapeHtml(ops) + '</td></tr>';
                });
                html += '</tbody></table></div>';
            }
        }

        // Plugins
        if (config.plugins && config.plugins.length > 0) {
            html += '<div class="rc-section"><h4>Plugins (' + config.plugins.length + ')</h4><table class="data-table"><thead><tr><th>Name</th><th>Source</th></tr></thead><tbody>';
            config.plugins.forEach(function(p) {
                var source = p.source || p.Source || 'marketplace';
                html += '<tr><td>' + escapeHtml(p.name || p.Name || '') + '</td><td>' + escapeHtml(source) + '</td></tr>';
            });
            html += '</tbody></table></div>';
        }

        // Skill Repos
        if (config.skill_repos && config.skill_repos.length > 0) {
            html += '<div class="rc-section"><h4>Skill Repos (' + config.skill_repos.length + ')</h4><table class="data-table"><thead><tr><th>Name</th><th>URL</th><th>Ref</th></tr></thead><tbody>';
            config.skill_repos.forEach(function(r) {
                html += '<tr><td>' + escapeHtml(r.name || r.Name || '') + '</td><td>' + escapeHtml(r.url || r.URL || '') + '</td><td>' + escapeHtml(r.ref || r.Ref || 'main') + '</td></tr>';
            });
            html += '</tbody></table></div>';
        }

        // MCP Servers
        if (config.mcp_servers && Object.keys(config.mcp_servers).length > 0) {
            var servers = Object.keys(config.mcp_servers);
            html += '<div class="rc-section"><h4>MCP Servers (' + servers.length + ')</h4><table class="data-table"><thead><tr><th>Name</th><th>Command</th></tr></thead><tbody>';
            servers.forEach(function(name) {
                var srv = config.mcp_servers[name];
                var cmd = (srv.command || '') + ' ' + ((srv.args || []).join(' '));
                html += '<tr><td>' + escapeHtml(name) + '</td><td><code>' + escapeHtml(cmd.trim()) + '</code></td></tr>';
            });
            html += '</tbody></table></div>';
        }

        // Credentials
        if (config.credentials && config.credentials.length > 0) {
            html += '<div class="rc-section"><h4>Credentials (' + config.credentials.length + ')</h4><table class="data-table"><thead><tr><th>Env Var</th><th>Provider</th><th>Type</th></tr></thead><tbody>';
            config.credentials.forEach(function(c) {
                var cls = c.classification || 'unknown';
                var badge = cls === 'dummy' ? '<span class="badge" style="background:rgba(241,196,15,0.2);color:#f1c40f;">DUMMY</span>' :
                            cls === 'real' ? '<span class="badge" style="background:rgba(46,204,113,0.2);color:#2ecc71;">REAL</span>' :
                            '<span class="badge">' + escapeHtml(cls) + '</span>';
                html += '<tr><td><code>' + escapeHtml(c.env_var || '') + '</code></td><td>' + escapeHtml(c.provider || '') + '</td><td>' + badge + '</td></tr>';
            });
            html += '</tbody></table></div>';
        }

        html += '</div>';
        el.innerHTML = html;
    }

    function renderEnvSnapshot(snapshot) {
        var el = $('#env-snapshot-content');
        if (!snapshot) {
            el.innerHTML = '<p class="form-help" style="text-align:center;padding:24px;">No environment data.</p>';
            return;
        }

        var lines = snapshot.split('\n');
        var html = '<div style="padding:16px;">';
        html += '<div style="margin-bottom:12px;color:var(--text-secondary);font-size:0.875rem;">' + lines.length + ' environment variable' + (lines.length !== 1 ? 's' : '') + ' captured at session startup. Sensitive values are redacted.</div>';
        html += '<pre style="background:var(--bg-secondary);border:1px solid var(--border);border-radius:6px;padding:16px;overflow-x:auto;font-size:0.8125rem;line-height:1.6;margin:0;white-space:pre-wrap;word-break:break-all;">';

        lines.forEach(function(line) {
            var eqIdx = line.indexOf('=');
            if (eqIdx < 0) {
                html += escapeHtml(line) + '\n';
                return;
            }
            var key = line.substring(0, eqIdx);
            var value = line.substring(eqIdx + 1);

            html += '<span style="color:var(--accent);">' + escapeHtml(key) + '</span>=';
            if (value === '[REDACTED]') {
                html += '<span style="color:#e74c3c;font-style:italic;">[REDACTED]</span>';
            } else if (value === '[DUMMY]') {
                html += '<span style="color:#f1c40f;font-style:italic;">[DUMMY]</span>';
            } else {
                html += escapeHtml(value);
            }
            html += '\n';
        });

        html += '</pre></div>';
        el.innerHTML = html;
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
            hide($('#detail-transcript'));
            hide($('#detail-proxy-log'));
            hide($('#detail-runtime-config'));
            hide($('#detail-environment'));
            if (target === 'transcript') {
                show($('#detail-transcript'));
            } else if (target === 'proxy-log') {
                show($('#detail-proxy-log'));
                // Re-load proxy log if content is missing or empty
                const proxyTbody = $('#proxy-log-tbody');
                if (!proxyTbody.innerHTML.trim() || proxyTbody.querySelector('td[colspan]')) {
                    loadProxyLog(currentSessionId);
                }
            } else if (target === 'runtime-config') {
                show($('#detail-runtime-config'));
            } else if (target === 'environment') {
                show($('#detail-environment'));
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
            if (isExecutableTranscript(events)) {
                renderExecutableTranscript(content, events);
            } else {
                for (var i = 0; i < events.length; i++) {
                    appendTranscriptEvent(content, events[i]);
                }
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

    function isExecutableTranscript(events) {
        if (!events || events.length < 3) return false;
        var execCount = 0;
        for (var i = 0; i < events.length; i++) {
            var ev = events[i];
            var type = ev.type || ev.role || 'system';
            if (type === 'text' && ev.source === 'executable') {
                execCount++;
            } else if (type === 'system' || type === 'result') {
                continue;
            }
        }
        return execCount > 3;
    }

    function renderExecutableTranscript(container, events) {
        var lines = [];
        for (var i = 0; i < events.length; i++) {
            var ev = events[i];
            var type = ev.type || ev.role || 'system';
            if (type === 'system' || type === 'result') {
                if (lines.length > 0) {
                    flushExecutableBlock(container, lines);
                    lines = [];
                }
                appendTranscriptEvent(container, ev);
                continue;
            }
            if (type === 'text' && ev.source === 'executable') {
                lines.push({content: ev.content || '', stream: ev.stream || ''});
            }
        }
        if (lines.length > 0) {
            flushExecutableBlock(container, lines);
        }
    }

    function flushExecutableBlock(container, lines) {
        var div = document.createElement('div');
        div.className = 'tx-exec-block';
        var html = '';
        for (var i = 0; i < lines.length; i++) {
            html += renderExecutableLine(lines[i]);
        }
        div.innerHTML = html;
        container.appendChild(div);
    }

    function renderExecutableLine(lineObj) {
        var line = typeof lineObj === 'string' ? lineObj : lineObj.content;
        var stream = typeof lineObj === 'string' ? '' : (lineObj.stream || '');
        var escaped = escapeHtml(line);

        // Section headers: === Category Name ===
        if (/^=== .+ ===$/.test(line)) {
            var name = line.replace(/^=== /, '').replace(/ ===$/, '');
            return '<div class="tx-exec-section">' + escapeHtml(name) + '</div>';
        }

        // Empty lines
        if (line.trim() === '') {
            return '<div class="tx-exec-blank"></div>';
        }

        // Sub-section headers: "  Plugins (requested: 2):" or "  Credentials (3):"
        if (/^\s{2}\w[\w\s]*\(.*\):\s*$/.test(line)) {
            return '<div class="tx-exec-subheader">' + escaped + '</div>';
        }

        // Apply annotation badges
        escaped = escaped.replace(/\[DUMMY\]/g, '<span class="tx-exec-badge tx-exec-dummy">DUMMY</span>');
        escaped = escaped.replace(/\[MASKED\]/g, '<span class="tx-exec-badge tx-exec-masked">MASKED</span>');
        escaped = escaped.replace(/\[GATE-PROXY\]/g, '<span class="tx-exec-badge tx-exec-gate">GATE-PROXY</span>');
        escaped = escaped.replace(/\[REAL\]/g, '<span class="tx-exec-badge tx-exec-real">REAL</span>');
        escaped = escaped.replace(/\[OK\]\s*/g, '<span class="tx-exec-ok">[OK]</span> ');
        escaped = escaped.replace(/\[MISS\]/g, '<span class="tx-exec-miss">[MISS]</span>');

        var cls = stream === 'stderr' ? 'tx-exec-line tx-exec-line-stderr' : 'tx-exec-line';
        return '<div class="' + cls + '">' + escaped + '</div>';
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

    // ---------------------
    // (Schedule form removed — YAML is the single source of truth)
    // ---------------------
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

        var actions = '<button class="btn btn-small btn-primary profile-use-btn" data-name="' + escapeHtml(name) + '">Use in New Session</button>';

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

    }

    // (Profile form removed — YAML is the single source of truth)

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

                var actions = '<button class="btn btn-small btn-outline view-tool-btn" data-id="' + escapeHtml(id) + '">View</button>';

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

        } catch (err) {
            hide(loading);
            if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;color:var(--status-error);">Failed to load tools.</td></tr>';
            }
        }
    }

    // (Tool form removed — read-only view only)

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
                const lastActive = formatRelativeTime(assoc.last_accessed_at) || 'Never';

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

        // Check for disabled/unknown agent references in sync_error
        var agentWarning = '';
        if (workflow.sync_error && /unknown|disabled|not found|not enabled/i.test(workflow.sync_error)) {
            // Extract step name from sync_error if possible (patterns like "step 'foo'" or "agent 'foo'")
            var stepMatch = workflow.sync_error.match(/(?:step|agent)\s+['"]([^'"]+)['"]/i);
            var stepRef = stepMatch ? stepMatch[1] : '';
            agentWarning = '<div class="workflow-agent-warning">' +
                '⚠ ' + (stepRef ? 'Step \'' + escapeHtml(stepRef) + '\' references a disabled agent' : 'A step references an unknown or disabled agent') +
                ' — <a href="#catalog" style="color:inherit;text-decoration:underline;">enable it in the catalog</a>' +
            '</div>';
        }

        card.innerHTML =
            '<div class="workflow-def-header">' +
                '<div>' +
                    '<div class="workflow-def-name">' + escapeHtml(workflow.name || 'Unnamed Workflow') + '</div>' +
                    '<div class="workflow-def-step-count">' + stepCount + ' step' + (stepCount === 1 ? '' : 's') + '</div>' +
                '</div>' +
                '<div class="workflow-def-actions">' +
                    '<button class="btn btn-small btn-outline workflow-trigger-btn" data-workflow-id="' + escapeHtml(workflow.id) + '">Trigger Manually</button>' +
                '</div>' +
            '</div>' +
            dag +
            '<div class="workflow-def-meta">' +
                '<span>📁 ' + escapeHtml(sourceRepo) + '</span>' +
                '<span>🔄 Last synced: ' + escapeHtml(lastSynced) + '</span>' +
                (triggerInfo ? '<span class="workflow-def-trigger-info">' + triggerInfo + '</span>' : '') +
            '</div>' +
            syncError +
            agentWarning;

        container.appendChild(card);

        // Attach trigger button handler
        var triggerBtn = card.querySelector('.workflow-trigger-btn');
        if (triggerBtn) {
            triggerBtn.addEventListener('click', async function() {
                var workflowId = triggerBtn.getAttribute('data-workflow-id');
                triggerBtn.disabled = true;
                triggerBtn.textContent = 'Triggering...';
                try {
                    var resp = await api('POST', '/api/v1/workflow-runs', { workflow_id: workflowId });
                    if (!resp.ok) {
                        var data = await resp.json().catch(function() { return {}; });
                        alert(data.error || data.message || 'Failed to trigger workflow.');
                    } else {
                        var run = await resp.json();
                        var runId = run.id || '';
                        // Navigate to the workflow run detail page
                        navigate('workflow-run/' + runId);
                        return;
                    }
                } catch (err) {
                    if (err.message !== 'unauthorized' && err.message !== 'rh-identity-auth-error') {
                        alert('Failed to trigger workflow.');
                    }
                }
                triggerBtn.textContent = 'Trigger Manually';
                triggerBtn.disabled = false;
            });
        }
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

            var directOutboundBadge = '';
            if (step.direct_outbound) {
                directOutboundBadge = '<span class="step-direct-outbound-icon" title="Direct outbound network access">&#127760;</span>';
            }

            dagHtml += '<span class="workflow-dag-step' + (hasApproval ? ' has-approval' : '') + (isBridge ? ' dag-step-bridge' : '') + '">' +
                       stepTypeIcon + escapeHtml(stepName) + approvalIcon + bridgeBadge + credBadge + directOutboundBadge + '</span>';

            if (index < executionOrder.length - 1) {
                dagHtml += '<span class="workflow-dag-arrow">→</span>';
            }
        });
        dagHtml += '</div>';

        return dagHtml;
    }

    function buildTriggerInfo(trigger) {
        if (!trigger) return '🔧 Manual';

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

        return info.length > 0 ? info.join(' • ') : '🔧 Manual';
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

                // Direct outbound network indicator
                var directOutboundHtml = '';
                if (step.direct_outbound) {
                    directOutboundHtml = '<div class="step-direct-outbound"><span title="Direct outbound network access">&#127760;</span> Direct outbound</div>';
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
                        directOutboundHtml +
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
                    // Navigate to sessions list — don't stay on a detail page from the old team
                    navigate('sessions');
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

    var catalogEntries = [];
    var catalogCustomPlugins = [];
    var catalogEnabledMap = {};       // entryId -> bool
    var catalogItemSlugs = {};        // entryId -> slug (lazy-loaded)
    var catalogActiveCategory = '';   // '' = all
    var catalogSearchQuery = '';

    var categoryLabels = {
        'code-quality': 'Code Quality',
        'security': 'Security',
        'frontend': 'Frontend & UI',
        'devops': 'DevOps & Infra',
        'testing': 'Testing & QA',
        'developer-tools': 'Developer Tools',
        'engineering': 'Engineering',
        'documentation': 'Documentation',
        'marketing': 'Marketing & Content',
        'specialized': 'Specialized'
    };

    var sourceLabels = {
        'claude-plugins-official': 'plugin',
        'agency-agents': 'agent',
        'mcp-server': 'mcp',
        'plugin-bundle': 'bundle'
    };

    async function loadCatalogPage() {
        var listEl = $('#catalog-sources-list');
        var loading = $('#catalog-loading');
        var empty = $('#catalog-empty');
        listEl.innerHTML = '';
        show(loading);
        hide(empty);

        try {
            if (!activeTeamId) {
                hide(loading);
                show(empty);
                return;
            }

            // Fetch catalog entries and team enablement in parallel
            var catalogResp = api('GET', '/api/v1/catalog');
            var teamResp = api('GET', '/api/v1/teams/' + activeTeamId + '/catalog');
            var customResp = catalogResp; // custom plugins come from team catalog

            var results = await Promise.all([catalogResp, teamResp]);

            // Parse catalog entries
            if (results[0] && results[0].ok) {
                var data = await results[0].json();
                catalogEntries = data.entries || [];
            } else {
                catalogEntries = [];
            }

            // Parse team enabled state
            catalogEnabledMap = {};
            catalogCustomPlugins = [];
            if (results[1] && results[1].ok) {
                var teamData = await results[1].json();
                var sources = teamData.sources || [];
                sources.forEach(function(src) {
                    catalogEnabledMap[src.source_id] = (src.enabled_items || 0) > 0;
                });
                catalogCustomPlugins = teamData.custom_plugins || [];
            }
        } catch (err) {
            if (err.message === 'unauthorized') { hide(loading); return; }
            catalogEntries = [];
            catalogEnabledMap = {};
            catalogCustomPlugins = [];
        }

        hide(loading);
        renderCatalogCategoryPills();
        renderCatalogGrid();
        renderCatalogCustomPlugins();
    }

    function renderCatalogCategoryPills() {
        var listEl = $('#catalog-sources-list');

        // Count entries per category
        var counts = {};
        catalogEntries.forEach(function(e) {
            counts[e.category] = (counts[e.category] || 0) + 1;
        });

        var html = '<div class="catalog-pills">';
        html += '<button class="catalog-pill' + (!catalogActiveCategory ? ' active' : '') + '" data-category="">All (' + catalogEntries.length + ')</button>';
        Object.keys(categoryLabels).forEach(function(cat) {
            if (!counts[cat]) return;
            html += '<button class="catalog-pill' + (catalogActiveCategory === cat ? ' active' : '') + '" data-category="' + escapeHtml(cat) + '">' +
                escapeHtml(categoryLabels[cat] || cat) + ' (' + counts[cat] + ')</button>';
        });
        html += '</div>';

        // Search bar
        html += '<div class="catalog-search-bar">' +
            '<input type="text" class="catalog-search" placeholder="Search catalog..." value="' + escapeHtml(catalogSearchQuery) + '">' +
        '</div>';

        html += '<div id="catalog-grid" class="catalog-grid"></div>';
        listEl.innerHTML = html;

        // Wire pills
        listEl.querySelectorAll('.catalog-pill').forEach(function(btn) {
            btn.addEventListener('click', function() {
                catalogActiveCategory = btn.getAttribute('data-category');
                listEl.querySelectorAll('.catalog-pill').forEach(function(p) { p.classList.remove('active'); });
                btn.classList.add('active');
                renderCatalogGrid();
            });
        });

        // Wire search
        var searchInput = listEl.querySelector('.catalog-search');
        if (searchInput) {
            searchInput.addEventListener('input', debounce(function() {
                catalogSearchQuery = searchInput.value;
                renderCatalogGrid();
            }, 200));
        }
    }

    function renderCatalogGrid() {
        var gridEl = document.getElementById('catalog-grid');
        var empty = $('#catalog-empty');
        if (!gridEl) return;

        var entries = catalogEntries.slice();

        // Filter by category
        if (catalogActiveCategory) {
            entries = entries.filter(function(e) { return e.category === catalogActiveCategory; });
        }

        // Filter by search
        if (catalogSearchQuery) {
            var q = catalogSearchQuery.toLowerCase();
            entries = entries.filter(function(e) {
                var searchable = (e.name + ' ' + (e.description || '') + ' ' + (e.tags || []).join(' ')).toLowerCase();
                return searchable.indexOf(q) !== -1;
            });
        }

        // Sort: enabled first, then alphabetical
        entries.sort(function(a, b) {
            var aEnabled = catalogEnabledMap[a.id] || false;
            var bEnabled = catalogEnabledMap[b.id] || false;
            if (aEnabled && !bEnabled) return -1;
            if (!aEnabled && bEnabled) return 1;
            return (a.name || '').localeCompare(b.name || '');
        });

        if (entries.length === 0) {
            gridEl.innerHTML = '<p class="form-help" style="color:var(--text-muted);padding:16px 0;">No matching catalog entries.</p>';
            hide(empty);
            return;
        }
        hide(empty);

        var html = '';
        entries.forEach(function(entry) {
            var enabled = catalogEnabledMap[entry.id] || false;
            var sourceLabel = sourceLabels[entry.source_type] || entry.source_type;
            var badgeClass = 'catalog-badge-' + (entry.category || '').replace(/\s+/g, '-');
            var catLabel = categoryLabels[entry.category] || entry.category || '';

            var tagsHtml = '<span class="catalog-card-tag ' + badgeClass + '">' + escapeHtml(catLabel) + '</span>';

            html += '<div class="catalog-card">' +
                '<div class="catalog-card-info">' +
                    '<div class="catalog-card-header">' +
                        '<span class="catalog-card-name">' + escapeHtml(entry.name) + '</span>' +
                        (entry.docs_url ? ' <a href="' + escapeHtml(entry.docs_url) + '" target="_blank" class="catalog-docs-link" title="Documentation">&#128279;</a>' : '') +
                    '</div>' +
                    '<div class="catalog-card-desc">' + escapeHtml(entry.description) + '</div>' +
                    '<div class="catalog-card-tags">' +
                        '<span class="catalog-card-tag source-' + escapeHtml(entry.source_type || 'unknown') + '">' + escapeHtml(sourceLabel) + '</span>' +
                        tagsHtml +
                    '</div>' +
                '</div>' +
                '<div class="catalog-card-toggle">' +
                    '<label class="toggle-switch" title="' + (enabled ? 'Disable' : 'Enable') + '">' +
                        '<input type="checkbox" class="catalog-toggle" data-entry-id="' + escapeHtml(entry.id) + '"' + (enabled ? ' checked' : '') + '>' +
                        '<span class="toggle-slider"></span>' +
                    '</label>' +
                '</div>' +
            '</div>';
        });
        gridEl.innerHTML = html;

        // Wire toggles
        gridEl.querySelectorAll('.catalog-toggle').forEach(function(cb) {
            cb.addEventListener('change', async function() {
                var entryId = cb.getAttribute('data-entry-id');
                var enabled = cb.checked;
                try {
                    var resp = await api('PUT', '/api/v1/teams/' + activeTeamId + '/catalog/' + encodeURIComponent(entryId), { enabled: enabled });
                    if (resp.ok) {
                        catalogEnabledMap[entryId] = enabled;
                    } else {
                        cb.checked = !enabled;
                    }
                } catch (e) {
                    cb.checked = !enabled;
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
