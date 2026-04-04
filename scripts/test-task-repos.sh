#!/bin/bash
# test-task-repos.sh — Tests for the task repo and task definitions API.
#
# Verifies system task repo CRUD, repo validation, sync lifecycle,
# and task definition detail endpoints.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Internet access (tests clone repos from GitHub)
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-task-repos.sh
#
# Manual UI checklist (verify in dashboard after running):
#   [ ] Admin Settings > Task Repos shows configured repos
#   [ ] Task Definitions page lists synced tasks
#   [ ] Task Definition detail page shows raw YAML
#   [ ] Removing repos and re-syncing clears definitions
#
# Tests:
#   Group 1: System Task Repo CRUD (5 tests)
#   Group 2: Validation (3 tests)
#   Group 3: Sync Lifecycle (6 tests)
#   Group 4: Task Definition Detail (2 tests)
#   Group 5: Cleanup

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
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])")

ALCOVE_TESTING_URL="https://github.com/bmbouter/alcove-testing.git"

# =====================================================================
# Group 1: System Task Repo CRUD
# =====================================================================
log "=== Group 1: System Task Repo CRUD ==="

# Test 1.1: GET admin/settings/task-repos — empty initially
log "Test 1.1: GET task-repos — initially empty"
# Clear any existing repos first
curl -s -X PUT "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos":[]}' > /dev/null
REPOS=$(curl -s "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('repos',[])))")
if [ "$REPOS" = "0" ]; then
  pass "Task repos initially empty"
else
  fail "Task repos not empty (got $REPOS)"
fi

# Test 1.2: PUT with alcove-testing repo — 200
log "Test 1.2: PUT alcove-testing repo"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"repos\":[{\"url\":\"$ALCOVE_TESTING_URL\",\"name\":\"alcove-testing\"}]}")
if [ "$HTTP_CODE" = "200" ]; then
  pass "PUT alcove-testing repo returned 200"
else
  fail "PUT returned $HTTP_CODE (expected 200)"
fi

# Test 1.3: GET — verify saved
log "Test 1.3: GET — verify saved"
SAVED_URL=$(curl -s "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | python3 -c "import json,sys; repos=json.load(sys.stdin).get('repos',[]); print(repos[0]['url'] if repos else 'EMPTY')")
if [ "$SAVED_URL" = "$ALCOVE_TESTING_URL" ]; then
  pass "Repo URL saved correctly"
else
  fail "Repo URL mismatch: $SAVED_URL"
fi

# Test 1.4: PUT with empty — 200
log "Test 1.4: PUT empty repos"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos":[]}')
if [ "$HTTP_CODE" = "200" ]; then
  pass "PUT empty repos returned 200"
else
  fail "PUT empty returned $HTTP_CODE (expected 200)"
fi

# Test 1.5: GET — verify empty
log "Test 1.5: GET — verify empty after clearing"
REPOS=$(curl -s "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('repos',[])))")
if [ "$REPOS" = "0" ]; then
  pass "Task repos empty after clearing"
else
  fail "Task repos not empty after clearing (got $REPOS)"
fi

# =====================================================================
# Group 2: Validation
# =====================================================================
log "=== Group 2: Validation ==="

# Test 2.1: POST validate with alcove-testing — valid=true, task_count=2
log "Test 2.1: Validate alcove-testing repo"
VALIDATE=$(curl -s -X POST "$BRIDGE_URL/api/v1/task-repos/validate" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"url\":\"$ALCOVE_TESTING_URL\"}")
VALID=$(echo "$VALIDATE" | python3 -c "import json,sys; print(json.load(sys.stdin).get('valid',False))")
TASK_COUNT=$(echo "$VALIDATE" | python3 -c "import json,sys; print(json.load(sys.stdin).get('task_count',0))")
if [ "$VALID" = "True" ] && [ "$TASK_COUNT" = "2" ]; then
  pass "Validate alcove-testing: valid=true, task_count=2"
else
  fail "Validate alcove-testing: valid=$VALID, task_count=$TASK_COUNT (expected True, 2)"
fi

# Test 2.2: POST validate with nonexistent repo — valid=false
log "Test 2.2: Validate nonexistent repo"
VALIDATE=$(curl -s -X POST "$BRIDGE_URL/api/v1/task-repos/validate" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://github.com/this-does-not-exist-ever/no-repo-here.git"}')
VALID=$(echo "$VALIDATE" | python3 -c "import json,sys; print(json.load(sys.stdin).get('valid',False))")
if [ "$VALID" = "False" ]; then
  pass "Nonexistent repo returns valid=false"
else
  fail "Nonexistent repo returns valid=$VALID (expected False)"
fi

# Test 2.3: POST validate with empty URL — 400
log "Test 2.3: Validate empty URL"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/task-repos/validate" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":""}')
if [ "$HTTP_CODE" = "400" ]; then
  pass "Empty URL returns 400"
else
  fail "Empty URL returned $HTTP_CODE (expected 400)"
fi

# =====================================================================
# Group 3: Sync Lifecycle
# =====================================================================
log "=== Group 3: Sync Lifecycle ==="

# Test 3.1: Add alcove-testing repo
log "Test 3.1: Add alcove-testing repo for sync"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"repos\":[{\"url\":\"$ALCOVE_TESTING_URL\",\"name\":\"alcove-testing\"}]}")
if [ "$HTTP_CODE" = "200" ]; then
  pass "Added alcove-testing repo for sync"
else
  fail "Failed to add repo: $HTTP_CODE"
fi

