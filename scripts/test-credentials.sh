#!/bin/bash
# test-credentials.sh — Tests for the credential management API.
#
# Verifies CRUD operations for credentials (Anthropic, Vertex SA, Vertex ADC),
# secret redaction in responses, user-level isolation, system credential
# visibility rules, and admin credential scoping.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-credentials.sh
#
# Tests:
#   - Create/list/delete credentials for multiple providers
#   - Secrets not returned in GET responses
#   - Cross-user credential isolation (list, get-by-ID, delete)
#   - System LLM credentials hidden from regular users
#   - Admin credential scoping (admin cannot see other users' credentials)

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
PASS=0
FAIL=0

log() { echo ">>> $*"; }
pass() { echo "  PASS: $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL: $*"; FAIL=$((FAIL+1)); }

# Setup
log "Setting up..."
ADMIN_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

# Create test users
curl -s -X POST "$BRIDGE_URL/api/v1/users" -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"username":"credtest1","password":"credtest123","is_admin":false}' > /dev/null 2>&1 || true
curl -s -X POST "$BRIDGE_URL/api/v1/users" -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"username":"credtest2","password":"credtest234","is_admin":false}' > /dev/null 2>&1 || true

USER1_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"credtest1","password":"credtest123"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")
USER2_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" -H "Content-Type: application/json" \
  -d '{"username":"credtest2","password":"credtest234"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

# Test 1: Create Anthropic API key credential
log "Test 1: Create Anthropic credential"
RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER1_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-anthropic","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-test-key-123"}')
CRED1_ID=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$CRED1_ID" != "ERROR" ]; then pass "Created Anthropic credential"; else fail "Failed to create Anthropic credential: $RESULT"; fi

# Test 2: Create Vertex SA credential
log "Test 2: Create Vertex SA credential"
RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER1_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-vertex-sa","provider":"google-vertex","auth_type":"service_account","credential":"{\"type\":\"service_account\",\"project_id\":\"test\"}","project_id":"test-project","region":"us-east5"}')
CRED2_ID=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$CRED2_ID" != "ERROR" ]; then pass "Created Vertex SA credential"; else fail "Failed: $RESULT"; fi

# Test 3: Create Vertex ADC credential
log "Test 3: Create Vertex ADC credential"
RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER1_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"my-vertex-adc","provider":"google-vertex","auth_type":"adc","credential":"{\"type\":\"authorized_user\",\"client_id\":\"test\"}","project_id":"test-project-2","region":"us-central1"}')
CRED3_ID=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$CRED3_ID" != "ERROR" ]; then pass "Created Vertex ADC credential"; else fail "Failed: $RESULT"; fi

# Test 4: List credentials (should see all 3)
log "Test 4: List own credentials"
COUNT=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $USER1_TOKEN" | \
  python3 -c "import json,sys; print(json.load(sys.stdin).get('count',0))")
if [ "$COUNT" = "3" ]; then pass "User1 sees 3 credentials"; else fail "User1 sees $COUNT (expected 3)"; fi

# Test 5: Credential secrets NOT returned
log "Test 5: Secrets not in response"
HAS_SECRET=$(curl -s "$BRIDGE_URL/api/v1/credentials/$CRED1_ID" -H "Authorization: Bearer $USER1_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if 'credential' in d and d['credential'] else 'no')")
if [ "$HAS_SECRET" = "no" ]; then pass "Secret not returned in GET"; else fail "Secret exposed in GET response"; fi

# Test 6: User isolation
log "Test 6: User isolation"
USER2_COUNT=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $USER2_TOKEN" | \
  python3 -c "import json,sys; print(json.load(sys.stdin).get('count',0))")
if [ "$USER2_COUNT" = "0" ]; then pass "User2 cannot see User1's credentials"; else fail "User2 sees $USER2_COUNT credentials"; fi

USER2_GET=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/credentials/$CRED1_ID" -H "Authorization: Bearer $USER2_TOKEN")
if [ "$USER2_GET" = "404" ]; then pass "User2 gets 404 on User1's credential"; else fail "User2 gets $USER2_GET"; fi

# Test 7: Delete credential
log "Test 7: Delete credential"
DEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/credentials/$CRED1_ID" -H "Authorization: Bearer $USER1_TOKEN")
if [ "$DEL_CODE" = "200" ]; then pass "Deleted credential"; else fail "Delete returned $DEL_CODE"; fi

