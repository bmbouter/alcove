#!/usr/bin/env bash
# test-fixture-sync.sh — Comprehensive test of agent repo sync with known fixtures.
#
# Syncs bmbouter/alcove-tests and verifies all agent definitions, security
# profiles, and workflows are parsed and stored correctly.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Internet access (clones from GitHub)
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-fixture-sync.sh

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
ALCOVE_TESTS_URL="https://github.com/bmbouter/alcove-tests.git"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup ---
log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); t=d.get('token',''); print(t) if t else sys.exit('Login failed')")

# Create test user
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"fixture-tester","password":"fixture123","is_admin":false}' > /dev/null 2>&1 || true

USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"fixture-tester","password":"fixture123"}' | \
  python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

TEAM_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" | \
  python3 -c "import json,sys; teams=json.load(sys.stdin).get('teams',[]); print(next((t['id'] for t in teams if t.get('is_personal')), ''))")

# =====================================================================
# Step 1: Configure and sync test repo
# =====================================================================
log "Step 1: Configure and sync alcove-tests repo"

curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d "{\"repos\":[{\"url\":\"$ALCOVE_TESTS_URL\",\"name\":\"alcove-tests\"}]}" > /dev/null

curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID" > /dev/null

# Wait for sync
for attempt in $(seq 1 10); do
  sleep 3
  COUNT=$(curl -s "$BRIDGE_URL/api/v1/agent-definitions" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "X-Alcove-Team: $TEAM_ID" | \
    python3 -c "import json,sys; print(len(json.load(sys.stdin).get('agent_definitions',[])))" 2>/dev/null || echo "0")
  if [ "$COUNT" -ge 5 ]; then break; fi
done

if [ "$COUNT" -ge 5 ]; then
  pass "Synced $COUNT agent definitions"
else
  fail "Only $COUNT agent definitions synced (expected >= 5)"
fi

# =====================================================================
# Test 2: Verify specific agent definitions
# =====================================================================
log "Test 2: Verify agent definition fields"

