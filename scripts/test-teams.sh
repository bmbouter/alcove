#!/bin/bash
# test-teams.sh — Tests for the teams management API.
#
# Verifies team CRUD, membership management, personal team constraints,
# team-scoped resource isolation, cross-team isolation, and backward
# compatibility with existing API endpoints.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-teams.sh
#
# Tests:
#   - Personal team auto-creation
#   - Team CRUD (create, list, get, update, delete)
#   - Membership management (add/remove members)
#   - Personal team constraints (cannot delete, add members, or rename)
#   - Team-scoped resource isolation (credentials scoped to teams)
#   - Cross-team isolation (same-name credentials in different teams)
#   - Backward compatibility (API works without X-Alcove-Team header)

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
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | python3 -c "import json,sys; d=json.load(sys.stdin); t=d.get('token',''); print(t) if t else sys.exit('Login failed: ' + json.dumps(d))")

# Create test users
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"team-alice","password":"teamalice123","is_admin":false}' > /dev/null 2>&1 || true

curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"team-bob","password":"teambob12345","is_admin":false}' > /dev/null 2>&1 || true

ALICE_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"team-alice","password":"teamalice123"}' | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

BOB_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"team-bob","password":"teambob12345"}' | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

echo "  Alice token: ${ALICE_TOKEN:0:10}..."
echo "  Bob token: ${BOB_TOKEN:0:10}..."

# =====================================================================
# Test 1: Personal team auto-creation
# =====================================================================
log "Test 1: Personal team auto-creation"

TEAMS_RESULT=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN")
TEAM_COUNT=$(echo "$TEAMS_RESULT" | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d.get('teams',[])))")
if [ "$TEAM_COUNT" = "1" ]; then
  pass "Alice has exactly one team"
else
  fail "Alice has $TEAM_COUNT teams (expected 1)"
fi

IS_PERSONAL=$(echo "$TEAMS_RESULT" | python3 -c "import json,sys; d=json.load(sys.stdin); teams=d.get('teams',[]); print(teams[0].get('is_personal',False) if teams else 'NONE')")
if [ "$IS_PERSONAL" = "True" ]; then
  pass "Alice's team is personal"
else
  fail "Alice's team is_personal=$IS_PERSONAL (expected True)"
fi

PERSONAL_TEAM_ID=$(echo "$TEAMS_RESULT" | python3 -c "import json,sys; d=json.load(sys.stdin); teams=d.get('teams',[]); print(teams[0].get('id','') if teams else '')")

# Verify Alice is a member of her personal team
PERSONAL_DETAIL=$(curl -s "$BRIDGE_URL/api/v1/teams/$PERSONAL_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN")
ALICE_IS_MEMBER=$(echo "$PERSONAL_DETAIL" | python3 -c "
import json,sys
d=json.load(sys.stdin)
members=d.get('members',[])
print('yes' if 'team-alice' in members else 'no')
")
if [ "$ALICE_IS_MEMBER" = "yes" ]; then
  pass "Alice is a member of her personal team"
else
  fail "Alice is not a member of her personal team"
fi

# =====================================================================
# Test 2: Team CRUD
# =====================================================================
log "Test 2: Create team"

CREATE_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"test-team"}')
TEST_TEAM_ID=$(echo "$CREATE_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$TEST_TEAM_ID" != "ERROR" ]; then
  pass "Created team: $TEST_TEAM_ID"
else
  fail "Failed to create team: $CREATE_RESULT"
fi