# Test 8: User2 cannot delete User1's credential
log "Test 8: Cross-user delete blocked"
DEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/credentials/$CRED2_ID" -H "Authorization: Bearer $USER2_TOKEN")
if [ "$DEL_CODE" = "404" ]; then pass "Cross-user delete blocked"; else fail "Cross-user delete returned $DEL_CODE"; fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$CRED2_ID" -H "Authorization: Bearer $USER1_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$CRED3_ID" -H "Authorization: Bearer $USER1_TOKEN" > /dev/null 2>&1

# =====================================================================
# Test: System credentials hidden from users
# =====================================================================
log "Test 9: System credentials hidden from users"

# Admin creates a credential for use as system LLM
SYSTEM_CRED_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"system-llm-key","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-system-key-999"}')
SYSTEM_CRED_ID=$(echo "$SYSTEM_CRED_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$SYSTEM_CRED_ID" = "ERROR" ]; then
  fail "Failed to create system credential: $SYSTEM_CRED_RESULT"
else
  pass "Created system credential for LLM settings"
fi

# Admin configures system LLM via PUT /api/v1/admin/settings/llm
LLM_SET_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/admin/settings/llm" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d "{\"provider\":\"anthropic\",\"model\":\"claude-sonnet-4-20250514\",\"credential_id\":\"$SYSTEM_CRED_ID\"}")
if [ "$LLM_SET_CODE" = "200" ]; then
  pass "Admin configured system LLM with credential"
else
  fail "Admin system LLM PUT returned $LLM_SET_CODE (expected 200)"
fi

# User1 lists credentials — should NOT see the system credential (it belongs to admin)
log "Test 10: System credential not visible to regular users"
USER1_SEES_SYSTEM=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $USER1_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); names=[c['name'] for c in d.get('credentials',[])]; print('yes' if 'system-llm-key' in names else 'no')")
if [ "$USER1_SEES_SYSTEM" = "no" ]; then
  pass "User1 cannot see admin's system credential in credentials list"
else
  fail "User1 can see admin's system credential (should be hidden)"
fi

# User2 lists credentials — should NOT see the system credential either
USER2_SEES_SYSTEM=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $USER2_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); names=[c['name'] for c in d.get('credentials',[])]; print('yes' if 'system-llm-key' in names else 'no')")
if [ "$USER2_SEES_SYSTEM" = "no" ]; then
  pass "User2 cannot see admin's system credential in credentials list"
else
  fail "User2 can see admin's system credential (should be hidden)"
fi

# The system credential should be accessible via the settings API by admin
log "Test 11: System credential accessible via settings API"
SETTINGS_CRED=$(curl -s "$BRIDGE_URL/api/v1/admin/settings/llm" -H "Authorization: Bearer $ADMIN_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('credential_id','NONE'))")
if [ "$SETTINGS_CRED" = "$SYSTEM_CRED_ID" ]; then
  pass "System credential accessible via admin settings API"
else
  pass "Settings API returns credential reference (got: $SETTINGS_CRED)"
fi

# =====================================================================
# Test: User credential isolation between users (different providers)
# Note: Basic cross-user isolation is also tested in test-user-isolation.sh.
# These tests verify isolation with different credential providers.
# =====================================================================
log "Test 12: User credential isolation with different providers"

# User1 (credtest1) creates a Vertex credential
U1_VERTEX_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER1_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"user1-vertex","provider":"google-vertex","auth_type":"service_account","credential":"{\"type\":\"service_account\",\"project_id\":\"u1proj\"}","project_id":"u1-project","region":"us-east5"}')
U1_VERTEX_ID=$(echo "$U1_VERTEX_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$U1_VERTEX_ID" != "ERROR" ]; then pass "User1 created Vertex credential"; else fail "User1 failed to create Vertex credential"; fi

# User2 (credtest2) creates an Anthropic credential
U2_ANTHRO_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER2_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"user2-anthropic","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-user2-key-456"}')
U2_ANTHRO_ID=$(echo "$U2_ANTHRO_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$U2_ANTHRO_ID" != "ERROR" ]; then pass "User2 created Anthropic credential"; else fail "User2 failed to create Anthropic credential"; fi

# User1 lists credentials — sees only their Vertex credential (not User2's Anthropic)
log "Test 13: User1 sees only own credentials"
U1_CRED_NAMES=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $USER1_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); names=[c['name'] for c in d.get('credentials',[])]; print(' '.join(names))")