DEFS=$(curl -s "$BRIDGE_URL/api/v1/agent-definitions" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID")

echo "$DEFS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
defs = {d['name']: d for d in data.get('agent_definitions', [])}
results = []

# Simple agent exists
if 'Test Simple Agent' in defs:
    results.append(('PASS', 'Test Simple Agent exists'))
else:
    results.append(('FAIL', 'Test Simple Agent NOT found'))

# Dev container agent has image
if 'Dev Container Test' in defs:
    dc = defs['Dev Container Test'].get('dev_container')
    if dc and dc.get('image') == 'docker.io/library/python:3.11-slim':
        results.append(('PASS', 'Dev Container Test has correct image'))
    else:
        results.append(('FAIL', f'Dev Container Test image wrong: {dc}'))
else:
    results.append(('FAIL', 'Dev Container Test NOT found'))

# No-dev-container agent has null dev_container
if 'Test Task' in defs:
    dc = defs['Test Task'].get('dev_container')
    if dc is None:
        results.append(('PASS', 'Test Task has null dev_container'))
    else:
        results.append(('FAIL', f'Test Task dev_container should be null: {dc}'))
else:
    results.append(('FAIL', 'Test Task NOT found'))

# Multi-repo agent has 2 repos
if 'Test Multi-Repo Agent' in defs:
    repos = defs['Test Multi-Repo Agent'].get('repos', [])
    if len(repos) == 2:
        results.append(('PASS', 'Multi-repo agent has 2 repos'))
    else:
        results.append(('FAIL', f'Multi-repo agent has {len(repos)} repos (expected 2)'))
else:
    results.append(('FAIL', 'Test Multi-Repo Agent NOT found'))

# Unknown profile agent has sync_error
if 'Missing Profile Task' in defs:
    err = defs['Missing Profile Task'].get('sync_error', '')
    if 'unknown security profile' in err:
        results.append(('PASS', 'Missing Profile Task has expected sync_error'))
    else:
        results.append(('FAIL', f'Missing Profile Task sync_error: {err}'))
else:
    results.append(('FAIL', 'Missing Profile Task NOT found'))

# Direct outbound agent
if 'Test Direct Outbound Agent' in defs:
    results.append(('PASS', 'Direct Outbound Agent exists'))
else:
    results.append(('FAIL', 'Test Direct Outbound Agent NOT found'))

for status, msg in results:
    print(f'  {status}: {msg}')
" 2>&1 | while IFS= read -r line; do
  if echo "$line" | grep -q "PASS:"; then
    pass "$(echo "$line" | sed 's/.*PASS: //')"
  elif echo "$line" | grep -q "FAIL:"; then
    fail "$(echo "$line" | sed 's/.*FAIL: //')"
  fi
done

# =====================================================================
# Test 3: Verify security profiles
# =====================================================================
log "Test 3: Verify security profiles"

PROFILES=$(curl -s "$BRIDGE_URL/api/v1/security-profiles" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID")

YAML_PROFILE_COUNT=$(echo "$PROFILES" | python3 -c "
import json, sys
data = json.load(sys.stdin)
profiles = data.get('profiles', [])
yaml_profiles = [p for p in profiles if p.get('source') == 'yaml']
print(len(yaml_profiles))
")

if [ "$YAML_PROFILE_COUNT" -ge 2 ]; then
  pass "Found $YAML_PROFILE_COUNT YAML security profiles (expected >= 2)"
else
  fail "Found $YAML_PROFILE_COUNT YAML security profiles (expected >= 2)"
fi

# Check specific profiles
echo "$PROFILES" | python3 -c "
import json, sys
data = json.load(sys.stdin)
profiles = {p['name']: p for p in data.get('profiles', []) if p.get('source') == 'yaml'}
if 'testing-readonly' in profiles:
    print('  PASS: testing-readonly profile exists')
else:
    print('  FAIL: testing-readonly profile NOT found')
if 'testing-writer' in profiles:
    print('  PASS: testing-writer profile exists')
else:
    print('  FAIL: testing-writer profile NOT found')
" 2>&1 | while IFS= read -r line; do
  if echo "$line" | grep -q "PASS:"; then
    pass "$(echo "$line" | sed 's/.*PASS: //')"
  elif echo "$line" | grep -q "FAIL:"; then
    fail "$(echo "$line" | sed 's/.*FAIL: //')"
  fi
done

# =====================================================================
# Test 4: Verify workflows
# =====================================================================
log "Test 4: Verify workflows"

WORKFLOWS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID")

WF_COUNT=$(echo "$WORKFLOWS" | python3 -c "
import json, sys
print(len(json.load(sys.stdin).get('workflows', [])))
")

if [ "$WF_COUNT" -ge 2 ]; then
  pass "Found $WF_COUNT workflows (expected >= 2)"
else
  fail "Found $WF_COUNT workflows (expected >= 2)"
fi

# Check bridge steps workflow
echo "$WORKFLOWS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
wfs = {w['name']: w for w in data.get('workflows', [])}
if 'Test Bridge Steps Workflow' in wfs:
    wf = wfs['Test Bridge Steps Workflow']
    steps = wf.get('workflow', [])
    bridge_steps = [s for s in steps if s.get('type') == 'bridge']
    if len(bridge_steps) >= 2:
        print(f'  PASS: Bridge Steps Workflow has {len(bridge_steps)} bridge steps')
    else:
        print(f'  FAIL: Bridge Steps Workflow has {len(bridge_steps)} bridge steps (expected >= 2)')
else:
    print('  FAIL: Test Bridge Steps Workflow NOT found')
" 2>&1 | while IFS= read -r line; do
  if echo "$line" | grep -q "PASS:"; then
    pass "$(echo "$line" | sed 's/.*PASS: //')"
  elif echo "$line" | grep -q "FAIL:"; then
    fail "$(echo "$line" | sed 's/.*FAIL: //')"
  fi
done

# =====================================================================
# Cleanup
# =====================================================================
log "Cleanup..."
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"repos":[]}' > /dev/null
curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID" > /dev/null

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
