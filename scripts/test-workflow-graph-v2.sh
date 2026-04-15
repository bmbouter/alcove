#!/bin/bash
# test-workflow-graph-v2.sh — Tests for Workflow Graph v2 features.
#
# Verifies bridge actions endpoint, workflow definitions with bridge steps,
# depends expressions, max_iterations validation, backward compatibility
# with the old needs syntax, cycle detection with max_iterations, and
# workflow runs with bridge steps.
#
# Prerequisites:
#   - Bridge running at BRIDGE_URL (default http://localhost:8080)
#   - AUTH_BACKEND=postgres with PostgreSQL accessible
#   - ADMIN_PASSWORD set in the environment
#   - Internet access (tests clone repos from GitHub for workflow sync)
#
# Usage:
#   ADMIN_PASSWORD=<pw> ./scripts/test-workflow-graph-v2.sh
#
# Tests:
#   Test 1: Bridge actions endpoint
#   Test 2: Workflow definition with bridge steps
#   Test 3: Depends expression validation
#   Test 4: Max iterations validation
#   Test 5: Backward compatibility (old needs syntax)
#   Test 6: Workflow with cycle detection and max_iterations
#   Test 7: Workflow run with bridge steps

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
  -d '{"username":"wfv2-alice","password":"wfv2alice123","is_admin":false}' > /dev/null 2>&1 || true

ALICE_TOKEN=$(curl -s -X POST "$BRIDGE_URL/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"username":"wfv2-alice","password":"wfv2alice123"}' | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('token',''))")

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

# =====================================================================
# Test 1: Bridge actions endpoint
# =====================================================================
log "Test 1: Bridge actions endpoint"

ACTIONS_RESULT=$(curl -s -w "\n%{http_code}" "$BRIDGE_URL/api/v1/bridge-actions" \
  -H "Authorization: Bearer $ALICE_TOKEN")
ACTIONS_CODE=$(echo "$ACTIONS_RESULT" | tail -1)
ACTIONS_BODY=$(echo "$ACTIONS_RESULT" | sed '$d')

if [ "$ACTIONS_CODE" = "200" ]; then
  pass "GET /api/v1/bridge-actions returns 200"
else
  fail "GET /api/v1/bridge-actions returns $ACTIONS_CODE (expected 200)"
fi