# Test 3.2: Trigger sync
log "Test 3.2: Trigger sync"
SYNC_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/task-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$SYNC_CODE" = "200" ]; then
  pass "Sync triggered successfully"
else
  fail "Sync returned $SYNC_CODE (expected 200)"
fi

# Test 3.3: Wait and verify task definitions count=2
log "Test 3.3: Verify synced task definitions (count=2)"
COUNT=0
for attempt in 1 2 3 4 5; do
  sleep 3
  COUNT=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/task-definitions" | \
    python3 -c "import sys,json; print(json.load(sys.stdin).get('count',0))")
  if [ "$COUNT" -ge 2 ]; then break; fi
done
if [ "$COUNT" -ge 2 ]; then
  pass "Task definitions synced (count=$COUNT)"
else
  fail "Expected at least 2 task definitions, got $COUNT"
fi

# Test 3.4: Verify fields on first definition (name, source_repo, source_file)
log "Test 3.4: Verify task definition fields"
DEFS_JSON=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/task-definitions")
FIELDS_OK=$(echo "$DEFS_JSON" | python3 -c "
import json, sys
data = json.load(sys.stdin)
defs = data.get('task_definitions', [])
if not defs:
    print('NO_DEFS')
    sys.exit()
d = defs[0]
has_name = bool(d.get('name', ''))
has_source_repo = bool(d.get('source_repo', ''))
has_source_file = bool(d.get('source_file', ''))
if has_name and has_source_repo and has_source_file:
    print('OK')
else:
    print('MISSING name=%s source_repo=%s source_file=%s' % (d.get('name',''), d.get('source_repo',''), d.get('source_file','')))
")
if [ "$FIELDS_OK" = "OK" ]; then
  pass "Task definition has name, source_repo, source_file"
else
  fail "Task definition fields: $FIELDS_OK"
fi

# Save first definition ID for Group 4
FIRST_DEF_ID=$(echo "$DEFS_JSON" | python3 -c "import json,sys; defs=json.load(sys.stdin).get('task_definitions',[]); print(defs[0]['id'] if defs else 'NONE')")

# Test 3.5: Remove repo (PUT empty) and sync
log "Test 3.5: Remove repo and sync"
curl -s -X PUT "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos":[]}' > /dev/null
SYNC_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/task-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$SYNC_CODE" = "200" ]; then
  pass "Sync after removing repos returned 200"
else
  fail "Sync after removing repos returned $SYNC_CODE"
fi

# Test 3.6: Verify task definitions cleared (count=0)
log "Test 3.6: Verify definitions cleared after removing repos"
COUNT=0
for attempt in 1 2 3 4 5; do
  sleep 3
  COUNT=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/task-definitions" | \
    python3 -c "import sys,json; print(json.load(sys.stdin).get('count',0))")
  if [ "$COUNT" = "0" ]; then break; fi
done
if [ "$COUNT" = "0" ]; then
  pass "Task definitions cleared (count=0)"
else
  fail "Expected 0 task definitions after clearing repos, got $COUNT"
fi

# =====================================================================
# Group 4: Task Definition Detail
# =====================================================================
log "=== Group 4: Task Definition Detail ==="

# Re-add repo and sync so we have definitions to query
curl -s -X PUT "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"repos\":[{\"url\":\"$ALCOVE_TESTING_URL\",\"name\":\"alcove-testing\"}]}" > /dev/null
curl -s -X POST "$BRIDGE_URL/api/v1/task-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null
for attempt in 1 2 3 4 5; do
  sleep 3
  DEF_COUNT=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/task-definitions" | \
    python3 -c "import sys,json; print(json.load(sys.stdin).get('count',0))")
  if [ "$DEF_COUNT" -gt 0 ]; then break; fi
done

DEF_ID=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/task-definitions" | \
  python3 -c "import json,sys; defs=json.load(sys.stdin).get('task_definitions',[]); print(defs[0]['id'] if defs else 'NONE')")

# Test 4.1: GET task-definitions/{id} — verify has raw_yaml
log "Test 4.1: GET task definition by ID"
if [ "$DEF_ID" = "NONE" ]; then
  fail "No task definition ID available to test detail endpoint"
else
  HAS_YAML=$(curl -s -H "Authorization: Bearer $ADMIN_TOKEN" "$BRIDGE_URL/api/v1/task-definitions/$DEF_ID" | \
    python3 -c "import json,sys; d=json.load(sys.stdin); print('yes' if d.get('raw_yaml','') else 'no')")
  if [ "$HAS_YAML" = "yes" ]; then
    pass "Task definition detail includes raw_yaml"
  else
    fail "Task definition detail missing raw_yaml"
  fi
fi

# Test 4.2: GET task-definitions/nonexistent — 404
log "Test 4.2: GET nonexistent task definition"
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $ADMIN_TOKEN" \
  "$BRIDGE_URL/api/v1/task-definitions/00000000-0000-0000-0000-000000000000")
if [ "$HTTP_CODE" = "404" ]; then
  pass "Nonexistent task definition returns 404"
else
  fail "Nonexistent task definition returned $HTTP_CODE (expected 404)"
fi

# =====================================================================
# Group 5: Cleanup
# =====================================================================
log "=== Group 5: Cleanup ==="
curl -s -X PUT "$BRIDGE_URL/api/v1/admin/settings/task-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"repos":[]}' > /dev/null
curl -s -X POST "$BRIDGE_URL/api/v1/task-definitions/sync" \
  -H "Authorization: Bearer $ADMIN_TOKEN" > /dev/null
log "Cleanup complete: repos cleared, final sync triggered"

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