# Verify it appears in the list
log "Test 2b: Team appears in list"
LIST_RESULT=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN")
HAS_TEST_TEAM=$(echo "$LIST_RESULT" | python3 -c "
import json,sys
d=json.load(sys.stdin)
ids=[t.get('id','') for t in d.get('teams',[])]
print('yes' if '$TEST_TEAM_ID' in ids else 'no')
")
if [ "$HAS_TEST_TEAM" = "yes" ]; then
  pass "Team appears in list"
else
  fail "Team not found in list"
fi

# Get team detail
log "Test 2c: Get team detail"
DETAIL=$(curl -s "$BRIDGE_URL/api/v1/teams/$TEST_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN")
DETAIL_NAME=$(echo "$DETAIL" | python3 -c "import json,sys; print(json.load(sys.stdin).get('name',''))")
CREATOR_IS_MEMBER=$(echo "$DETAIL" | python3 -c "
import json,sys
d=json.load(sys.stdin)
members=d.get('members',[])
print('yes' if 'team-alice' in members else 'no')
")
if [ "$DETAIL_NAME" = "test-team" ]; then
  pass "Team detail name is correct"
else
  fail "Team detail name is '$DETAIL_NAME' (expected 'test-team')"
fi
if [ "$CREATOR_IS_MEMBER" = "yes" ]; then
  pass "Creator is a member of the team"
else
  fail "Creator is not a member of the team"
fi

# Rename team
log "Test 2d: Rename team"
RENAME_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT "$BRIDGE_URL/api/v1/teams/$TEST_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"renamed-team"}')
if [ "$RENAME_CODE" = "200" ]; then
  pass "Renamed team (HTTP 200)"
else
  fail "Rename returned $RENAME_CODE (expected 200)"
fi

RENAMED_NAME=$(curl -s "$BRIDGE_URL/api/v1/teams/$TEST_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "import json,sys; print(json.load(sys.stdin).get('name',''))")
if [ "$RENAMED_NAME" = "renamed-team" ]; then
  pass "Team name updated to 'renamed-team'"
else
  fail "Team name is '$RENAMED_NAME' (expected 'renamed-team')"
fi

# Delete team
log "Test 2e: Delete team"
DEL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "$BRIDGE_URL/api/v1/teams/$TEST_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$DEL_CODE" = "200" ]; then
  pass "Deleted team (HTTP 200)"
else
  fail "Delete returned $DEL_CODE (expected 200)"
fi

# Verify it's gone
GONE_CHECK=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
ids=[t.get('id','') for t in d.get('teams',[])]
print('found' if '$TEST_TEAM_ID' in ids else 'gone')
")
if [ "$GONE_CHECK" = "gone" ]; then
  pass "Team confirmed deleted from list"
else
  fail "Team still appears in list after deletion"
fi

# =====================================================================
# Test 3: Membership management
# =====================================================================
log "Test 3: Membership management"

# Create a team for membership tests
MEMBER_TEAM=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"member-test-team"}')
MEMBER_TEAM_ID=$(echo "$MEMBER_TEAM" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$MEMBER_TEAM_ID" = "ERROR" ]; then
  fail "Failed to create team for membership test"
else
  pass "Created team for membership test"
fi

# Add Bob as a member
log "Test 3b: Add member"
ADD_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BRIDGE_URL/api/v1/teams/$MEMBER_TEAM_ID/members" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"team-bob"}')
if [ "$ADD_CODE" = "200" ] || [ "$ADD_CODE" = "201" ]; then
  pass "Added Bob as member (HTTP $ADD_CODE)"
else
  fail "Add member returned $ADD_CODE (expected 200 or 201)"
fi

# Verify Bob is in members list
BOB_IN_MEMBERS=$(curl -s "$BRIDGE_URL/api/v1/teams/$MEMBER_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
members=d.get('members',[])
print('yes' if 'team-bob' in members else 'no')
")
if [ "$BOB_IN_MEMBERS" = "yes" ]; then
  pass "Bob appears in team members"
else
  fail "Bob not found in team members"
fi

# Remove Bob
log "Test 3c: Remove member"
REMOVE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
  "$BRIDGE_URL/api/v1/teams/$MEMBER_TEAM_ID/members/team-bob" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$REMOVE_CODE" = "200" ]; then
  pass "Removed Bob from team (HTTP 200)"
else
  fail "Remove member returned $REMOVE_CODE (expected 200)"
fi

# Verify Bob is gone
BOB_GONE=$(curl -s "$BRIDGE_URL/api/v1/teams/$MEMBER_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
members=d.get('members',[])
print('yes' if 'team-bob' in members else 'no')
")
if [ "$BOB_GONE" = "no" ]; then
  pass "Bob removed from team members"
else
  fail "Bob still in team members after removal"
fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$MEMBER_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1

# =====================================================================
# Test 4: Personal team constraints
# =====================================================================
log "Test 4: Personal team constraints"

# Try to delete personal team
DEL_PERSONAL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE \
  "$BRIDGE_URL/api/v1/teams/$PERSONAL_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$DEL_PERSONAL_CODE" = "400" ] || [ "$DEL_PERSONAL_CODE" = "403" ]; then
  pass "Cannot delete personal team (HTTP $DEL_PERSONAL_CODE)"
else
  fail "Delete personal team returned $DEL_PERSONAL_CODE (expected 400 or 403)"
fi

# Try to add member to personal team
ADD_PERSONAL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  "$BRIDGE_URL/api/v1/teams/$PERSONAL_TEAM_ID/members" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"team-bob"}')
if [ "$ADD_PERSONAL_CODE" = "400" ] || [ "$ADD_PERSONAL_CODE" = "403" ]; then
  pass "Cannot add member to personal team (HTTP $ADD_PERSONAL_CODE)"
else
  fail "Add member to personal team returned $ADD_PERSONAL_CODE (expected 400 or 403)"
fi

# Try to rename personal team
RENAME_PERSONAL_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X PUT \
  "$BRIDGE_URL/api/v1/teams/$PERSONAL_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"hacked-personal"}')
if [ "$RENAME_PERSONAL_CODE" = "400" ] || [ "$RENAME_PERSONAL_CODE" = "403" ]; then
  pass "Cannot rename personal team (HTTP $RENAME_PERSONAL_CODE)"
else
  fail "Rename personal team returned $RENAME_PERSONAL_CODE (expected 400 or 403)"
fi

# =====================================================================
# Test 5: Team-scoped resource isolation
# =====================================================================
log "Test 5: Team-scoped resource isolation"

# Create team-a
TEAM_A_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"team-a"}')
TEAM_A_ID=$(echo "$TEAM_A_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$TEAM_A_ID" != "ERROR" ]; then
  pass "Created team-a"
else
  fail "Failed to create team-a: $TEAM_A_RESULT"
fi

# Create a credential scoped to team-a
CRED_A_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_A_ID" \
  -d '{"name":"team-a-cred","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-team-a-key"}')
CRED_A_ID=$(echo "$CRED_A_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$CRED_A_ID" != "ERROR" ]; then
  pass "Created credential in team-a"
else
  fail "Failed to create credential in team-a: $CRED_A_RESULT"
fi

# List credentials with X-Alcove-Team set to team-a — should see the credential
TEAM_A_CREDS=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $TEAM_A_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
names=[c.get('name','') for c in d.get('credentials',[])]
print('yes' if 'team-a-cred' in names else 'no')
")
if [ "$TEAM_A_CREDS" = "yes" ]; then
  pass "Credential visible in team-a scope"
else
  fail "Credential not visible in team-a scope"
fi

# List credentials WITHOUT X-Alcove-Team (defaults to personal team) — should NOT see it
PERSONAL_CREDS=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
names=[c.get('name','') for c in d.get('credentials',[])]
print('yes' if 'team-a-cred' in names else 'no')
")
if [ "$PERSONAL_CREDS" = "no" ]; then
  pass "Credential NOT visible in personal team scope"
else
  fail "Credential visible in personal team scope (should be isolated)"
fi

# Delete team-a (credential should cascade-delete)
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$TEAM_A_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1

# Verify credential is gone after team deletion
CRED_AFTER_DELETE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/credentials/$CRED_A_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$CRED_AFTER_DELETE" = "404" ]; then
  pass "Credential cascade-deleted with team"
else
  fail "Credential still exists after team deletion (HTTP $CRED_AFTER_DELETE)"
fi

# =====================================================================
# Test 6: Cross-team isolation
# =====================================================================
log "Test 6: Cross-team isolation"

# Create team-x and team-y
TEAM_X_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"team-x"}')
TEAM_X_ID=$(echo "$TEAM_X_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")