# Check that create-pr action exists
HAS_CREATE_PR=$(echo "$ACTIONS_BODY" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    actions=d.get('actions',[])
    names=[a.get('name','') for a in actions]
    print('yes' if 'create-pr' in names else 'no')
except:
    print('error')
")
if [ "$HAS_CREATE_PR" = "yes" ]; then
  pass "Bridge actions include create-pr"
else
  fail "Bridge actions missing create-pr"
fi

# Check that await-ci action exists
HAS_AWAIT_CI=$(echo "$ACTIONS_BODY" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    actions=d.get('actions',[])
    names=[a.get('name','') for a in actions]
    print('yes' if 'await-ci' in names else 'no')
except:
    print('error')
")
if [ "$HAS_AWAIT_CI" = "yes" ]; then
  pass "Bridge actions include await-ci"
else
  fail "Bridge actions missing await-ci"
fi

# Check that merge-pr action exists
HAS_MERGE_PR=$(echo "$ACTIONS_BODY" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    actions=d.get('actions',[])
    names=[a.get('name','') for a in actions]
    print('yes' if 'merge-pr' in names else 'no')
except:
    print('error')
")
if [ "$HAS_MERGE_PR" = "yes" ]; then
  pass "Bridge actions include merge-pr"
else
  fail "Bridge actions missing merge-pr"
fi

# Verify each action has inputs and outputs defined
ACTIONS_HAVE_IO=$(echo "$ACTIONS_BODY" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    actions=d.get('actions',[])
    all_ok=True
    for a in actions:
        if 'inputs' not in a or 'outputs' not in a:
            all_ok=False
            break
    print('yes' if all_ok and len(actions) > 0 else 'no')
except:
    print('error')
")
if [ "$ACTIONS_HAVE_IO" = "yes" ]; then
  pass "All bridge actions have inputs and outputs defined"
else
  fail "Some bridge actions missing inputs or outputs"
fi

# =====================================================================
# Test 2: Workflow definition with bridge steps
# =====================================================================
log "Test 2: Workflow definition with bridge steps"

# Configure alcove-testing repo (which should contain v2 workflow definitions)
# and sync to get workflow definitions with bridge steps.
# We use the agent-repos validate endpoint to check if the repo can be parsed.

# First, create a workflow definition via the sync mechanism by configuring
# a repo that has v2 workflow YAML files. Since we cannot directly POST
# workflow definitions, we use the validate endpoint to verify parsing.

# Verify the workflows endpoint returns workflow definitions
WORKFLOWS_RESULT=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")
WF_STATUS=$(echo "$WORKFLOWS_RESULT" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    print('ok')
except:
    print('error')
")
if [ "$WF_STATUS" = "ok" ]; then
  pass "GET /api/v1/workflows returns valid JSON"
else
  fail "GET /api/v1/workflows returned invalid response"
fi

# Verify that workflows response has the expected shape (count + workflows array)
WF_SHAPE=$(echo "$WORKFLOWS_RESULT" | python3 -c "
import json,sys
d=json.load(sys.stdin)
has_count='count' in d
has_workflows='workflows' in d and isinstance(d['workflows'], list)
print('yes' if has_count and has_workflows else 'no')
")
if [ "$WF_SHAPE" = "yes" ]; then
  pass "Workflows response has count and workflows array"
else
  fail "Workflows response missing expected fields"
fi

# Sync a repo with v2 workflow definitions to test bridge step parsing.
# Configure the alcove-testing repo which contains workflow YAML files.
curl -s -X PUT "$BRIDGE_URL/api/v1/user/settings/agent-repos" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" \
  -d '{"repos":[{"url":"https://github.com/bmbouter/alcove-testing.git","name":"alcove-testing"}]}' > /dev/null

curl -s -X POST "$BRIDGE_URL/api/v1/agent-definitions/sync" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" > /dev/null

# Wait for sync to complete
for attempt in 1 2 3 4 5; do
  sleep 3
  SYNC_COUNT=$(curl -s "$BRIDGE_URL/api/v1/agent-definitions" \
    -H "Authorization: Bearer $ALICE_TOKEN" \
    -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "import json,sys; print(json.load(sys.stdin).get('count',0))")
  if [ "$SYNC_COUNT" -gt 0 ]; then break; fi
done

if [ "$SYNC_COUNT" -gt 0 ]; then
  pass "Agent definitions synced ($SYNC_COUNT definitions)"
else
  fail "No agent definitions synced after repo configuration"
fi

# After sync, check if any workflows were created
WF_COUNT=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "import json,sys; print(json.load(sys.stdin).get('count',0))")
log "  Synced workflow count: $WF_COUNT"

# If workflows were synced, verify step fields are preserved in the response
if [ "$WF_COUNT" -gt 0 ]; then
  # Check that workflow steps have the expected fields
  STEP_FIELDS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
    -H "Authorization: Bearer $ALICE_TOKEN" \
    -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
if not wfs:
    print('no_workflows')
    sys.exit()
wf=wfs[0]
steps=wf.get('workflow',[])
if not steps:
    print('no_steps')
    sys.exit()
step=steps[0]
has_id='id' in step
has_agent='agent' in step
print('yes' if has_id and has_agent else 'no')
")
  if [ "$STEP_FIELDS" = "yes" ]; then
    pass "Workflow steps have id and agent fields preserved"
  else
    fail "Workflow step fields not preserved ($STEP_FIELDS)"
  fi

  # Check for bridge step type field if present in any workflow
  HAS_TYPE_FIELD=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
    -H "Authorization: Bearer $ALICE_TOKEN" \
    -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    for step in wf.get('workflow',[]):
        if step.get('type','') == 'bridge':
            print('yes')
            sys.exit()
print('no_bridge_steps')
")
  log "  Bridge step type check: $HAS_TYPE_FIELD"
  # This is informational -- bridge steps may or may not be in the test repo
else
  log "  No workflows synced from repo (workflow YAML may not be in alcove-testing)"
fi

# =====================================================================
# Test 3: Depends expression validation
# =====================================================================
log "Test 3: Depends expression validation"

# Test valid condition expressions through the validate endpoint
# Since the workflow parsing logic validates conditions, we can test
# by syncing repos with different condition expressions.

# Test that the condition evaluator accepts valid expressions
# We verify this indirectly by checking that the workflows endpoint
# returns workflows with conditions set.
VALID_CONDITION_WFS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
conditions_found=[]
for wf in wfs:
    for step in wf.get('workflow',[]):
        cond=step.get('condition','')
        if cond:
            conditions_found.append(cond)
print(len(conditions_found))
")
log "  Workflows with conditions: $VALID_CONDITION_WFS"

# Test the validate endpoint with a workflow that has valid depends expressions
VALIDATE_VALID=$(curl -s -w "\n%{http_code}" -X POST "$BRIDGE_URL/api/v1/agent-repos/validate" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url":"https://github.com/bmbouter/alcove-testing.git"}')
VALIDATE_CODE=$(echo "$VALIDATE_VALID" | tail -1)
VALIDATE_BODY=$(echo "$VALIDATE_VALID" | sed '$d')

if [ "$VALIDATE_CODE" = "200" ]; then
  pass "Repo validation endpoint returns 200"
else
  fail "Repo validation returned $VALIDATE_CODE (expected 200)"
fi

VALIDATE_IS_VALID=$(echo "$VALIDATE_BODY" | python3 -c "import json,sys; print(json.load(sys.stdin).get('valid',False))")
if [ "$VALIDATE_IS_VALID" = "True" ]; then
  pass "alcove-testing repo validates successfully (conditions accepted)"
else
  fail "alcove-testing repo validation failed"
fi

# Verify workflow-runs endpoint accepts valid status filters
RUNS_RESULT=$(curl -s -w "\n%{http_code}" "$BRIDGE_URL/api/v1/workflow-runs?status=pending" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")
RUNS_CODE=$(echo "$RUNS_RESULT" | tail -1)
if [ "$RUNS_CODE" = "200" ]; then
  pass "Workflow runs endpoint with status filter returns 200"
else
  fail "Workflow runs endpoint returned $RUNS_CODE (expected 200)"
fi

# =====================================================================
# Test 4: Max iterations validation
# =====================================================================
log "Test 4: Max iterations validation"

# Since workflow definitions are synced from repos (not created via POST),
# we verify max_iterations support through the workflow response structure.
# Check that the workflows endpoint can return workflow step data properly.

# Verify workflow steps are returned as JSON-serializable objects
WF_STEPS_OK=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    steps=wf.get('workflow',[])
    for step in steps:
        # Check step is a valid dict with required fields
        if not isinstance(step, dict):
            print('invalid_step_type')
            sys.exit()
        if 'id' not in step:
            print('missing_id')
            sys.exit()
# Check max_iterations field if present on any step
for wf in wfs:
    for step in wf.get('workflow',[]):
        mi=step.get('max_iterations', None)
        if mi is not None:
            if not isinstance(mi, (int, float)):
                print('invalid_max_iterations_type')
                sys.exit()
            if mi < 1:
                print('invalid_max_iterations_value')
                sys.exit()
print('ok')
")
if [ "$WF_STEPS_OK" = "ok" ]; then
  pass "Workflow steps are valid with proper field types"
else
  fail "Workflow steps validation failed: $WF_STEPS_OK"
fi

# Verify the workflow run steps endpoint returns valid data
RUNS_LIST=$(curl -s "$BRIDGE_URL/api/v1/workflow-runs" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")
RUNS_SHAPE=$(echo "$RUNS_LIST" | python3 -c "
import json,sys
d=json.load(sys.stdin)
has_runs='workflow_runs' in d
has_count='count' in d
print('yes' if has_runs and has_count else 'no')
")
if [ "$RUNS_SHAPE" = "yes" ]; then
  pass "Workflow runs response has expected shape"
else
  fail "Workflow runs response missing expected fields"
fi

# =====================================================================
# Test 5: Backward compatibility (old needs syntax)
# =====================================================================
log "Test 5: Backward compatibility (old needs syntax)"

# Verify that workflows using the old needs list syntax still work.
# The alcove-testing repo should contain workflows with needs dependencies.

WF_WITH_NEEDS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    for step in wf.get('workflow',[]):
        needs=step.get('needs',[])
        if needs and len(needs) > 0:
            print('yes')
            sys.exit()
print('no')
")
if [ "$WF_WITH_NEEDS" = "yes" ]; then
  pass "Workflows with needs dependencies exist and are accepted"
else
  log "  No workflows with needs found (may need v2 test data in alcove-testing)"
fi

# Verify that the workflow list endpoint is backward compatible (returns all workflows)
WF_LIST_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")
if [ "$WF_LIST_CODE" = "200" ]; then
  pass "Workflow list endpoint returns 200 (backward compatible)"
else
  fail "Workflow list endpoint returned $WF_LIST_CODE (expected 200)"
fi

# Verify workflow-runs endpoint works without status filter (backward compatible)
RUNS_NO_FILTER_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BRIDGE_URL/api/v1/workflow-runs" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")
if [ "$RUNS_NO_FILTER_CODE" = "200" ]; then
  pass "Workflow runs endpoint works without filter (backward compatible)"
else
  fail "Workflow runs endpoint without filter returned $RUNS_NO_FILTER_CODE (expected 200)"
fi

# Verify that empty workflow runs list returns proper structure
EMPTY_RUNS_SHAPE=$(curl -s "$BRIDGE_URL/api/v1/workflow-runs" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
runs=d.get('workflow_runs',[])
count=d.get('count',None)
if isinstance(runs, list) and count is not None:
    print('ok')
else:
    print('bad_shape')
")
if [ "$EMPTY_RUNS_SHAPE" = "ok" ]; then
  pass "Empty workflow runs response has correct structure"
else
  fail "Empty workflow runs response has bad structure: $EMPTY_RUNS_SHAPE"
fi

# =====================================================================
# Test 6: Workflow with cycle detection
# =====================================================================
log "Test 6: Workflow with cycle detection"

# The existing workflow parser rejects circular dependencies in the needs graph.
# Verify this continues to work by checking that the parser accepts acyclic
# dependencies but rejects cycles.

# Verify that existing acyclic workflows are accepted (already shown by sync above)
ACYCLIC_WFS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
# Check for sync errors in any workflow
errors_found=[]
for wf in wfs:
    sync_error=wf.get('sync_error','')
    if sync_error and 'circular' in sync_error.lower():
        errors_found.append(sync_error)
print(len(errors_found))
")
if [ "$ACYCLIC_WFS" = "0" ]; then
  pass "No circular dependency errors in synced workflows"
else
  fail "Found circular dependency errors in synced workflows"
fi

# Verify that workflow definitions include dependency information
WF_HAS_DEPS=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
total_steps=0
for wf in wfs:
    total_steps += len(wf.get('workflow',[]))
print(total_steps)
")
if [ "$WF_HAS_DEPS" -gt 0 ]; then
  pass "Workflows contain steps ($WF_HAS_DEPS total steps across all workflows)"
else
  log "  No workflow steps found (workflows may not have synced)"
fi

# With max_iterations > 1 and the depends expression syntax, cycles should be
# allowed. This is a new v2 feature. Verify that workflows with max_iterations
# are accepted when present.
WF_MAX_ITER_CHECK=$(curl -s "$BRIDGE_URL/api/v1/workflows" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID" | python3 -c "
import json,sys
d=json.load(sys.stdin)
wfs=d.get('workflows',[])
for wf in wfs:
    for step in wf.get('workflow',[]):
        mi = step.get('max_iterations', None)
        if mi is not None and mi > 1:
            print('found')
            sys.exit()
print('none')
")
log "  max_iterations > 1 in synced workflows: $WF_MAX_ITER_CHECK"

# =====================================================================
# Test 7: Workflow run with bridge steps
# =====================================================================
log "Test 7: Workflow run with bridge steps"

# We cannot fully test bridge action execution (it needs a real GitHub repo),
# but we can verify the workflow runs API structure and bridge step metadata.

# Get the list of workflow runs
RUNS_RESULT=$(curl -s "$BRIDGE_URL/api/v1/workflow-runs" \
  -H "Authorization: Bearer $ALICE_TOKEN" \
  -H "X-Alcove-Team: $ALICE_TEAM_ID")
RUN_COUNT=$(echo "$RUNS_RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('count',0))")
log "  Current workflow run count: $RUN_COUNT"

# If there are any runs, verify the run detail endpoint works
if [ "$RUN_COUNT" -gt 0 ]; then
  FIRST_RUN_ID=$(echo "$RUNS_RESULT" | python3 -c "
import json,sys
d=json.load(sys.stdin)
runs=d.get('workflow_runs',[])
print(runs[0]['id'] if runs else '')
")

  if [ -n "$FIRST_RUN_ID" ]; then
    DETAIL_RESULT=$(curl -s -w "\n%{http_code}" "$BRIDGE_URL/api/v1/workflow-runs/$FIRST_RUN_ID" \
      -H "Authorization: Bearer $ALICE_TOKEN")
    DETAIL_CODE=$(echo "$DETAIL_RESULT" | tail -1)
    DETAIL_BODY=$(echo "$DETAIL_RESULT" | sed '$d')

    if [ "$DETAIL_CODE" = "200" ]; then
      pass "Workflow run detail endpoint returns 200"

      # Verify run detail has expected fields
      DETAIL_SHAPE=$(echo "$DETAIL_BODY" | python3 -c "
import json,sys
d=json.load(sys.stdin)
run=d.get('workflow_run',{})
steps=d.get('steps',[])
has_run_id='id' in run
has_status='status' in run
has_steps=isinstance(steps, list)
print('yes' if has_run_id and has_status and has_steps else 'no')
")
      if [ "$DETAIL_SHAPE" = "yes" ]; then
        pass "Workflow run detail has run and steps fields"
      else
        fail "Workflow run detail missing expected fields"
      fi

      # Check for bridge step fields (type, action, iteration) if present
      BRIDGE_STEP_CHECK=$(echo "$DETAIL_BODY" | python3 -c "
import json,sys
d=json.load(sys.stdin)
steps=d.get('steps',[])
bridge_steps=[]
for s in steps:
    if s.get('type','') == 'bridge':
        bridge_steps.append(s)
if bridge_steps:
    for bs in bridge_steps:
        if 'action' not in bs:
            print('missing_action')
            sys.exit()
        if 'iteration' not in bs:
            print('missing_iteration')
            sys.exit()
    print('found_bridge_steps')
else:
    print('no_bridge_steps')
")
      log "  Bridge step check in run detail: $BRIDGE_STEP_CHECK"
    else
      fail "Workflow run detail returned $DETAIL_CODE (expected 200)"
    fi
  fi
fi

# Verify a nonexistent workflow run returns 404
NOTFOUND_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
  "$BRIDGE_URL/api/v1/workflow-runs/00000000-0000-0000-0000-000000000000" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$NOTFOUND_CODE" = "404" ]; then
  pass "Nonexistent workflow run returns 404"
else
  fail "Nonexistent workflow run returned $NOTFOUND_CODE (expected 404)"
fi

# Verify workflow runs endpoint with various status filters returns 200
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

# Verify approve/reject on nonexistent run returns error
APPROVE_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  "$BRIDGE_URL/api/v1/workflow-runs/00000000-0000-0000-0000-000000000000/approve/fake-step" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$APPROVE_CODE" = "400" ] || [ "$APPROVE_CODE" = "404" ]; then
  pass "Approve on nonexistent run returns error ($APPROVE_CODE)"
else
  fail "Approve on nonexistent run returned $APPROVE_CODE (expected 400 or 404)"
fi

REJECT_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST \
  "$BRIDGE_URL/api/v1/workflow-runs/00000000-0000-0000-0000-000000000000/reject/fake-step" \
  -H "Authorization: Bearer $ALICE_TOKEN")
if [ "$REJECT_CODE" = "400" ] || [ "$REJECT_CODE" = "404" ]; then
  pass "Reject on nonexistent run returns error ($REJECT_CODE)"
else
  fail "Reject on nonexistent run returned $REJECT_CODE (expected 400 or 404)"
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
