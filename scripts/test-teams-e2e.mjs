/**
 * test-teams-e2e.mjs — End-to-end browser test for team scoping.
 *
 * Uses Playwright to load the actual dashboard, login, switch teams,
 * and verify that API requests include the correct X-Alcove-Team header
 * and that the UI shows different data per team.
 *
 * Prerequisites:
 *   - Bridge running at BRIDGE_URL (default http://localhost:8080)
 *   - AUTH_BACKEND=postgres
 *   - npx playwright install chromium
 *
 * Usage:
 *   node scripts/test-teams-e2e.mjs
 *   BRIDGE_URL=https://example.com node scripts/test-teams-e2e.mjs
 */

import { chromium } from 'playwright';

const BRIDGE_URL = process.env.BRIDGE_URL || 'http://localhost:8080';
const ADMIN_USER = process.env.ADMIN_USER || 'admin';
const ADMIN_PASS = process.env.ADMIN_PASS || 'admin';

let pass = 0;
let fail = 0;

function log(msg) { console.log(`\n=== ${msg} ===`); }
function ok(msg) { console.log(`  PASS: ${msg}`); pass++; }
function bad(msg) { console.log(`  FAIL: ${msg}`); fail++; }

async function apiCall(method, path, token, body, teamId) {
    const headers = { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` };
    if (teamId) headers['X-Alcove-Team'] = teamId;
    const opts = { method, headers };
    if (body) opts.body = JSON.stringify(body);
    const resp = await fetch(`${BRIDGE_URL}${path}`, opts);
    return resp.json();
}

async function run() {
    // ─── API Setup: create user, teams, and credentials ──────────────────
    log('API Setup');

    // Login as admin
    const adminResp = await apiCall('POST', '/api/v1/auth/login', '', { username: ADMIN_USER, password: ADMIN_PASS });
    const adminToken = adminResp.token;
    if (!adminToken) { bad('Admin login failed'); process.exit(1); }

    // Create test user
    await apiCall('POST', '/api/v1/users', adminToken, { username: 'e2e-user', password: 'e2e-pass-123', is_admin: false });
    const userResp = await apiCall('POST', '/api/v1/auth/login', '', { username: 'e2e-user', password: 'e2e-pass-123' });
    const userToken = userResp.token;
    if (!userToken) { bad('User login failed'); process.exit(1); }

    // Get personal team
    const teamsResp = await apiCall('GET', '/api/v1/teams', userToken);
    const personalTeam = teamsResp.teams.find(t => t.is_personal);
    if (!personalTeam) { bad('No personal team'); process.exit(1); }
    ok(`Personal team: ${personalTeam.id}`);

    // Create a shared team
    const sharedTeam = await apiCall('POST', '/api/v1/teams', userToken, { name: 'E2E Shared Team' });
    if (!sharedTeam.id) { bad('Create shared team failed'); process.exit(1); }
    ok(`Shared team: ${sharedTeam.id}`);

    // Add credential to personal team
    await apiCall('POST', '/api/v1/credentials', userToken,
        { name: 'Personal Cred', provider: 'anthropic', auth_type: 'api_key', credential: 'sk-fake-personal' },
        personalTeam.id);

    // Add credential to shared team
    await apiCall('POST', '/api/v1/credentials', userToken,
        { name: 'Shared Cred', provider: 'github', auth_type: 'api_key', credential: 'ghp-fake-shared' },
        sharedTeam.id);
    ok('Credentials created in both teams');

    // Verify via API
    const personalCreds = await apiCall('GET', '/api/v1/credentials', userToken, null, personalTeam.id);
    const sharedCreds = await apiCall('GET', '/api/v1/credentials', userToken, null, sharedTeam.id);
    if (personalCreds.credentials?.length === 1 && personalCreds.credentials[0].name === 'Personal Cred') {
        ok('API: personal team has 1 correct credential');
    } else {
        bad(`API: personal team expected 1 cred, got ${JSON.stringify(personalCreds.credentials?.map(c => c.name))}`);
    }
    if (sharedCreds.credentials?.length === 1 && sharedCreds.credentials[0].name === 'Shared Cred') {
        ok('API: shared team has 1 correct credential');
    } else {
        bad(`API: shared team expected 1 cred, got ${JSON.stringify(sharedCreds.credentials?.map(c => c.name))}`);
    }

    // ─── Browser test ────────────────────────────────────────────────────
    const browser = await chromium.launch({ headless: true });
    const context = await browser.newContext();
    const page = await context.newPage();

    // Capture all API requests and their headers
    const apiRequests = [];
    page.on('request', req => {
        const url = req.url();
        if (url.includes('/api/v1/')) {
            apiRequests.push({
                url: url.replace(BRIDGE_URL, ''),
                method: req.method(),
                teamHeader: req.headers()['x-alcove-team'] || null,
            });
        }
    });

    // Also capture responses for credential data verification
    const apiResponses = {};
    page.on('response', async resp => {
        const url = resp.url();
        if (url.includes('/api/v1/credentials') && resp.request().method() === 'GET') {
            try {
                const data = await resp.json();
                apiResponses[url] = data;
            } catch {}
        }
    });

    // ─── Login ───────────────────────────────────────────────────────────
    log('Browser Login');
    await page.goto(BRIDGE_URL);
    await page.waitForTimeout(1000);

    const usernameInput = await page.$('#login-username');
    const passwordInput = await page.$('#login-password');
    if (usernameInput && passwordInput) {
        await usernameInput.fill('e2e-user');
        await passwordInput.fill('e2e-pass-123');
        await page.click('#login-form button[type="submit"]');
        await page.waitForTimeout(2000);
        ok('Logged in as e2e-user');
    } else {
        bad('Login form not found');
        await browser.close();
        process.exit(1);
    }

    // ─── Check teams loaded ──────────────────────────────────────────────
    log('Team switcher');
    const teamSwitcher = await page.$('#active-team-name');
    const teamName = teamSwitcher ? await teamSwitcher.textContent() : '';
    if (teamName) {
        ok(`Team switcher shows: "${teamName}"`);
    } else {
        bad('Team switcher not found or empty');
    }

    // Get the list of teams by clicking the switcher
    await page.click('#team-switcher-toggle');
    await page.waitForTimeout(500);
    const teamButtons = await page.$$('.team-switcher-item');
    const teams = [];
    for (const btn of teamButtons) {
        const name = await btn.textContent();
        const id = await btn.getAttribute('data-team-id');
        teams.push({ name: name.trim(), id });
    }
    // Close the menu
    await page.click('#team-switcher-toggle');
    await page.waitForTimeout(300);

    if (teams.length > 0) {
        ok(`Found ${teams.length} teams: ${teams.map(t => t.name).join(', ')}`);
    } else {
        bad('No teams found in switcher');
    }

    const personalTeamUI = teams.find(t => t.name === 'My Workspace');
    const otherTeamsUI = teams.filter(t => t.name !== 'My Workspace');

    // ─── Test credentials page with team switching ───────────────────────
    log('Credentials page — team scoping');

    // Navigate to credentials
    await page.click('a[href="#credentials"]');
    await page.waitForTimeout(2000);

    // Clear captured requests
    apiRequests.length = 0;

    // Record what credentials are shown for the current team
    const getCredentialNames = async () => {
        const rows = await page.$$('#credentials-tbody-llm tr, #credentials-tbody-scm tr');
        const names = [];
        for (const row of rows) {
            const nameCell = await row.$('td:first-child');
            if (nameCell) {
                const text = await nameCell.textContent();
                if (text.trim()) names.push(text.trim());
            }
        }
        return names;
    };

    const currentTeamCreds = await getCredentialNames();
    console.log(`  Current team credentials: ${JSON.stringify(currentTeamCreds)}`);

    // Check that the credentials API request had the team header
    const credReqs = apiRequests.filter(r => r.url.includes('/api/v1/credentials') && r.method === 'GET');
    if (credReqs.length > 0) {
        const lastReq = credReqs[credReqs.length - 1];
        if (lastReq.teamHeader) {
            ok(`Credentials request includes X-Alcove-Team: ${lastReq.teamHeader}`);
        } else {
            bad('Credentials request MISSING X-Alcove-Team header');
        }
    }

    // Switch to each team and verify the header and displayed data
    const credsByTeam = {};
    for (const team of teams) {
        apiRequests.length = 0;

        // Click team switcher
        await page.click('#team-switcher-toggle');
        await page.waitForTimeout(500);

        // Click the team button
        const btn = await page.$(`.team-switcher-item[data-team-id="${team.id}"]`);
        if (btn) {
            await btn.click();
            await page.waitForTimeout(2000);
        } else {
            bad(`Team button not found for ${team.name}`);
            continue;
        }

        // Check which team is now active
        const activeName = await page.$eval('#active-team-name', el => el.textContent);
        if (activeName.trim() === team.name) {
            ok(`Switched to "${team.name}"`);
        } else {
            bad(`Expected active team "${team.name}", got "${activeName.trim()}"`);
        }

        // Check the credentials API request header
        const teamCredReqs = apiRequests.filter(r =>
            r.url.includes('/api/v1/credentials') && r.method === 'GET'
        );
        if (teamCredReqs.length > 0) {
            const lastReq = teamCredReqs[teamCredReqs.length - 1];
            if (lastReq.teamHeader === team.id) {
                ok(`Credentials request for "${team.name}" has correct team header`);
            } else if (lastReq.teamHeader) {
                bad(`Credentials request for "${team.name}" has wrong team header: ${lastReq.teamHeader} (expected ${team.id})`);
            } else {
                bad(`Credentials request for "${team.name}" MISSING team header`);
            }
        }

        // Record credential names displayed in the UI
        const teamCreds = await getCredentialNames();
        credsByTeam[team.name] = teamCreds;
        console.log(`  "${team.name}" UI shows: ${JSON.stringify(teamCreds)}`);
    }

    // Verify credentials differ between teams
    if (Object.keys(credsByTeam).length >= 2) {
        const names = Object.keys(credsByTeam);
        const creds0 = JSON.stringify(credsByTeam[names[0]]);
        const creds1 = JSON.stringify(credsByTeam[names[1]]);
        if (creds0 !== creds1) {
            ok(`Different teams show different credentials`);
        } else {
            bad(`Both teams show same credentials: ${creds0} — TEAM SCOPING BROKEN`);
        }

        // Verify personal team shows 'Personal Cred'
        const myWs = credsByTeam['My Workspace'] || [];
        if (myWs.some(n => n.includes('Personal'))) {
            ok('My Workspace shows Personal Cred');
        } else {
            bad(`My Workspace credentials: ${JSON.stringify(myWs)} (expected Personal Cred)`);
        }

        // Verify shared team shows 'Shared Cred'
        const shared = credsByTeam['E2E Shared Team'] || [];
        if (shared.some(n => n.includes('Shared'))) {
            ok('E2E Shared Team shows Shared Cred');
        } else {
            bad(`E2E Shared Team credentials: ${JSON.stringify(shared)} (expected Shared Cred)`);
        }
    }

    // ─── Test sessions page with team switching ──────────────────────────
    log('Sessions page — team scoping');

    await page.click('a[href="#sessions"]');
    await page.waitForTimeout(2000);

    for (const team of teams) {
        apiRequests.length = 0;

        await page.click('#team-switcher-toggle');
        await page.waitForTimeout(300);
        const btn = await page.$(`.team-switcher-item[data-team-id="${team.id}"]`);
        if (btn) {
            await btn.click();
            await page.waitForTimeout(2000);
        }

        const sessionReqs = apiRequests.filter(r =>
            r.url.includes('/api/v1/sessions') && r.method === 'GET'
        );
        if (sessionReqs.length > 0) {
            const lastReq = sessionReqs[sessionReqs.length - 1];
            if (lastReq.teamHeader === team.id) {
                ok(`Sessions request for "${team.name}" has correct team header`);
            } else if (lastReq.teamHeader) {
                bad(`Sessions request for "${team.name}" has wrong header: ${lastReq.teamHeader}`);
            } else {
                bad(`Sessions request for "${team.name}" MISSING team header`);
            }
        } else {
            bad(`No sessions API request after switching to "${team.name}"`);
        }
    }

    // ─── Test schedules page with team switching ─────────────────────────
    log('Schedules page — team scoping');

    await page.click('a[href="#schedules"]');
    await page.waitForTimeout(2000);

    for (const team of teams) {
        apiRequests.length = 0;

        await page.click('#team-switcher-toggle');
        await page.waitForTimeout(300);
        const btn = await page.$(`.team-switcher-item[data-team-id="${team.id}"]`);
        if (btn) {
            await btn.click();
            await page.waitForTimeout(2000);
        }

        const schedReqs = apiRequests.filter(r =>
            (r.url.includes('/api/v1/schedules') || r.url.includes('/api/v1/agent-definitions')) && r.method === 'GET'
        );
        if (schedReqs.length > 0) {
            const allCorrect = schedReqs.every(r => r.teamHeader === team.id);
            if (allCorrect) {
                ok(`Schedules/agent-defs requests for "${team.name}" have correct team header`);
            } else {
                const wrong = schedReqs.find(r => r.teamHeader !== team.id);
                bad(`Schedules request for "${team.name}" has wrong header: ${wrong.teamHeader} (expected ${team.id})`);
            }
        } else {
            bad(`No schedules API requests after switching to "${team.name}"`);
        }
    }

    // ─── Test workflows page ─────────────────────────────────────────────
    log('Workflows page — team scoping');

    await page.click('a[href="#workflows"]');
    await page.waitForTimeout(2000);

    for (const team of teams) {
        apiRequests.length = 0;

        await page.click('#team-switcher-toggle');
        await page.waitForTimeout(300);
        const btn = await page.$(`.team-switcher-item[data-team-id="${team.id}"]`);
        if (btn) {
            await btn.click();
            await page.waitForTimeout(2000);
        }

        const wfReqs = apiRequests.filter(r =>
            r.url.includes('/api/v1/workflow') && r.method === 'GET'
        );
        if (wfReqs.length > 0) {
            const allCorrect = wfReqs.every(r => r.teamHeader === team.id);
            if (allCorrect) {
                ok(`Workflow requests for "${team.name}" have correct team header`);
            } else {
                const wrong = wfReqs.find(r => r.teamHeader !== team.id);
                bad(`Workflow request for "${team.name}" has wrong header: ${wrong?.teamHeader}`);
            }
        } else {
            bad(`No workflow API requests after switching to "${team.name}"`);
        }
    }

    // ─── Summary ─────────────────────────────────────────────────────────
    await browser.close();

    console.log(`\n=== RESULTS ===`);
    console.log(`  ${pass} passed, ${fail} failed`);
    if (fail > 0) {
        console.log('  SOME TESTS FAILED');
        process.exit(1);
    } else {
        console.log('  ALL TESTS PASSED');
    }
}

run().catch(err => {
    console.error('Fatal error:', err);
    process.exit(1);
});
