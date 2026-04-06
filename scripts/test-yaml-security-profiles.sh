#!/bin/bash
# test-yaml-security-profiles.sh — Tests for YAML security profiles synced from task repos.
#
# Verifies that YAML-defined security profiles are synced from task repos,
# are read-only (cannot be modified or deleted), and are cleaned up when
# repos are removed.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Internet access (tests clone repos from GitHub)
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-yaml-security-profiles.sh
#
# Tests:
#   Group 1: Add repo and sync — verify YAML profiles appear (5 tests)
#   Group 2: YAML profiles are read-only (2 tests)
#   Group 3: Task definitions from test repo have no sync_error (1 test)
#   Group 4: Cleanup — remove repo, verify profiles removed (3 tests)

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup ---
log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

ALCOVE_TESTING_URL="https://github.com/bmbouter/alcove-testing.git"

# Clear any existing repos first to start clean
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos":[]}' > /dev/null
curl -s -X POST "$BRIDGE_URL/api/v1/task-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null
sleep 3

# =====================================================================
# Group 1: Add repo and sync — verify YAML profiles appear
# =====================================================================
log "=== Group 1: Add repo and sync ==="

# Test 1.1: Add alcove-testing repo
log "Test 1.1: Add alcove-testing repo"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/user/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"repos\":[{\"url\":\"$ALCOVE_TESTING_URL\",\"name\":\"alcove-testing\"}]}")
if [ "$HTTP_CODE" = "200" ]; then
  pass "Added alcove-testing repo (200)"
else
  fail "Failed to add repo: $HTTP_CODE (expected 200)"
fi

# Test 1.2: Trigger sync
log "Test 1.2: Trigger sync"
SYNC_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/task-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$SYNC_CODE" = "200" ]; then
  pass "Sync triggered successfully (200)"
else
  fail "Sync returned $SYNC_CODE (expected 200)"
fi

# Test 1.3: Verify YAML-sourced profiles appear
log "Test 1.3: Verify YAML-sourced profiles appear"
YAML_COUNT=0
for attempt in 1 2 3 4 5; do
  sleep 3
  YAML_COUNT=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/security-profiles" | \
    python3 -c "
import json, sys
data = json.load(sys.stdin)
profiles = data.get('profiles', [])
yaml_profiles = [p for p in profiles if p.get('source') == 'yaml']
print(len(yaml_profiles))
")
  if [ "$YAML_COUNT" -ge 2 ]; then break; fi
done
if [ "$YAML_COUNT" -ge 2 ]; then
  pass "YAML-sourced profiles found (count=$YAML_COUNT)"
else
  fail "Expected at least 2 YAML profiles, got $YAML_COUNT"
fi

# Test 1.4: Verify testing-readonly profile exists
log "Test 1.4: Verify testing-readonly profile exists"
PROFILES_JSON=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/security-profiles")
HAS_READONLY=$(echo "$PROFILES_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
profiles = data.get('profiles', [])
names = [p['name'] for p in profiles if p.get('source') == 'yaml']
print('yes' if 'testing-readonly' in names else 'no')
")
if [ "$HAS_READONLY" = "yes" ]; then
  pass "testing-readonly profile exists with source=yaml"
else
  fail "testing-readonly profile not found among YAML profiles"
fi

# Test 1.5: Verify testing-writer profile exists with source_repo set
log "Test 1.5: Verify testing-writer profile exists with source_repo"
HAS_WRITER=$(echo "$PROFILES_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
profiles = data.get('profiles', [])
for p in profiles:
    if p.get('name') == 'testing-writer' and p.get('source') == 'yaml':
        if p.get('source_repo', ''):
            print('yes')
            sys.exit()
print('no')
")
if [ "$HAS_WRITER" = "yes" ]; then
  pass "testing-writer profile exists with source=yaml and source_repo set"
else
  fail "testing-writer profile not found or missing source_repo"
fi

# =====================================================================
# Group 2: YAML profiles are read-only
# =====================================================================
log "=== Group 2: YAML profiles are read-only ==="

# Test 2.1: PUT to update a YAML profile — expect failure (403)
log "Test 2.1: PUT testing-readonly — expect 403"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/security-profiles/testing-readonly" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"testing-readonly","display_name":"Modified","description":"Should not work","tools":{}}')
if [ "$HTTP_CODE" != "200" ]; then
  pass "PUT on YAML profile rejected (HTTP $HTTP_CODE)"
else
  fail "PUT on YAML profile returned 200 — should have been rejected"
fi

# Test 2.2: DELETE a YAML profile — expect failure (not 200)
log "Test 2.2: DELETE testing-readonly — expect failure"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/security-profiles/testing-readonly" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$HTTP_CODE" != "200" ]; then
  pass "DELETE on YAML profile rejected (HTTP $HTTP_CODE)"
else
  fail "DELETE on YAML profile returned 200 — should have been rejected"
fi

# =====================================================================
# Group 3: Task definitions from test repo have no sync_error
# =====================================================================
log "=== Group 3: Task definitions have no sync_error ==="

# Test 3.1: Verify synced task definitions do not have sync_error
log "Test 3.1: Task definitions from alcove-testing have no sync_error"
DEFS_JSON=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/task-definitions")
SYNC_ERRORS=$(echo "$DEFS_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
defs = data.get('task_definitions', [])
errors = [d['name'] for d in defs if d.get('sync_error', '')]
if errors:
    print('ERRORS: ' + ', '.join(errors))
else:
    print('NONE')
")
if [ "$SYNC_ERRORS" = "NONE" ]; then
  pass "No task definitions have sync_error"
else
  fail "Task definitions with sync_error: $SYNC_ERRORS"
fi

# =====================================================================
# Group 4: Cleanup — remove repo, verify profiles removed
# =====================================================================
log "=== Group 4: Cleanup ==="

# Test 4.1: Remove task repos
log "Test 4.1: Remove task repos"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/user/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos":[]}')
if [ "$HTTP_CODE" = "200" ]; then
  pass "Removed task repos (200)"
else
  fail "Failed to remove repos: $HTTP_CODE"
fi

# Test 4.2: Trigger sync after removing repos
log "Test 4.2: Trigger sync after cleanup"
SYNC_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/task-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$SYNC_CODE" = "200" ]; then
  pass "Cleanup sync triggered (200)"
else
  fail "Cleanup sync returned $SYNC_CODE (expected 200)"
fi

# Test 4.3: Verify YAML profiles are cleaned up
log "Test 4.3: Verify YAML profiles removed after cleanup"
YAML_COUNT=99
for attempt in 1 2 3 4 5; do
  sleep 3
  YAML_COUNT=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/security-profiles" | \
    python3 -c "
import json, sys
data = json.load(sys.stdin)
profiles = data.get('profiles', [])
yaml_profiles = [p for p in profiles if p.get('source') == 'yaml']
print(len(yaml_profiles))
")
  if [ "$YAML_COUNT" = "0" ]; then break; fi
done
if [ "$YAML_COUNT" = "0" ]; then
  pass "YAML profiles cleaned up (count=0)"
else
  fail "Expected 0 YAML profiles after cleanup, got $YAML_COUNT"
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
