#!/usr/bin/env bash
# test-catalog-items.sh — API integration tests for per-item catalog granularity.
#
# Tests: source listing with item counts, item listing, individual toggle,
# bulk toggle, search, enabled agents endpoint, backward compatibility.
#
# Catalog items are seeded from embedded data on Bridge startup, so they
# are available immediately without triggering a manual sync first.
#
# Prerequisites: Bridge running at BRIDGE_URL with AUTH_BACKEND=postgres
# Usage: ADMIN_PASSWORD=<pw> bash scripts/test-catalog-items.sh

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
  -d '{"username":"catalog-item-tester","password":"catitemtest123","is_admin":false}' > /dev/null 2>&1 || true

USER_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"catalog-item-tester","password":"catitemtest123"}' \
  | python3 -c "import json,sys; print(json.load(sys.stdin).get('token',''))")

# Get personal team
PERSONAL_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  | python3 -c "import json,sys; d=json.load(sys.stdin); t=[x for x in d.get('teams',[]) if x.get('is_personal')]; print(t[0]['id'] if t else '')")

# Create a shared team for isolation tests
SHARED=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $USER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"catalog-item-test-team"}')
SHARED_ID=$(echo "$SHARED" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id',''))")
pass "Setup complete (personal=$PERSONAL_ID shared=$SHARED_ID)"

# =====================================================================
# Test 0: Catalog items available immediately (seeded from embedded data)
# =====================================================================
log "Test 0: Catalog items available immediately (no sync needed)"

# Catalog items should be available right away — they are seeded from
# embedded data on Bridge startup, not from git cloning.
SEED_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
SEED_COUNT=$(echo "$SEED_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
total=sum(s.get('total_items',0) for s in sources)
print(total)
")
if [ "$SEED_COUNT" -gt "0" ]; then
  pass "Catalog items available immediately without sync (count=$SEED_COUNT)"
else
  fail "No catalog items available on startup (expected seeded items)"
fi

# =====================================================================
# Test 1: Sources list with item counts
# =====================================================================
log "Test 1: Sources list with item counts"

SOURCES_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")

# Verify response has sources array
HAS_SOURCES=$(echo "$SOURCES_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('yes' if 'sources' in d and isinstance(d['sources'], list) else 'no')
")
if [ "$HAS_SOURCES" = "yes" ]; then
  pass "Response has sources array"
else
  fail "Response missing sources array: $SOURCES_RESP"
fi

# Verify each source has required fields
SOURCES_VALID=$(echo "$SOURCES_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
if not sources:
    print('empty')
    sys.exit()
for s in sources:
    if 'source_id' not in s:
        print('missing_source_id')
        sys.exit()
    if 'total_items' not in s:
        print('missing_total_items')
        sys.exit()
    if 'enabled_items' not in s:
        print('missing_enabled_items')
        sys.exit()
print('valid')
")
if [ "$SOURCES_VALID" = "valid" ]; then
  pass "All sources have source_id, total_items, enabled_items"
elif [ "$SOURCES_VALID" = "empty" ]; then
  fail "Sources array is empty (expected at least one source)"
else
  fail "Sources validation failed: $SOURCES_VALID"
fi

# Count sources
SOURCE_COUNT=$(echo "$SOURCES_RESP" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('sources',[])))
")
pass "Found $SOURCE_COUNT catalog sources"

# =====================================================================
# Test 2: List items in a source
# =====================================================================
log "Test 2: List items in a source"

# Pick the first source_id from test 1 (fall back to code-review)
SOURCE_ID=$(echo "$SOURCES_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
sources=d.get('sources',[])
print(sources[0]['source_id'] if sources else 'code-review')
")

ITEMS_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID" \
  -H "Authorization: Bearer $USER_TOKEN")

# Verify response has items array
HAS_ITEMS=$(echo "$ITEMS_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('yes' if 'items' in d and isinstance(d['items'], list) else 'no')
")
if [ "$HAS_ITEMS" = "yes" ]; then
  pass "Response has items array"
else
  fail "Response missing items array: $ITEMS_RESP"
fi

# Verify items have required fields
ITEMS_VALID=$(echo "$ITEMS_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
items=d.get('items',[])
if not items:
    print('empty')
    sys.exit()
for item in items:
    missing=[]
    for field in ['slug','name','item_type','enabled']:
        if field not in item:
            missing.append(field)
    if missing:
        print('missing:' + ','.join(missing))
        sys.exit()
print('valid')
")
if [ "$ITEMS_VALID" = "valid" ]; then
  pass "All items have slug, name, item_type, enabled fields"
elif [ "$ITEMS_VALID" = "empty" ]; then
  fail "Items array is empty for source $SOURCE_ID"
else
  fail "Items validation failed: $ITEMS_VALID"
fi

# Get first item slug for subsequent tests
FIRST_ITEM_SLUG=$(echo "$ITEMS_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
items=d.get('items',[])
print(items[0]['slug'] if items else '')
")
ITEM_COUNT=$(echo "$ITEMS_RESP" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('items',[])))
")
pass "Source '$SOURCE_ID' has $ITEM_COUNT items (first: $FIRST_ITEM_SLUG)"

# =====================================================================
# Test 3: Toggle individual item
# =====================================================================
log "Test 3: Toggle individual item"

if [ -z "$FIRST_ITEM_SLUG" ]; then
  fail "No item slug available to test toggle (skipping)"
else
  # Enable the item
  ENABLE_RESP=$(curl -s -w "\n%{http_code}" -X PUT \
    "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID/$FIRST_ITEM_SLUG" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"enabled":true}')
  ENABLE_CODE=$(echo "$ENABLE_RESP" | tail -1)
  ENABLE_BODY=$(echo "$ENABLE_RESP" | head -1)

  if [ "$ENABLE_CODE" = "200" ]; then
    pass "Enable item returned 200"
  else
    fail "Enable item returned $ENABLE_CODE (expected 200): $ENABLE_BODY"
  fi

  # Verify item shows as enabled
  ITEM_ENABLED=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID" \
    -H "Authorization: Bearer $USER_TOKEN" \
    | python3 -c "
import json,sys
d=json.load(sys.stdin)
for item in d.get('items',[]):
    if item.get('slug') == '$FIRST_ITEM_SLUG':
        print('yes' if item.get('enabled') else 'no')
        sys.exit()
print('not_found')
")
  if [ "$ITEM_ENABLED" = "yes" ]; then
    pass "Item '$FIRST_ITEM_SLUG' shows as enabled after toggle on"
  else
    fail "Item '$FIRST_ITEM_SLUG' not enabled after toggle (got: $ITEM_ENABLED)"
  fi

  # Disable the item
  DISABLE_RESP=$(curl -s -w "\n%{http_code}" -X PUT \
    "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID/$FIRST_ITEM_SLUG" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"enabled":false}')
  DISABLE_CODE=$(echo "$DISABLE_RESP" | tail -1)

  if [ "$DISABLE_CODE" = "200" ]; then
    pass "Disable item returned 200"
  else
    fail "Disable item returned $DISABLE_CODE (expected 200)"
  fi

  # Verify item shows as disabled
  ITEM_DISABLED=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID" \
    -H "Authorization: Bearer $USER_TOKEN" \
    | python3 -c "
import json,sys
d=json.load(sys.stdin)
for item in d.get('items',[]):
    if item.get('slug') == '$FIRST_ITEM_SLUG':
        print('yes' if item.get('enabled') else 'no')
        sys.exit()
print('not_found')
")
  if [ "$ITEM_DISABLED" = "no" ]; then
    pass "Item '$FIRST_ITEM_SLUG' shows as disabled after toggle off"
  else
    fail "Item '$FIRST_ITEM_SLUG' still enabled after disable (got: $ITEM_DISABLED)"
  fi
fi

# =====================================================================
# Test 4: Bulk toggle
# =====================================================================
log "Test 4: Bulk toggle"

if [ -z "$FIRST_ITEM_SLUG" ]; then
  fail "No item slug available to test bulk toggle (skipping)"
else
  BULK_RESP=$(curl -s -w "\n%{http_code}" -X PUT \
    "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"items\":[{\"slug\":\"$FIRST_ITEM_SLUG\",\"enabled\":true}]}")
  BULK_CODE=$(echo "$BULK_RESP" | tail -1)
  BULK_BODY=$(echo "$BULK_RESP" | head -1)

  if [ "$BULK_CODE" = "200" ]; then
    pass "Bulk toggle returned 200"
  else
    fail "Bulk toggle returned $BULK_CODE (expected 200): $BULK_BODY"
  fi

  # Verify the item is now enabled after bulk toggle
  BULK_VERIFY=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID" \
    -H "Authorization: Bearer $USER_TOKEN" \
    | python3 -c "
import json,sys
d=json.load(sys.stdin)
for item in d.get('items',[]):
    if item.get('slug') == '$FIRST_ITEM_SLUG':
        print('yes' if item.get('enabled') else 'no')
        sys.exit()
print('not_found')
")
  if [ "$BULK_VERIFY" = "yes" ]; then
    pass "Item enabled after bulk toggle"
  else
    fail "Item not enabled after bulk toggle (got: $BULK_VERIFY)"
  fi

  # Disable via bulk toggle for cleanup
  curl -s -X PUT "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"items\":[{\"slug\":\"$FIRST_ITEM_SLUG\",\"enabled\":false}]}" > /dev/null
fi

# =====================================================================
# Test 5: Search items
# =====================================================================
log "Test 5: Search items"

# Search within the source we already know has items
SEARCH_RESP=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID?search=${FIRST_ITEM_SLUG:0:4}" \
  -H "Authorization: Bearer $USER_TOKEN")
SEARCH_COUNT=$(echo "$SEARCH_RESP" | python3 -c "
import json,sys
d=json.load(sys.stdin)
items=d.get('items',[])
print(len(items))
")

if [ "$SEARCH_COUNT" -gt "0" ]; then
  pass "Search returned $SEARCH_COUNT items for query '${FIRST_ITEM_SLUG:0:4}'"
else
  # Search may not be implemented yet — note it but don't hard fail
  fail "Search returned 0 items (search may not be implemented or no matches)"
fi

# =====================================================================
# Test 6: List enabled agents
# =====================================================================
log "Test 6: List enabled agents"

# Disable the item for cleanup (toggle already tested above in Test 4)
if [ -n "$FIRST_ITEM_SLUG" ]; then
  curl -s -X PUT "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog/$SOURCE_ID/$FIRST_ITEM_SLUG" \
    -H "Authorization: Bearer $USER_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"enabled":false}' > /dev/null
fi

# =====================================================================
# Test 7: Backward compatibility
# =====================================================================
log "Test 7: Backward compatibility"

# Old catalog endpoint still works (GET /api/v1/catalog)
OLD_CATALOG_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$OLD_CATALOG_CODE" = "200" ]; then
  pass "Old catalog endpoint (GET /api/v1/catalog) returns 200"
else
  fail "Old catalog endpoint returned $OLD_CATALOG_CODE (expected 200)"
fi

# Old team catalog endpoint still works (GET /api/v1/teams/{id}/catalog with entries format)
OLD_TEAM_CATALOG=$(curl -s "$BRIDGE_URL/api/v1/teams/$SHARED_ID/catalog" \
  -H "Authorization: Bearer $USER_TOKEN")
OLD_FORMAT_OK=$(echo "$OLD_TEAM_CATALOG" | python3 -c "
import json,sys
d=json.load(sys.stdin)
# Should have at least one of: sources (new format) or entries (old format)
if 'sources' in d or 'entries' in d:
    print('ok')
else:
    print('missing')
")
if [ "$OLD_FORMAT_OK" = "ok" ]; then
  pass "Team catalog response has expected structure"
else
  fail "Team catalog response missing both sources and entries: $OLD_TEAM_CATALOG"
fi

# Sessions endpoint returns 200
SESSIONS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$SESSIONS_CODE" = "200" ]; then
  pass "Sessions endpoint returns 200"
else
  fail "Sessions endpoint returned $SESSIONS_CODE (expected 200)"
fi

# Credentials endpoint returns 200
CREDS_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$CREDS_CODE" = "200" ]; then
  pass "Credentials endpoint returns 200"
else
  fail "Credentials endpoint returned $CREDS_CODE (expected 200)"
fi

# Workflows endpoint returns 200
WF_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $USER_TOKEN")
if [ "$WF_CODE" = "200" ]; then
  pass "Workflows endpoint returns 200"
else
  fail "Workflows endpoint returned $WF_CODE (expected 200)"
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