TEAM_Y_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"team-y"}')
TEAM_Y_ID=$(echo "$TEAM_Y_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")

if [ "$TEAM_X_ID" != "ERROR" ] && [ "$TEAM_Y_ID" != "ERROR" ]; then
  pass "Created team-x and team-y"
else
  fail "Failed to create teams: team-x=$TEAM_X_ID, team-y=$TEAM_Y_ID"
fi

# Create credential with same name in team-x
CRED_X_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_X_ID" \
  -d '{"name":"shared-name-cred","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-team-x-key"}')
CRED_X_CODE=$(echo "$CRED_X_RESULT" | tail -1)
CRED_X_ID=$(echo "$CRED_X_RESULT" | head -1 | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$CRED_X_ID" != "ERROR" ]; then
  pass "Created credential in team-x"
else
  fail "Failed to create credential in team-x (HTTP $CRED_X_CODE)"
fi

# Create credential with same name in team-y — should succeed (unique per team)
CRED_Y_RESULT=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $TEAM_Y_ID" \
  -d '{"name":"shared-name-cred","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-team-y-key"}')
CRED_Y_CODE=$(echo "$CRED_Y_RESULT" | tail -1)
CRED_Y_ID=$(echo "$CRED_Y_RESULT" | head -1 | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$CRED_Y_ID" != "ERROR" ]; then
  pass "Same-name credential allowed in team-y (unique per team)"
else
  fail "Same-name credential rejected in team-y (HTTP $CRED_Y_CODE, expected success)"
fi

# List credentials for team-x — see only team-x's credential
TEAM_X_CRED_IDS=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $TEAM_X_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
ids=[c.get('id','') for c in d.get('credentials',[])]
print(' '.join(ids))
")
if echo "$TEAM_X_CRED_IDS" | grep -q "$CRED_X_ID"; then
  pass "Team-x credential visible in team-x scope"
else
  fail "Team-x credential NOT visible in team-x scope"
fi
if echo "$TEAM_X_CRED_IDS" | grep -q "$CRED_Y_ID"; then
  fail "Team-y credential visible in team-x scope (isolation broken)"
else
  pass "Team-y credential NOT visible in team-x scope"
fi

# List credentials for team-y — see only team-y's credential
TEAM_Y_CRED_IDS=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $TEAM_Y_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
ids=[c.get('id','') for c in d.get('credentials',[])]
print(' '.join(ids))
")
if echo "$TEAM_Y_CRED_IDS" | grep -q "$CRED_Y_ID"; then
  pass "Team-y credential visible in team-y scope"
else
  fail "Team-y credential NOT visible in team-y scope"
fi
if echo "$TEAM_Y_CRED_IDS" | grep -q "$CRED_X_ID"; then
  fail "Team-x credential visible in team-y scope (isolation broken)"
else
  pass "Team-x credential NOT visible in team-y scope"
fi

# Cleanup: delete both teams (credentials should cascade-delete)
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$TEAM_X_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$TEAM_Y_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1

# =====================================================================
# Test 7: Backward compatibility
# =====================================================================
log "Test 7: Backward compatibility"

# Create a credential WITHOUT X-Alcove-Team header (should use personal team)
COMPAT_CRED_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"compat-cred","provider":"anthropic","auth_type":"api_key","credential":"sk-ant-compat-key"}')
COMPAT_CRED_ID=$(echo "$COMPAT_CRED_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$COMPAT_CRED_ID" != "ERROR" ]; then
  pass "Created credential without X-Alcove-Team header"
else
  fail "Failed to create credential without X-Alcove-Team: $COMPAT_CRED_RESULT"
fi

# List credentials without X-Alcove-Team — should see the credential
COMPAT_LIST=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
names=[c.get('name','') for c in d.get('credentials',[])]
print('yes' if 'compat-cred' in names else 'no')
")
if [ "$COMPAT_LIST" = "yes" ]; then
  pass "Credential visible without X-Alcove-Team header (personal team default)"
else
  fail "Credential not visible without X-Alcove-Team header"
fi

# Create a schedule WITHOUT X-Alcove-Team header (should use personal team)
COMPAT_SCHED_RESULT=$(curl -s -X POST "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"compat-schedule","cron":"0 * * * *","prompt":"backward compat test","enabled":false}')
COMPAT_SCHED_ID=$(echo "$COMPAT_SCHED_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")
if [ "$COMPAT_SCHED_ID" != "ERROR" ]; then
  pass "Created schedule without X-Alcove-Team header"
else
  fail "Failed to create schedule without X-Alcove-Team: $COMPAT_SCHED_RESULT"
fi

# List schedules without X-Alcove-Team — should see the schedule
COMPAT_SCHED_LIST=$(curl -s "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
names=[s.get('name','') for s in d.get('schedules',[])]
print('yes' if 'compat-schedule' in names else 'no')
")
if [ "$COMPAT_SCHED_LIST" = "yes" ]; then
  pass "Schedule visible without X-Alcove-Team header (personal team default)"
else
  fail "Schedule not visible without X-Alcove-Team header"
fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/credentials/$COMPAT_CRED_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1
curl -s -X DELETE "$BRIDGE_URL/api/v1/schedules/$COMPAT_SCHED_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1

# =====================================================================
# Test 8: Credential scoping — no team header must NOT leak all creds
# =====================================================================
log "Test 8: No-header credential leak prevention"

# Create a shared team and put a credential in it
LEAK_TEAM=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"leak-test-team"}')
LEAK_TEAM_ID=$(echo "$LEAK_TEAM" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id','ERROR'))")

curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $LEAK_TEAM_ID" \
  -d '{"name":"leak-test-cred","provider":"anthropic","auth_type":"api_key","credential":"sk-leak-test"}' > /dev/null

# Also put a credential in the personal team
curl -s -X POST "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" \
  -d '{"name":"personal-only-cred","provider":"anthropic","auth_type":"api_key","credential":"sk-personal"}' > /dev/null

# Request credentials WITHOUT team header — should only show personal, not shared
NO_HEADER_CREDS=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN")
NO_HEADER_NAMES=$(echo "$NO_HEADER_CREDS" | python3 -c "
import json,sys
d=json.load(sys.stdin)
names=[c.get('name','') for c in d.get('credentials',[])]
print(','.join(names))
")
NO_HEADER_COUNT=$(echo "$NO_HEADER_CREDS" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('credentials',[])))")

if echo "$NO_HEADER_NAMES" | grep -q "leak-test-cred"; then
  fail "DATA LEAK: shared team credential visible without team header ($NO_HEADER_NAMES)"
else
  pass "Shared team credential not visible without team header"
fi

if echo "$NO_HEADER_NAMES" | grep -q "personal-only-cred"; then
  pass "Personal credential visible without team header (correct default)"
else
  fail "Personal credential not visible without team header"
fi

# Request with shared team header — should only show shared cred
SHARED_HEADER_NAMES=$(curl -s "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $LEAK_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
names=[c.get('name','') for c in d.get('credentials',[])]
print(','.join(names))
")

if echo "$SHARED_HEADER_NAMES" | grep -q "leak-test-cred"; then
  pass "Shared team credential visible with shared team header"
else
  fail "Shared team credential not visible with its own team header"
fi

if echo "$SHARED_HEADER_NAMES" | grep -q "personal-only-cred"; then
  fail "Personal credential leaked into shared team view"
else
  pass "Personal credential not visible in shared team view"
fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$LEAK_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1

# =====================================================================
# Test 9: Session scoping — sessions belong to the team they were dispatched for
# =====================================================================
log "Test 9: Session scoping"

# List sessions with personal team header
PERSONAL_SESSIONS=$(curl -s "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(d.get('count', len(d.get('sessions',[]))))")

# No team header should default to personal team and return same count
DEFAULT_SESSIONS=$(curl -s "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(d.get('count', len(d.get('sessions',[]))))")

if [ "$PERSONAL_SESSIONS" = "$DEFAULT_SESSIONS" ]; then
  pass "No-header sessions match personal team sessions ($PERSONAL_SESSIONS)"
else
  fail "No-header sessions ($DEFAULT_SESSIONS) != personal team sessions ($PERSONAL_SESSIONS)"
fi

# =====================================================================
# Test 10: Workflow scoping
# =====================================================================
log "Test 10: Workflow scoping"

# Configure alcove repo on personal team, sync, and check workflows
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" \
  -d '{"repos":[{"url":"https://github.com/bmbouter/alcove/","ref":"main","name":"alcove"}]}' > /dev/null

curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" > /dev/null
sleep 8

PERSONAL_WFS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('workflows',[])))")

if [ "$PERSONAL_WFS" -gt "0" ]; then
  pass "Workflows synced to personal team ($PERSONAL_WFS workflows)"
else
  fail "No workflows synced to personal team"
fi

# Create a second team and verify it has 0 workflows
WF_TEAM=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"wf-isolation-team"}')
WF_TEAM_ID=$(echo "$WF_TEAM" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id',''))")

OTHER_WFS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $WF_TEAM_ID" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('workflows',[])))")

if [ "$OTHER_WFS" = "0" ]; then
  pass "Other team has 0 workflows (not leaked from personal)"
else
  fail "Other team has $OTHER_WFS workflows (expected 0)"
fi

# No-header should return personal team's workflows
DEFAULT_WFS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('workflows',[])))")

