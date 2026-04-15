#!/usr/bin/env bash
# test-catalog.sh — API integration tests for the Catalog feature.
#
# Tests: catalog listing, team toggle, custom plugins, team scoping.
#
# Prerequisites: Bridge running at BRIDGE_URL with AUTH_BACKEND=postgres
# Usage: ADMIN_PASSWORD=<pw> bash scripts/test-catalog.sh

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ""; echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# --- Setup ---
log "Setup"
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")
if [ -z "$ADMIN_TOKEN" ]; then echo "Login failed"; exit 1; fi

# Create test user
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"catalog-tester","password":"cattest123","is_admin":false}' > /dev/null 2>&1 || true

USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"catalog-tester","password":"cattest123"}' \
  | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

# Get personal team
PERSONAL_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "import json,sys; d=json.load(sys.stdin); t=[x for x in d.get('teams',[]) if x.get('is_personal')]; print(t[0]['id'] if t else '')")

# Create a shared team for isolation tests
SHARED=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"catalog-test-team"}')
SHARED_ID=$(echo "$SHARED" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id',''))")
pass "Setup complete (personal=$PERSONAL_ID shared=$SHARED_ID)"

# =====================================================================
# Test 1: GET /api/v1/catalog returns entries
# =====================================================================
log "Test 1: Global catalog listing"

CATALOG=$(curl -s "$BRIDGE_URL/api/v1/catalog" -H "Authorization: Bearer $USER_TOKEN")
COUNT=$(echo "$CATALOG" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('entries',[])))")

if [ "$COUNT" -gt "0" ]; then
  pass "Catalog has $COUNT entries"
else
  fail "Catalog returned 0 entries"
fi

# Verify entries have required fields
VALID=$(echo "$CATALOG" | python3 -c "
import json,sys
d=json.load(sys.stdin)
for e in d.get('entries',[]):
    if not e.get('id') or not e.get('name') or not e.get('category'):
        print('invalid')
        sys.exit()
print('valid')
")
if [ "$VALID" = "valid" ]; then
  pass "All entries have required fields"
else
  fail "Some entries missing required fields"
fi

# =====================================================================
# Test 2: GET /teams/{id}/catalog — fresh team has nothing enabled
# =====================================================================
log "Test 2: Fresh team catalog state"

TEAM_CATALOG=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
ENABLED_COUNT=$(echo "$TEAM_CATALOG" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(sum(1 for e in d.get('entries',[]) if e.get('enabled')))
")

if [ "$ENABLED_COUNT" = "0" ]; then
  pass "Fresh team has 0 enabled entries"
else
  fail "Fresh team has $ENABLED_COUNT enabled (expected 0)"
fi

CUSTOM_COUNT=$(echo "$TEAM_CATALOG" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('custom_plugins') or []))
")
if [ "$CUSTOM_COUNT" = "0" ]; then
  pass "Fresh team has 0 custom plugins"
else
  fail "Fresh team has $CUSTOM_COUNT custom plugins (expected 0)"
fi

# =====================================================================
# Test 3: PUT /teams/{id}/catalog/{entryId} — enable an entry
# =====================================================================
log "Test 3: Toggle catalog entry"

TOGGLE_RESP=$(curl -s -X PUT "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/code-review" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true}')
UPDATED=$(echo "$TOGGLE_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('updated',False))")

if [ "$UPDATED" = "True" ]; then
  pass "Enabled code-review"
else
  fail "Toggle failed: $TOGGLE_RESP"
fi

# Verify it's now enabled
ENABLED=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "
import json,sys
d=json.load(sys.stdin)
for e in d.get('entries',[]):
    if e.get('id') == 'code-review':
        print('yes' if e.get('enabled') else 'no')
        break
")
if [ "$ENABLED" = "yes" ]; then
  pass "code-review shows as enabled"
else
  fail "code-review not enabled after toggle"
fi

# Disable it
curl -s -X PUT "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/code-review" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":false}' > /dev/null

DISABLED=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "
import json,sys
d=json.load(sys.stdin)
for e in d.get('entries',[]):
    if e.get('id') == 'code-review':
        print('yes' if e.get('enabled') else 'no')
        break
")
if [ "$DISABLED" = "no" ]; then
  pass "code-review disabled after toggle off"
else
  fail "code-review still enabled after disable"
fi

# =====================================================================
# Test 4: Invalid entry ID returns 404
# =====================================================================
log "Test 4: Invalid entry ID"

INVALID_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT \
  "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/nonexistent-entry" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true}')

if [ "$INVALID_CODE" = "404" ]; then
  pass "Invalid entry ID returns 404"
else
  fail "Invalid entry ID returned $INVALID_CODE (expected 404)"
fi

# =====================================================================
# Test 5: Custom plugins CRUD
# =====================================================================
log "Test 5: Custom plugins"

# Add a custom plugin
ADD_RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/custom" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://github.com/test/custom-plugin.git","ref":"main"}')
ADDED=$(echo "$ADD_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('added',False))")

if [ "$ADDED" = "True" ]; then
  pass "Added custom plugin"
else
  fail "Add custom plugin failed: $ADD_RESP"
fi

# Verify it appears
CUSTOM=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "
import json,sys
d=json.load(sys.stdin)
plugins=d.get('custom_plugins') or []
print(len(plugins))
")
if [ "$CUSTOM" = "1" ]; then
  pass "Custom plugin appears in team catalog"
else
  fail "Custom plugin count: $CUSTOM (expected 1)"
fi

# Remove it
DEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
  "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/custom/0" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$DEL_CODE" = "200" ]; then
  pass "Removed custom plugin (HTTP 200)"
else
  fail "Remove returned $DEL_CODE (expected 200)"
fi

# Verify it's gone
CUSTOM2=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "
import json,sys
d=json.load(sys.stdin)
plugins=d.get('custom_plugins') or []
print(len(plugins))
")
if [ "$CUSTOM2" = "0" ]; then
  pass "Custom plugin removed"
else
  fail "Custom plugin still present after removal ($CUSTOM2)"
fi

# =====================================================================
# Test 6: Team isolation — different teams have independent state
# =====================================================================
log "Test 6: Team isolation"

# Enable code-review on shared team
curl -s -X PUT "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/code-review" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true}' > /dev/null

# Check personal team — should NOT have it enabled
PERSONAL_ENABLED=$(curl -s "$BRIDGE_URL/api/v1/teams/$PERSONAL_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "
import json,sys
d=json.load(sys.stdin)
for e in d.get('entries',[]):
    if e.get('id') == 'code-review':
        print('yes' if e.get('enabled') else 'no')
        break
")
if [ "$PERSONAL_ENABLED" = "no" ]; then
  pass "Personal team not affected by shared team toggle"
else
  fail "Personal team has code-review enabled (isolation broken)"
fi

# =====================================================================
# Test 7: Empty URL rejected for custom plugins
# =====================================================================
log "Test 7: Validation"

EMPTY_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/custom" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"","ref":"main"}')

if [ "$EMPTY_CODE" = "400" ]; then
  pass "Empty URL rejected (HTTP 400)"
else
  fail "Empty URL returned $EMPTY_CODE (expected 400)"
fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$SHARED_ID" \
  -H "Authorization: Bearer $USER_TOKEN" > /dev/null 2>&1

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
