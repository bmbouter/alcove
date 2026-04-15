#!/usr/bin/env bash
# test-generic-secrets.sh — API tests for generic secrets (issue #276).
set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ""; echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# Setup
log "Setup"
USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD:-admin}\"}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")
if [ -z "$USER_TOKEN" ]; then echo "Login failed"; exit 1; fi

TEAM_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "import json,sys; t=[x for x in json.load(sys.stdin).get('teams',[]) if x.get('is_personal')]; print(t[0]['id'] if t else '')")
pass "Setup (team=$TEAM_ID)"

# Test 1: Create generic secret
log "Test 1: Create generic secret"
RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"name":"my-api-key","provider":"generic","auth_type":"secret","credential":"sk-test-123"}')
SECRET_ID=$(echo "$RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$SECRET_ID" != "ERROR" ]; then
  pass "Created generic secret (id=$SECRET_ID)"
else
  fail "Create failed: $RESP"
fi

# Test 2: Generic secret appears in list
log "Test 2: Generic secret in list"
LIST=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID")
HAS_SECRET=$(echo "$LIST" | python3 -c "
import json,sys
d=json.load(sys.stdin)
names=[c['name'] for c in d.get('credentials',[]) if c.get('provider')=='generic']
print('yes' if 'my-api-key' in names else 'no')
")
if [ "$HAS_SECRET" = "yes" ]; then
  pass "Generic secret appears in credential list"
else
  fail "Generic secret not found in list"
fi

# Test 3: Create LLM credential alongside generic (should work)
log "Test 3: LLM + generic coexistence"
LLM_RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"name":"My LLM","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-fake"}')
LLM_ID=$(echo "$LLM_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$LLM_ID" != "ERROR" ]; then
  pass "Created LLM credential alongside generic secret"
else
  fail "LLM create failed: $LLM_RESP"
fi

# Test 4: Second LLM should be rejected (one-per-team limit still works)
log "Test 4: One LLM per team limit"
LLM2_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"name":"Second LLM","provider":"google-vertex","auth_type":"service_account","credential":"{}","project_id":"p","region":"r"}')
if [ "$LLM2_CODE" = "409" ]; then
  pass "Second LLM rejected (HTTP 409)"
else
  fail "Second LLM returned $LLM2_CODE (expected 409)"
fi

# Test 5: Multiple generic secrets allowed
log "Test 5: Multiple generic secrets"
RESP2=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"name":"another-secret","provider":"generic","auth_type":"secret","credential":"token-456"}')
SECRET2_ID=$(echo "$RESP2" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$SECRET2_ID" != "ERROR" ]; then
  pass "Created second generic secret"
else
  fail "Second generic secret failed: $RESP2"
fi

GENERIC_COUNT=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "X-Alcove-Team: $TEAM_ID" \
  | python3 -c "import json,sys; print(len([c for c in json.load(sys.stdin).get('credentials',[]) if c.get('provider')=='generic']))")
if [ "$GENERIC_COUNT" = "2" ]; then
  pass "Two generic secrets coexist"
else
  fail "Generic count: $GENERIC_COUNT (expected 2)"
fi

# Test 6: Splunk credential alongside LLM (was broken, now fixed)
log "Test 6: Splunk + LLM coexistence (bug fix)"
SPLUNK_RESP=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_ID" \
  -d '{"name":"splunk","provider":"splunk","auth_type":"api_key","credential":"splunk-token-fake"}')
SPLUNK_ID=$(echo "$SPLUNK_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$SPLUNK_ID" != "ERROR" ]; then
  pass "Created Splunk credential alongside LLM (bug fixed)"
else
  fail "Splunk create failed: $SPLUNK_RESP"
fi

# Test 7: Delete generic secret
log "Test 7: Delete generic secret"
DEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
  "$BRIDGE_URL/api/v1/credentials/$SECRET_ID" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$DEL_CODE" = "200" ]; then
  pass "Deleted generic secret (HTTP 200)"
else
  fail "Delete returned $DEL_CODE"
fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$SECRET2_ID" -H "Authorization: Bearer $USER_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$LLM_ID" -H "Authorization: Bearer $USER_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$SPLUNK_ID" -H "Authorization: Bearer $USER_TOKEN" > /dev/null 2>&1

# Summary
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
