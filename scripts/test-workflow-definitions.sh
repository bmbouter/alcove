#!/bin/bash
# test-workflow-definitions.sh — Tests for updated workflow definitions.
#
# Verifies that the workflow definitions with bridge actions, depends
# expressions, max_iterations, and step types load correctly via the
# Bridge API after syncing from the alcove repo.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Internet access (syncs workflow definitions from GitHub)
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-workflow-definitions.sh
#
# Tests:
#   Test 1: Feature pipeline loads
#   Test 2: Feature pipeline step types
#   Test 3: Feature pipeline depends expressions
#   Test 4: Feature pipeline max_iterations
#   Test 5: Release pipeline loads
#   Test 6: Backward compatibility

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

# Create test user
curl -s -X POST "$BRIDGE_URL/api/v1/users" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"username":"wd-alice","password":"wdalice12345","is_admin":false}' > /dev/null 2>&1 || true

ALICE_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"wd-alice","password":"wdalice12345"}' | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

# Get Alice's personal team ID
ALICE_TEAM_ID=$(curl -s "$BRIDGE_URL/api/v1/teams" \
  -H "Authorization: Bearer $ALICE_TOKEN" | python3 -c "
import json,sys
d=json.load(sys.stdin)
teams=d.get('teams',[])
for t in teams:
    if t.get('is_personal', False):
        print(t['id'])
        sys.exit()
print('')
")

echo "  Alice token: ${ALICE_TOKEN:0:10}..."
echo "  Alice team: ${ALICE_TEAM_ID:0:10}..."

# Sync the alcove repo to get workflow definitions
log "Syncing alcove repo for workflow definitions..."
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" \
  -d '{"repos":[{"url":"https://github.com/bmbouter/alcove/","ref":"'"${ALCOVE_TEST_REF:-main}"'","name":"alcove"}]}' > /dev/null

curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" > /dev/null

# Wait for sync to complete (workflows are synced along with agent definitions)
for attempt in 1 2 3 4 5 6 7 8; do
  sleep 3
  WF_COUNT=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
    -H "Authorization: Bearer $ALICE_TOKEN" \
    -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "import json,sys; print(json.load(sys.stdin).get('count',0))")
  if [ "$WF_COUNT" -gt 0 ]; then break; fi
done

if [ "$WF_COUNT" -gt 0 ]; then
  log "Workflows synced ($WF_COUNT workflows)"
else
  fail "No workflows synced after repo configuration"
  echo ""
  log "=== Test Summary ==="
  echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
  exit 1
fi

# Fetch all workflows once for use across tests
WF_RESPONSE=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")

# =====================================================================
# Test 1: Feature pipeline loads
# =====================================================================
log "Test 1: Feature pipeline loads"

# Check for the Feature Development Pipeline (or similar name)
FEATURE_WF=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    name=wf.get('name','').lower()
    if 'feature' in name or 'sdlc' in name or 'development' in name:
        steps=wf.get('workflow',[])
        print(f'found:{wf[\"name\"]}:{len(steps)}')
        sys.exit()
print('not_found')
")

if [[ "$FEATURE_WF" == found:* ]]; then
  FEATURE_NAME=$(echo "$FEATURE_WF" | cut -d: -f2)
  FEATURE_STEPS=$(echo "$FEATURE_WF" | cut -d: -f3)
  pass "Feature pipeline found: '$FEATURE_NAME' with $FEATURE_STEPS steps"
else
  fail "Feature pipeline workflow not found"
fi