if [ "$DEFAULT_WFS" = "$PERSONAL_WFS" ]; then
  pass "No-header workflows match personal team ($DEFAULT_WFS)"
else
  fail "No-header workflows ($DEFAULT_WFS) != personal ($PERSONAL_WFS)"
fi

# Cleanup
curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$WF_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" \
  -d '{"repos":[]}' > /dev/null

# =====================================================================
# Test 11: Agent definition scoping
# =====================================================================
log "Test 11: Agent definition and schedule scoping after sync"

# After the sync above, agent defs should be on personal team only
PERSONAL_DEFS=$(curl -s "$BRIDGE_URL/api/v1/agent-definitions" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('agent_definitions',[])))")

DEFAULT_DEFS=$(curl -s "$BRIDGE_URL/api/v1/agent-definitions" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('agent_definitions',[])))")

if [ "$PERSONAL_DEFS" -gt "0" ]; then
  pass "Agent definitions synced ($PERSONAL_DEFS defs on personal team)"
else
  fail "No agent definitions synced to personal team"
fi

if [ "$DEFAULT_DEFS" = "$PERSONAL_DEFS" ]; then
  pass "No-header defs match personal team ($DEFAULT_DEFS)"
else
  fail "No-header defs ($DEFAULT_DEFS) != personal ($PERSONAL_DEFS)"