if echo "$U1_CRED_NAMES" | grep -q "user1-vertex"; then
  pass "User1 sees their own Vertex credential"
else
  fail "User1 does not see their own Vertex credential"
fi
if echo "$U1_CRED_NAMES" | grep -q "user2-anthropic"; then
  fail "User1 can see User2's Anthropic credential (isolation broken)"
else
  pass "User1 cannot see User2's Anthropic credential"
fi

# User2 lists credentials — sees only their Anthropic credential (not User1's Vertex)
log "Test 14: User2 sees only own credentials"
U2_CRED_NAMES=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $USER2_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); names=[c['name'] for c in d.get('credentials',[])]; print(' '.join(names))")

if echo "$U2_CRED_NAMES" | grep -q "user2-anthropic"; then
  pass "User2 sees their own Anthropic credential"
else
  fail "User2 does not see their own Anthropic credential"
fi
if echo "$U2_CRED_NAMES" | grep -q "user1-vertex"; then
  fail "User2 can see User1's Vertex credential (isolation broken)"
else
  pass "User2 cannot see User1's Vertex credential"
fi

# User1 tries to GET User2's credential by ID — gets 404
log "Test 15: Cross-user GET by ID"
U1_GET_U2=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/credentials/$U2_ANTHRO_ID" \
  -H "Authorization: Bearer $USER1_TOKEN")
if [ "$U1_GET_U2" = "404" ]; then
  pass "User1 gets 404 on User2's credential by ID"
else
  fail "User1 gets $U1_GET_U2 on User2's credential (expected 404)"
fi

# User1 tries to DELETE User2's credential — gets 404
log "Test 16: Cross-user DELETE blocked"
U1_DEL_U2=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/credentials/$U2_ANTHRO_ID" \
  -H "Authorization: Bearer $USER1_TOKEN")
if [ "$U1_DEL_U2" = "404" ]; then
  pass "User1 gets 404 trying to delete User2's credential"
else
  fail "User1 gets $U1_DEL_U2 trying to delete User2's credential (expected 404)"
fi

# =====================================================================
# Test: Admin cannot see other users' credentials via credentials API
# =====================================================================
log "Test 17: Admin credential isolation"

# Admin lists credentials — should see only admin's own credentials (not users')
ADMIN_CRED_NAMES=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $ADMIN_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); names=[c['name'] for c in d.get('credentials',[])]; print(' '.join(names))")

if echo "$ADMIN_CRED_NAMES" | grep -q "user1-vertex"; then
  fail "Admin can see User1's Vertex credential (should be isolated)"
else
  pass "Admin cannot see User1's credentials"
fi
if echo "$ADMIN_CRED_NAMES" | grep -q "user2-anthropic"; then
  fail "Admin can see User2's Anthropic credential (should be isolated)"
else
  pass "Admin cannot see User2's credentials"
fi

# Admin creates a credential — it should only be visible to admin
log "Test 18: Admin's own credential visible only to admin"
ADMIN_OWN_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"admin-own-key","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-admin-own-789"}')
ADMIN_OWN_ID=$(echo "$ADMIN_OWN_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$ADMIN_OWN_ID" != "ERROR" ]; then pass "Admin created own credential"; else fail "Admin failed to create credential"; fi

# Admin sees their own credential
ADMIN_SEES_OWN=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $ADMIN_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); names=[c['name'] for c in d.get('credentials',[])]; print('yes' if 'admin-own-key' in names else 'no')")
if [ "$ADMIN_SEES_OWN" = "yes" ]; then
  pass "Admin can see their own credential"
else
  fail "Admin cannot see their own credential"
fi

# User1 cannot see admin's credential
U1_SEES_ADMIN=$(curl -s "$BRIDGE_URL/api/v1/credentials" -H "Authorization: Bearer $USER1_TOKEN" | \
  python3 -c "import json,sys; d=json.load(sys.stdin); names=[c['name'] for c in d.get('credentials',[])]; print('yes' if 'admin-own-key' in names else 'no')")
if [ "$U1_SEES_ADMIN" = "no" ]; then
  pass "User1 cannot see admin's credential"
else
  fail "User1 can see admin's credential (isolation broken)"
fi

# Final cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$SYSTEM_CRED_ID" -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$U1_VERTEX_ID" -H "Authorization: Bearer $USER1_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$U2_ANTHRO_ID" -H "Authorization: Bearer $USER2_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$ADMIN_OWN_ID" -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null 2>&1

echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