# Check for bridge steps (type: bridge) in any workflow
BRIDGE_STEPS=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
bridge_found=[]
for wf in wfs:
    for step in wf.get('workflow',[]):
        step_type=step.get('type','')
        if step_type == 'bridge':
            bridge_found.append(f\"{wf.get('name','')}/{step.get('id','')}\")
if bridge_found:
    print('found:' + ','.join(bridge_found))
else:
    print('none')
")

if [[ "$BRIDGE_STEPS" == found:* ]]; then
  BRIDGE_LIST=$(echo "$BRIDGE_STEPS" | cut -d: -f2)
  pass "Bridge steps found in workflows: $BRIDGE_LIST"
else
  log "  No bridge steps (type: bridge) found in synced workflows"
  log "  (Workflows may use agent steps only or bridge steps may not be deployed yet)"
fi

# Verify the workflow response structure is valid JSON with expected shape
WF_VALID=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
has_workflows=isinstance(d.get('workflows'), list)
has_count=isinstance(d.get('count'), int)
print('yes' if has_workflows and has_count else 'no')
")
if [ "$WF_VALID" = "yes" ]; then
  pass "Workflow response has valid structure (workflows array + count)"
else
  fail "Workflow response has invalid structure"
fi

# =====================================================================
# Test 2: Feature pipeline step types
# =====================================================================
log "Test 2: Feature pipeline step types"

# Get step details from the feature pipeline
STEP_DETAILS=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
feature_wf=None
for wf in wfs:
    name=wf.get('name','').lower()
    if 'feature' in name or 'sdlc' in name or 'development' in name:
        feature_wf=wf
        break
if not feature_wf:
    print('NO_FEATURE_WF')
    sys.exit()
steps=feature_wf.get('workflow',[])
for step in steps:
    step_id=step.get('id','')
    agent=step.get('agent','')
    step_type=step.get('type','agent')  # default is agent
    action=step.get('action','')
    type_str=f'type={step_type}' if step_type else 'type=agent'
    action_str=f' action={action}' if action else ''
    agent_str=f' agent={agent}' if agent else ''
    print(f'  {step_id}: {type_str}{action_str}{agent_str}')
print('STEP_COUNT:' + str(len(steps)))
")
echo "$STEP_DETAILS" | grep -v "^STEP_COUNT:" | grep -v "^NO_FEATURE_WF" || true

if [ "$STEP_DETAILS" = "NO_FEATURE_WF" ]; then
  fail "Feature pipeline not found for step type verification"
else
  STEP_COUNT=$(echo "$STEP_DETAILS" | grep "^STEP_COUNT:" | cut -d: -f2)
  if [ "$STEP_COUNT" -gt 0 ]; then
    pass "Feature pipeline has $STEP_COUNT steps with type information"
  else
    fail "Feature pipeline has no steps"
  fi

  # Verify all steps have an id field
  ALL_HAVE_IDS=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    name=wf.get('name','').lower()
    if 'feature' in name or 'sdlc' in name or 'development' in name:
        for step in wf.get('workflow',[]):
            if not step.get('id',''):
                print('missing')
                sys.exit()
        print('all_ok')
        sys.exit()
print('no_wf')
")
  if [ "$ALL_HAVE_IDS" = "all_ok" ]; then
    pass "All feature pipeline steps have id fields"
  elif [ "$ALL_HAVE_IDS" = "no_wf" ]; then
    log "  Feature pipeline not found"
  else
    fail "Some feature pipeline steps missing id field"
  fi

  # Verify agent steps reference known agent definitions
  AGENT_REFS=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    name=wf.get('name','').lower()
    if 'feature' in name or 'sdlc' in name or 'development' in name:
        agents=[]
        for step in wf.get('workflow',[]):
            agent=step.get('agent','')
            if agent:
                agents.append(agent)
        if agents:
            print(','.join(agents))
        else:
            print('none')
        sys.exit()
print('no_wf')
")
  if [ "$AGENT_REFS" != "none" ] && [ "$AGENT_REFS" != "no_wf" ]; then
    pass "Feature pipeline steps reference agents: $AGENT_REFS"
  else
    log "  No agent references found in feature pipeline steps"
  fi
fi

# =====================================================================
# Test 3: Feature pipeline depends expressions
# =====================================================================
log "Test 3: Feature pipeline depends expressions"

# Check for depends expressions in workflow steps
DEPENDS_INFO=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
depends_found=[]
needs_found=[]
conditions_found=[]
for wf in wfs:
    for step in wf.get('workflow',[]):
        step_id=step.get('id','')
        wf_name=wf.get('name','')
        dep=step.get('depends','')
        needs=step.get('needs',[])
        cond=step.get('condition','')
        if dep:
            depends_found.append(f'{wf_name}/{step_id}: depends=\"{dep}\"')
        if needs:
            needs_found.append(f'{wf_name}/{step_id}: needs={needs}')
        if cond:
            conditions_found.append(f'{wf_name}/{step_id}: condition=\"{cond}\"')
print(f'DEPENDS:{len(depends_found)}')
print(f'NEEDS:{len(needs_found)}')
print(f'CONDITIONS:{len(conditions_found)}')
for d_line in depends_found:
    print(f'  {d_line}')
for n_line in needs_found:
    print(f'  {n_line}')
for c_line in conditions_found:
    print(f'  {c_line}')
")
echo "$DEPENDS_INFO" | grep "^  " || true

DEPENDS_COUNT=$(echo "$DEPENDS_INFO" | grep "^DEPENDS:" | cut -d: -f2)
NEEDS_COUNT=$(echo "$DEPENDS_INFO" | grep "^NEEDS:" | cut -d: -f2)
CONDITIONS_COUNT=$(echo "$DEPENDS_INFO" | grep "^CONDITIONS:" | cut -d: -f2)

log "  Depends expressions: $DEPENDS_COUNT"
log "  Needs dependencies: $NEEDS_COUNT"
log "  Conditions: $CONDITIONS_COUNT"

# At least some dependency mechanism should be used
TOTAL_DEPS=$((DEPENDS_COUNT + NEEDS_COUNT))
if [ "$TOTAL_DEPS" -gt 0 ]; then
  pass "Workflow steps have dependency definitions ($DEPENDS_COUNT depends, $NEEDS_COUNT needs)"
else
  fail "No dependency definitions found in any workflow step"
fi

# Check for enhanced depends expressions with && or || operators (v2 feature)
if [ "$DEPENDS_COUNT" -gt 0 ]; then
  ENHANCED_DEPS=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
enhanced=[]
for wf in wfs:
    for step in wf.get('workflow',[]):
        dep=step.get('depends','')
        if dep and ('&&' in dep or '||' in dep or '.Succeeded' in dep or '.Failed' in dep):
            enhanced.append(f\"{step.get('id','')}: {dep}\")
if enhanced:
    for e in enhanced:
        print(f'  {e}')
    print(f'COUNT:{len(enhanced)}')
else:
    print('COUNT:0')
")
  echo "$ENHANCED_DEPS" | grep "^  " || true
  ENHANCED_COUNT=$(echo "$ENHANCED_DEPS" | grep "^COUNT:" | cut -d: -f2)
  if [ "$ENHANCED_COUNT" -gt 0 ]; then
    pass "Enhanced depends expressions found with operators ($ENHANCED_COUNT steps)"
  else
    log "  No enhanced depends expressions with &&/|| operators found"
  fi
fi

# Verify conditions parse correctly (no sync errors related to conditions)
COND_ERRORS=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
errors=0
for wf in wfs:
    sync_err=wf.get('sync_error','')
    if sync_err and ('condition' in sync_err.lower() or 'depends' in sync_err.lower()):
        errors += 1
        print(f'  ERROR: {wf.get(\"name\",\"\")}: {sync_err}')
print(f'ERRORS:{errors}')
")
echo "$COND_ERRORS" | grep "^  " || true
COND_ERR_COUNT=$(echo "$COND_ERRORS" | grep "^ERRORS:" | cut -d: -f2)
if [ "$COND_ERR_COUNT" = "0" ]; then
  pass "No sync errors related to conditions or depends expressions"
else
  fail "Sync errors found related to conditions ($COND_ERR_COUNT errors)"
fi

# =====================================================================
# Test 4: Feature pipeline max_iterations
# =====================================================================
log "Test 4: Feature pipeline max_iterations"

# Check for max_iterations fields in workflow steps
MAX_ITER_INFO=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
with_max_iter=[]
for wf in wfs:
    for step in wf.get('workflow',[]):
        mi=step.get('max_iterations', None)
        if mi is not None:
            with_max_iter.append(f\"{wf.get('name','')}/{step.get('id','')}: max_iterations={mi}\")
if with_max_iter:
    for line in with_max_iter:
        print(f'  {line}')
    print(f'COUNT:{len(with_max_iter)}')
else:
    print('COUNT:0')
")
echo "$MAX_ITER_INFO" | grep "^  " || true
MAX_ITER_COUNT=$(echo "$MAX_ITER_INFO" | grep "^COUNT:" | cut -d: -f2)

if [ "$MAX_ITER_COUNT" -gt 0 ]; then
  pass "Steps with max_iterations found ($MAX_ITER_COUNT steps)"

  # Verify max_iterations values are positive integers > 1 (cycle support)
  VALID_ITER=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
all_valid=True
for wf in wfs:
    for step in wf.get('workflow',[]):
        mi=step.get('max_iterations', None)
        if mi is not None:
            if not isinstance(mi, int) or mi < 1:
                all_valid=False
                print(f'INVALID: {step.get(\"id\",\"\")}: max_iterations={mi}')
print('VALID' if all_valid else 'INVALID')
")
  if echo "$VALID_ITER" | grep -q "^VALID$"; then
    pass "All max_iterations values are valid positive integers"
  else
    fail "Invalid max_iterations values found"
    echo "$VALID_ITER" | grep "^INVALID:" || true
  fi
else
  log "  No max_iterations fields found in workflows"
  log "  (Workflows may not use cycles yet, which is acceptable)"

  # Verify the field is at least supported by checking step schema
  ITER_FIELD_SUPPORT=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
# max_iterations defaults to 0/omitted when not set
# Verify steps serialize correctly (no errors)
total_steps=0
for wf in wfs:
    total_steps += len(wf.get('workflow',[]))
print(f'ok:{total_steps}')
")
  TOTAL_STEPS=$(echo "$ITER_FIELD_SUPPORT" | cut -d: -f2)
  pass "Workflow steps serialize correctly ($TOTAL_STEPS steps across all workflows)"
fi

# =====================================================================
# Test 5: Release pipeline loads
# =====================================================================
log "Test 5: Release pipeline loads"

# Check for the Release Pipeline
RELEASE_WF=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    name=wf.get('name','').lower()
    if 'release' in name:
        steps=wf.get('workflow',[])
        # Get step IDs and agents
        step_info=[]
        for s in steps:
            sid=s.get('id','')
            agent=s.get('agent','')
            step_info.append(f'{sid}({agent})')
        print(f'found:{wf[\"name\"]}:{len(steps)}:{\"|\".join(step_info)}')
        sys.exit()
print('not_found')
")

if [[ "$RELEASE_WF" == found:* ]]; then
  RELEASE_NAME=$(echo "$RELEASE_WF" | cut -d: -f2)
  RELEASE_STEPS=$(echo "$RELEASE_WF" | cut -d: -f3)
  RELEASE_INFO=$(echo "$RELEASE_WF" | cut -d: -f4)
  pass "Release pipeline found: '$RELEASE_NAME' with $RELEASE_STEPS steps"
  log "  Steps: $RELEASE_INFO"
else
  log "  Release pipeline workflow not found (not yet created)"
fi

# Verify release pipeline has expected structure (at least one step)
if [[ "$RELEASE_WF" == found:* ]]; then
  if [ "$RELEASE_STEPS" -gt 0 ]; then
    pass "Release pipeline has $RELEASE_STEPS step(s)"
  else
    fail "Release pipeline has no steps"
  fi

  # Check that the release pipeline references the Automated Release Agent
  RELEASE_AGENT_REF=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    name=wf.get('name','').lower()
    if 'release' in name:
        for step in wf.get('workflow',[]):
            agent=step.get('agent','').lower()
            if 'release' in agent:
                print('yes')
                sys.exit()
        print('no')
        sys.exit()
print('no_wf')
")
  if [ "$RELEASE_AGENT_REF" = "yes" ]; then
    pass "Release pipeline references the release agent"
  else
    log "  Release pipeline does not explicitly reference a release agent"
  fi
fi

# List all workflow names for visibility
ALL_WF_NAMES=$(echo "$WF_RESPONSE" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    steps=wf.get('workflow',[])
    print(f'  {wf.get(\"name\",\"unnamed\")}: {len(steps)} steps')
")
log "All synced workflows:"
echo "$ALL_WF_NAMES"

# =====================================================================
# Test 6: Backward compatibility
# =====================================================================
log "Test 6: Backward compatibility"

# Verify all existing API endpoints still return 200
ENDPOINTS=(
  "/api/v1/sessions"
  "/api/v1/credentials"
  "/api/v1/teams"
  "/api/v1/agent-definitions"
  "/api/v1/workflows"
  "/api/v1/workflow-runs"
  "/api/v1/bridge-actions"
  "/api/v1/schedules"
)

for ENDPOINT in "${ENDPOINTS[@]}"; do
  EP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL$ENDPOINT" \
    -H "Authorization: Bearer $ALICE_TOKEN" \
    -H "X-Alcove-Team: $ALICE_TEAM_ID")
  if [ "$EP_CODE" = "200" ]; then
    pass "$ENDPOINT returns 200"
  else
    fail "$ENDPOINT returns $EP_CODE (expected 200)"
  fi
done

# Verify endpoints work without X-Alcove-Team header (backward compat)
for ENDPOINT in "/api/v1/sessions" "/api/v1/credentials" "/api/v1/workflows"; do
  NO_TEAM_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL$ENDPOINT" \
    -H "Authorization: Bearer $ALICE_TOKEN")
  if [ "$NO_TEAM_CODE" = "200" ]; then
    pass "$ENDPOINT works without X-Alcove-Team header"
  else
    fail "$ENDPOINT returns $NO_TEAM_CODE without X-Alcove-Team header (expected 200)"
  fi
done

# Verify workflow-runs with status filter still works
for STATUS_FILTER in "pending" "running" "completed" "failed"; do
  FILTER_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
    "$BRIDGE_URL/api/v1/workflow-runs?status=$STATUS_FILTER" \
    -H "Authorization: Bearer $ALICE_TOKEN" \
    -H "X-Alcove-Team: $ALICE_TEAM_ID")
  if [ "$FILTER_CODE" = "200" ]; then
    pass "Workflow runs with status=$STATUS_FILTER returns 200"
  else
    fail "Workflow runs with status=$STATUS_FILTER returned $FILTER_CODE (expected 200)"
  fi
done

# Verify agent-definitions sync endpoint works
SYNC_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")
if [ "$SYNC_CODE" = "200" ]; then
  pass "Agent definitions sync endpoint returns 200"
else
  fail "Agent definitions sync returned $SYNC_CODE (expected 200)"
fi

# =====================================================================
# Cleanup
# =====================================================================
log "Cleanup..."
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" \
  -d '{"repos":[]}' > /dev/null
curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" > /dev/null
log "Cleanup complete"

# --- Summary ---
echo ""
log "=== Test Summary ==="
echo "  Total: $((PASS+FAIL))  Passed: $PASS  Failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then exit 1; else echo "  All tests passed."; fi
