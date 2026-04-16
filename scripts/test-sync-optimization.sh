#!/usr/bin/env bash
# test-sync-optimization.sh — Tests for sync optimization (Issue #318).
#
# Verifies that catalog items are seeded from embedded data on Bridge startup
# (no git cloning required), the manual sync endpoint works, sync handles
# empty repos gracefully, and existing endpoints remain backward-compatible.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-sync-optimization.sh
#
# Tests:
#   Test 1: Catalog items available without sync (seeded from embedded data)
#   Test 2: Manual sync endpoint works
#   Test 3: Sync doesn't crash on empty repos
#   Test 4: Backward compatibility (existing endpoints return 200)

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
  | python3 -c "import json,sys; d=json.load(sys.stdin); t=d.get('token',''); print(t) if t else sys.exit('Login failed: ' + json.dumps(d))")

# Create test user
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"sync-opt-tester","password":"syncopttest123","is_admin":false}' > /dev/null 2>&1 || true

USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"sync-opt-tester","password":"syncopttest123"}' \
  | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

# Get personal team
PERSONAL_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "import json,sys; d=json.load(sys.stdin); t=[x for x in d.get('teams',[]) if x.get('is_personal')]; print(t[0]['id'] if t else '')")

# Create a shared team for isolation
SHARED=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"sync-opt-test-team"}')
SHARED_ID=$(echo "$SHARED" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id',''))")
pass "Setup complete (personal=$PERSONAL_ID shared=$SHARED_ID)"

# =====================================================================
# Test 1: Catalog items available without sync (seeded from embedded data)
# =====================================================================
log "Test 1: Catalog items available without sync"

# 1a: GET catalog sources — should have sources with items immediately
CATALOG_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")

HAS_SOURCES=$(echo "$CATALOG_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
print('yes' if sources else 'no')
")
if [ "$HAS_SOURCES" = "yes" ]; then
  pass "Catalog has sources immediately on startup"
else
  fail "Catalog has no sources on startup (expected seeded data)"
fi

# 1b: Verify total item count > 0 across all sources
TOTAL_ITEMS=$(echo "$CATALOG_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
total=sum(s.get('total_items',0) for s in sources)
print(total)
")
if [ "$TOTAL_ITEMS" -gt "0" ]; then
  pass "Catalog has $TOTAL_ITEMS items available without sync"
else
  fail "Catalog has 0 items (expected > 0 from embedded seed data)"
fi

# 1c: Verify sources have expected structure
SOURCES_VALID=$(echo "$CATALOG_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
for s in sources:
    if 'source_id' not in s or 'total_items' not in s:
        print('invalid')
        sys.exit()
print('valid')
")
if [ "$SOURCES_VALID" = "valid" ]; then
  pass "Seeded sources have valid structure (source_id, total_items)"
else
  fail "Seeded sources have invalid structure"
fi

# 1d: Pick first source and verify items are listable
SOURCE_ID=$(echo "$CATALOG_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
print(sources[0]['source_id'] if sources else '')
")
if [ -n "$SOURCE_ID" ]; then
  ITEMS_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID" \
    -H "Authorization: Bearer $USER_TOKEN")
  ITEM_COUNT=$(echo "$ITEMS_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print(len(d.get('items',[])))
")
  if [ "$ITEM_COUNT" -gt "0" ]; then
    pass "Source '$SOURCE_ID' has $ITEM_COUNT items available without sync"
  else
    fail "Source '$SOURCE_ID' has 0 items (expected seeded items)"
  fi
else
  fail "No source_id found in catalog response"
fi

# =====================================================================
# Test 2: Manual sync endpoint works
# =====================================================================
log "Test 2: Manual sync endpoint works"

# 2a: POST /api/v1/agent-definitions/sync — should return 200
SYNC_RESP=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
SYNC_CODE=$(echo "$SYNC_RESP" | tail -1)

if [ "$SYNC_CODE" = "200" ]; then
  pass "POST /api/v1/agent-definitions/sync returned 200"
else
  fail "POST /api/v1/agent-definitions/sync returned $SYNC_CODE (expected 200)"
fi

# 2b: After sync, catalog items should still be present
AFTER_SYNC_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
AFTER_SYNC_COUNT=$(echo "$AFTER_SYNC_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
total=sum(s.get('total_items',0) for s in sources)
print(total)
")
if [ "$AFTER_SYNC_COUNT" -gt "0" ]; then
  pass "Catalog still has $AFTER_SYNC_COUNT items after manual sync"
else
  fail "Catalog has 0 items after manual sync (data should persist)"
fi

# =====================================================================
# Test 3: Sync doesn't crash on empty repos
# =====================================================================
log "Test 3: Sync with no user agent repos configured"

# 3a: Clear any user agent repos
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos":[]}' > /dev/null

# 3b: Verify repos are empty
REPOS_COUNT=$(curl -s "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('repos',[])))")
if [ "$REPOS_COUNT" = "0" ]; then
  pass "Agent repos confirmed empty"
else
  fail "Agent repos not empty after clearing (got $REPOS_COUNT)"
fi

# 3c: Trigger sync with no repos — should succeed, not crash
EMPTY_SYNC_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$EMPTY_SYNC_CODE" = "200" ]; then
  pass "Sync with no repos returned 200 (no crash)"
else
  fail "Sync with no repos returned $EMPTY_SYNC_CODE (expected 200)"
fi

# 3d: Catalog items should still exist (embedded seed data unaffected)
STILL_SEEDED_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
STILL_SEEDED_COUNT=$(echo "$STILL_SEEDED_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
total=sum(s.get('total_items',0) for s in sources)
print(total)
")
if [ "$STILL_SEEDED_COUNT" -gt "0" ]; then
  pass "Seeded catalog items still present after empty-repo sync ($STILL_SEEDED_COUNT items)"
else
  fail "Seeded catalog items lost after empty-repo sync"
fi

# =====================================================================
# Test 4: Backward compatibility
# =====================================================================
log "Test 4: Backward compatibility"

# 4a: GET /api/v1/catalog
CATALOG_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$CATALOG_CODE" = "200" ]; then
  pass "GET /api/v1/catalog returns 200"
else
  fail "GET /api/v1/catalog returned $CATALOG_CODE (expected 200)"
fi

# 4b: GET /api/v1/teams/{team}/catalog
TEAM_CATALOG_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$TEAM_CATALOG_CODE" = "200" ]; then
  pass "GET /api/v1/teams/{team}/catalog returns 200"
else
  fail "GET /api/v1/teams/{team}/catalog returned $TEAM_CATALOG_CODE (expected 200)"
fi

# 4c: GET /api/v1/sessions
SESSIONS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$SESSIONS_CODE" = "200" ]; then
  pass "GET /api/v1/sessions returns 200"
else
  fail "GET /api/v1/sessions returned $SESSIONS_CODE (expected 200)"
fi

# 4d: GET /api/v1/credentials
CREDS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$CREDS_CODE" = "200" ]; then
  pass "GET /api/v1/credentials returns 200"
else
  fail "GET /api/v1/credentials returned $CREDS_CODE (expected 200)"
fi

# 4e: GET /api/v1/agent-definitions
DEFS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/agent-definitions" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$DEFS_CODE" = "200" ]; then
  pass "GET /api/v1/agent-definitions returns 200"
else
  fail "GET /api/v1/agent-definitions returned $DEFS_CODE (expected 200)"
fi

# 4f: GET /api/v1/workflows
WF_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$WF_CODE" = "200" ]; then
  pass "GET /api/v1/workflows returns 200"
else
  fail "GET /api/v1/workflows returned $WF_CODE (expected 200)"
fi

# 4g: GET /api/v1/schedules
SCHED_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$SCHED_CODE" = "200" ]; then
  pass "GET /api/v1/schedules returns 200"
else
  fail "GET /api/v1/schedules returned $SCHED_CODE (expected 200)"
fi

# =====================================================================
# Cleanup
# =====================================================================
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$SHARED_ID" \
  -H "Authorization: Bearer $USER_TOKEN" > /dev/null 2>&1

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