fi

# Schedules
PERSONAL_SCHEDS=$(curl -s "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $PERSONAL_TEAM_ID" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('schedules',[]) or []))")

DEFAULT_SCHEDS=$(curl -s "$BRIDGE_URL/api/v1/schedules" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys; d=json.load(sys.stdin); print(len(d.get('schedules',[]) or []))")

if [ "$DEFAULT_SCHEDS" = "$PERSONAL_SCHEDS" ]; then
  pass "No-header schedules match personal team ($DEFAULT_SCHEDS)"
else
  fail "No-header schedules ($DEFAULT_SCHEDS) != personal ($PERSONAL_SCHEDS)"
fi

# =====================================================================
# Test 12: Cache-Control header on API responses
# =====================================================================
log "Test 12: Cache-Control headers"

CC=$(curl -sI "$BRIDGE_URL/api/v1/credentials" \
  -H "Authorization: Bearer $ALICE_TOKEN" | grep -i "cache-control" | tr -d '\r')
if echo "$CC" | grep -qi "no-store"; then
  pass "Cache-Control: no-store on credentials API"
else
  fail "Missing Cache-Control: no-store header on credentials API ($CC)"
fi

CC2=$(curl -sI "$BRIDGE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $ALICE_TOKEN" | grep -i "cache-control" | tr -d '\r')
if echo "$CC2" | grep -qi "no-store"; then
  pass "Cache-Control: no-store on sessions API"
else
  fail "Missing Cache-Control: no-store header on sessions API ($CC2)"
fi

# =====================================================================
# Test 13: Member validation — cannot add non-existent user
# =====================================================================
log "Test 13: Member validation"

VALIDATION_TEAM=$(curl -s -X POST "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"validation-test"}')
VALIDATION_TEAM_ID=$(echo "$VALIDATION_TEAM" | python3 -c "import json,sys; print(json.load(sys.stdin).get('id',''))")

ADD_FAKE_RESP=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/teams/$VALIDATION_TEAM_ID/members" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"does_not_exist_xyz"}')
ADD_FAKE_CODE=$(echo "$ADD_FAKE_RESP" | tail -1)

if [ "$ADD_FAKE_CODE" = "400" ] || [ "$ADD_FAKE_CODE" = "404" ]; then
  pass "Cannot add non-existent user (HTTP $ADD_FAKE_CODE)"
else
  fail "Adding non-existent user returned HTTP $ADD_FAKE_CODE (expected 400 or 404)"
fi

curl -s -X DELETE "$BRIDGE_URL/api/v1/teams/$VALIDATION_TEAM_ID" \
  -H "Authorization: Bearer $ALICE_TOKEN" > /dev/null 2>&1

# =====================================================================
# Test 14: Removed endpoints return 404
# =====================================================================
log "Test 14: Removed endpoints"

SKILL_ADMIN_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/admin/settings/skill-repos" \
  -H "Authorization: Bearer $ADMIN_TOKEN")
if [ "$SKILL_ADMIN_CODE" = "404" ]; then
  pass "admin/settings/skill-repos removed (HTTP 404)"
else
  fail "admin/settings/skill-repos still exists (HTTP $SKILL_ADMIN_CODE)"
fi

SKILL_USER_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/user/settings/skill-repos" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$SKILL_USER_CODE" = "404" ]; then
  pass "user/settings/skill-repos removed (HTTP 404)"
else
  fail "user/settings/skill-repos still exists (HTTP $SKILL_USER_CODE)"
fi

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
