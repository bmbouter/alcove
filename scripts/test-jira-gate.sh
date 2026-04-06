#!/usr/bin/env bash
# test-jira-gate.sh — End-to-end Gate security tests with REAL Jira API.
#
# This test dispatches a task through Bridge with different security profiles,
# then sends HTTP requests to the Gate container on the internal network to
# verify:
#   - Total blocking: when jira is NOT in scope, all requests get 403
#   - Read-only scope: allowed read operations succeed with real Jira data
#   - Write blocking: write operations are blocked by Gate (403) before reaching Jira
#   - Credential isolation: real credentials are injected for allowed requests,
#     and denied requests never reach Jira
#
# IMPORTANT: ALL tests are 100% read-only. Only GET requests are sent to Jira.
# Write-blocking tests send POST/PUT/DELETE through Gate, but Gate returns 403
# BEFORE forwarding to Jira, so no data is modified.
#
# Prerequisites:
#   - Bridge running at localhost:8080 with AUTH_BACKEND=postgres
#   - A Jira credential registered as "jira" in the credential store
#     (format: email@example.com:api-token, for Basic auth against Atlassian)
#   - The JIRA project PULP exists on redhat.atlassian.net
#   - Container images built (make build-images)
#
# Usage:
#   ADMIN_PASSWORD=admin123 ./scripts/test-jira-gate.sh

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin123}"
JIRA_PROJECT="PULP"
JIRA_COMPONENT="services"
PROFILE_BLOCKED="gate-jira-blocked-$$"
PROFILE_READONLY="gate-jira-readonly-$$"
TEST_IMAGE="localhost/alcove-skiff-base:dev"
INTERNAL_NET="alcove-internal"

PASS_COUNT=0
FAIL_COUNT=0
RESULTS=()

# Task IDs for cleanup
TASK_ID_BLOCKED=""
TASK_ID_READONLY=""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { echo ">>> $*"; }
pass() { PASS_COUNT=$((PASS_COUNT + 1)); RESULTS+=("PASS: $1"); echo "  PASS: $1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); RESULTS+=("FAIL: $1"); echo "  FAIL: $1"; }

# ---------------------------------------------------------------------------
# Cleanup (runs on exit)
# ---------------------------------------------------------------------------
cleanup() {
    log "Cleaning up..."

    # Remove task containers
    for tid in "$TASK_ID_BLOCKED" "$TASK_ID_READONLY"; do
        if [[ -n "$tid" ]]; then
            podman rm -f "skiff-${tid}" 2>/dev/null || true
            podman rm -f "gate-${tid}" 2>/dev/null || true
        fi
    done

    # Delete test profiles
    if [[ -n "${TOKEN:-}" ]]; then
        curl -s -X DELETE "${BRIDGE_URL}/api/v1/security-profiles/${PROFILE_BLOCKED}" \
            -H "Authorization: Bearer ${TOKEN}" > /dev/null 2>&1 || true
        curl -s -X DELETE "${BRIDGE_URL}/api/v1/security-profiles/${PROFILE_READONLY}" \
            -H "Authorization: Bearer ${TOKEN}" > /dev/null 2>&1 || true
    fi

    log "Cleanup complete."
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Step 1: Authenticate with Bridge
# ---------------------------------------------------------------------------
log "Logging in to Bridge..."
LOGIN_RESP=$(curl -s -X POST "${BRIDGE_URL}/api/v1/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}")

TOKEN=$(echo "$LOGIN_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin)['token'])" 2>/dev/null) || {
    echo "ERROR: Failed to login to Bridge. Response: ${LOGIN_RESP}"
    exit 1
}
log "Logged in successfully."

# ===========================================================================
# PHASE 1: Total Blocking — no jira in scope
# ===========================================================================
log ""
log "================================================================"
log "PHASE 1: Total Blocking (no jira service in scope)"
log "================================================================"

# Create a profile with only github (no jira)
log "Creating profile '${PROFILE_BLOCKED}' (github only, no jira)..."
PROFILE_RESP=$(curl -s -w "\n%{http_code}" -X POST "${BRIDGE_URL}/api/v1/security-profiles" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
        \"name\": \"${PROFILE_BLOCKED}\",
        \"description\": \"No JIRA access — for Gate blocking tests\",
        \"tools\": {
            \"github\": {
                \"rules\": [{
                    \"repos\": [\"bmbouter/alcove-testing\"],
                    \"operations\": [\"read_contents\"]
                }]
            }
        }
    }")

PROFILE_CODE=$(echo "$PROFILE_RESP" | tail -1)
if [[ "$PROFILE_CODE" != "201" ]]; then
    echo "ERROR: Failed to create blocked profile (HTTP ${PROFILE_CODE})"
    echo "$PROFILE_RESP" | head -1
    exit 1
fi
log "Blocked profile created."

# Dispatch a task with the blocked profile
log "Dispatching task for blocked scope..."
TASK_RESP=$(curl -s -X POST "${BRIDGE_URL}/api/v1/tasks" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
        \"prompt\": \"Gate JIRA integration test (blocked scope) — this task exists only to start Gate\",
        \"provider\": \"default\",
        \"timeout\": 120,
        \"debug\": true,
        \"profiles\": [\"${PROFILE_BLOCKED}\"]
    }")

TASK_ID_BLOCKED=$(echo "$TASK_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('task_id',''))" 2>/dev/null)
if [[ -z "$TASK_ID_BLOCKED" || "$TASK_ID_BLOCKED" == "None" ]]; then
    echo "ERROR: Failed to dispatch blocked task. Response:"
    echo "$TASK_RESP"
    exit 1
fi
log "Task dispatched: ${TASK_ID_BLOCKED}"

GATE_BLOCKED="gate-${TASK_ID_BLOCKED}"

# Wait for Gate container
log "Waiting for Gate container to start..."
for i in $(seq 1 30); do
    if podman ps --format "{{.Names}}" | grep -q "^${GATE_BLOCKED}$"; then
        break
    fi
    if [[ $i -eq 30 ]]; then
        echo "ERROR: Gate container ${GATE_BLOCKED} did not start within 15 seconds."
        podman ps -a --format "table {{.Names}}\t{{.Status}}" | grep "${TASK_ID_BLOCKED}" || true
        exit 1
    fi
    sleep 0.5
done

log "Gate container running. Waiting for healthz..."
for i in $(seq 1 30); do
    HEALTH=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s -o /dev/null -w '%{http_code}' http://${GATE_BLOCKED}:8443/healthz" 2>/dev/null) || true
    if [[ "$HEALTH" == "200" ]]; then
        break
    fi
    if [[ $i -eq 30 ]]; then
        echo "ERROR: Gate healthz did not return 200 within 15 seconds (got: ${HEALTH:-timeout})."
        exit 1
    fi
    sleep 0.5
done
log "Gate is healthy."

# ---------------------------------------------------------------------------
# Test helpers — run requests from inside the internal network
# ---------------------------------------------------------------------------

# run_test GATE_NAME METHOD PATH EXPECTED_CODE LABEL [BODY]
run_test() {
    local gate_name="$1" method="$2" path="$3" expected_code="$4" label="$5" body="${6:-}"

    local curl_args="-s -o /dev/null -w '%{http_code}' -X ${method} http://${gate_name}:8443${path} -H 'Authorization: Bearer dummy-token'"
    if [[ -n "$body" ]]; then
        curl_args="${curl_args} -H 'Content-Type: application/json' -d '${body}'"
    fi

    local actual_code
    actual_code=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl ${curl_args}" 2>/dev/null) || actual_code="000"

    if [[ "$actual_code" == "$expected_code" ]]; then
        pass "${label} (HTTP ${actual_code})"
    else
        fail "${label} (expected ${expected_code}, got ${actual_code})"
    fi
}

# run_test_content GATE_NAME PATH EXPECTED_SUBSTR LABEL
run_test_content() {
    local gate_name="$1" path="$2" expected_substr="$3" label="$4"

    local body
    body=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s 'http://${gate_name}:8443${path}' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || body=""

    if echo "$body" | grep -qi "$expected_substr"; then
        pass "${label} (response contains '${expected_substr}')"
    else
        fail "${label} (response does NOT contain '${expected_substr}')"
        echo "    Response (first 300 chars): ${body:0:300}"
    fi
}

# run_test_json_field GATE_NAME PATH FIELD LABEL
# Verifies the JSON response contains the given top-level field
run_test_json_field() {
    local gate_name="$1" path="$2" field="$3" label="$4"

    local body
    body=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s 'http://${gate_name}:8443${path}' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || body=""

    if echo "$body" | python3 -c "import json,sys; d=json.load(sys.stdin); assert '${field}' in d or (isinstance(d,list) and len(d)>0)" 2>/dev/null; then
        pass "${label}"
    else
        fail "${label}"
        echo "    Response (first 300 chars): ${body:0:300}"
    fi
}

echo ""

# ---------------------------------------------------------------------------
# Test Group 1: Total Blocking — all JIRA requests should get 403
# ---------------------------------------------------------------------------
log "=== Test Group 1: Total Blocking (jira not in scope, all should be 403) ==="

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/project" 403 \
    "List projects — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/project/${JIRA_PROJECT}" 403 \
    "Get project ${JIRA_PROJECT} — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/search?jql=project=${JIRA_PROJECT}&maxResults=1" 403 \
    "Search issues — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/issue/PULP-1" 403 \
    "Get issue PULP-1 — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/issue/PULP-1/comment" 403 \
    "Get comments — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/myself" 403 \
    "Get current user — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/agile/1.0/board" 403 \
    "List boards — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/issuetype" 403 \
    "List issue types — blocked (no jira in scope)"

run_test "$GATE_BLOCKED" GET "/jira/rest/api/3/priority" 403 \
    "List priorities — blocked (no jira in scope)"

# Also verify write operations are blocked
run_test "$GATE_BLOCKED" POST "/jira/rest/api/3/issue" 403 \
    "Create issue — blocked (no jira in scope)" \
    '{"fields":{"project":{"key":"PULP"},"summary":"test","issuetype":{"name":"Bug"}}}'

run_test "$GATE_BLOCKED" PUT "/jira/rest/api/3/issue/PULP-1" 403 \
    "Update issue — blocked (no jira in scope)" \
    '{"fields":{"summary":"test"}}'

run_test "$GATE_BLOCKED" DELETE "/jira/rest/api/3/issue/PULP-1" 403 \
    "Delete issue — blocked (no jira in scope)"

echo ""

# Clean up blocked task containers before starting readonly tests
log "Cleaning up blocked-scope task..."
podman rm -f "skiff-${TASK_ID_BLOCKED}" 2>/dev/null || true
podman rm -f "gate-${TASK_ID_BLOCKED}" 2>/dev/null || true

# ===========================================================================
# PHASE 2: Read-Only Access — jira in scope with read operations only
# ===========================================================================
log ""
log "================================================================"
log "PHASE 2: Read-Only Access (jira with read-only operations)"
log "================================================================"

# Create a read-only profile for JIRA
log "Creating profile '${PROFILE_READONLY}' (jira read-only)..."
PROFILE_RESP=$(curl -s -w "\n%{http_code}" -X POST "${BRIDGE_URL}/api/v1/security-profiles" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
        \"name\": \"${PROFILE_READONLY}\",
        \"description\": \"Read-only JIRA access for Gate integration tests\",
        \"tools\": {
            \"jira\": {
                \"rules\": [{
                    \"repos\": [\"*\"],
                    \"operations\": [\"read_issues\", \"search_issues\", \"read_comments\", \"read_projects\", \"read_metadata\", \"read_boards\", \"read_sprints\", \"read_transitions\"]
                }]
            }
        }
    }")

PROFILE_CODE=$(echo "$PROFILE_RESP" | tail -1)
if [[ "$PROFILE_CODE" != "201" ]]; then
    echo "ERROR: Failed to create readonly profile (HTTP ${PROFILE_CODE})"
    echo "$PROFILE_RESP" | head -1
    exit 1
fi
log "Read-only profile created."

# Dispatch a task with the readonly profile
log "Dispatching task for read-only scope..."
TASK_RESP=$(curl -s -X POST "${BRIDGE_URL}/api/v1/tasks" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
        \"prompt\": \"Gate JIRA integration test (read-only scope) — this task exists only to start Gate\",
        \"provider\": \"default\",
        \"timeout\": 180,
        \"debug\": true,
        \"profiles\": [\"${PROFILE_READONLY}\"]
    }")

TASK_ID_READONLY=$(echo "$TASK_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('task_id',''))" 2>/dev/null)
if [[ -z "$TASK_ID_READONLY" || "$TASK_ID_READONLY" == "None" ]]; then
    echo "ERROR: Failed to dispatch readonly task. Response:"
    echo "$TASK_RESP"
    exit 1
fi
log "Task dispatched: ${TASK_ID_READONLY}"

GATE_READONLY="gate-${TASK_ID_READONLY}"

# Wait for Gate container
log "Waiting for Gate container to start..."
for i in $(seq 1 30); do
    if podman ps --format "{{.Names}}" | grep -q "^${GATE_READONLY}$"; then
        break
    fi
    if [[ $i -eq 30 ]]; then
        echo "ERROR: Gate container ${GATE_READONLY} did not start within 15 seconds."
        podman ps -a --format "table {{.Names}}\t{{.Status}}" | grep "${TASK_ID_READONLY}" || true
        exit 1
    fi
    sleep 0.5
done

log "Gate container running. Waiting for healthz..."
for i in $(seq 1 30); do
    HEALTH=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s -o /dev/null -w '%{http_code}' http://${GATE_READONLY}:8443/healthz" 2>/dev/null) || true
    if [[ "$HEALTH" == "200" ]]; then
        break
    fi
    if [[ $i -eq 30 ]]; then
        echo "ERROR: Gate healthz did not return 200 within 15 seconds (got: ${HEALTH:-timeout})."
        exit 1
    fi
    sleep 0.5
done
log "Gate is healthy."
echo ""

# ---------------------------------------------------------------------------
# Test Group 2: Read operations — should all PASS (200)
# ---------------------------------------------------------------------------
log "=== Test Group 2: Read Operations (allowed by read-only scope) ==="

# 2a: List projects
run_test "$GATE_READONLY" GET "/jira/rest/api/3/project" 200 \
    "List projects (read_projects)"

# 2b: Get specific project
run_test "$GATE_READONLY" GET "/jira/rest/api/3/project/${JIRA_PROJECT}" 200 \
    "Get project ${JIRA_PROJECT} (read_projects)"

run_test_content "$GATE_READONLY" "/jira/rest/api/3/project/${JIRA_PROJECT}" \
    "${JIRA_PROJECT}" \
    "Project response contains project key '${JIRA_PROJECT}'"

# 2c: Search issues with JQL
run_test "$GATE_READONLY" GET \
    "/jira/rest/api/3/search?jql=project%3D${JIRA_PROJECT}%20AND%20component%3D${JIRA_COMPONENT}%20ORDER%20BY%20created%20DESC&maxResults=5" 200 \
    "Search issues with JQL (search_issues)"

# Extract an issue key from search results for subsequent tests
SEARCH_BODY=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
    -c "curl -s 'http://${GATE_READONLY}:8443/jira/rest/api/3/search?jql=project%3D${JIRA_PROJECT}%20AND%20component%3D${JIRA_COMPONENT}%20ORDER%20BY%20created%20DESC&maxResults=5' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || SEARCH_BODY=""

ISSUE_KEY=$(echo "$SEARCH_BODY" | python3 -c "
import json,sys
try:
    data = json.load(sys.stdin)
    issues = data.get('issues', [])
    if issues:
        print(issues[0]['key'])
    else:
        print('')
except:
    print('')
" 2>/dev/null) || ISSUE_KEY=""

if [[ -n "$ISSUE_KEY" && "$ISSUE_KEY" != "" ]]; then
    log "Found issue from search: ${ISSUE_KEY}"
    pass "Search returned real Jira data (found issue ${ISSUE_KEY})"
else
    log "WARNING: No issues found in search results. Using PULP-1 as fallback."
    ISSUE_KEY="PULP-1"
    # Don't fail — the search might legitimately return no results for the component
fi

# 2d: Get a specific issue
run_test "$GATE_READONLY" GET "/jira/rest/api/3/issue/${ISSUE_KEY}" 200 \
    "Get issue ${ISSUE_KEY} (read_issues)"

run_test_content "$GATE_READONLY" "/jira/rest/api/3/issue/${ISSUE_KEY}" \
    "${ISSUE_KEY}" \
    "Issue response contains key '${ISSUE_KEY}'"

# 2e: Get issue comments
run_test "$GATE_READONLY" GET "/jira/rest/api/3/issue/${ISSUE_KEY}/comment" 200 \
    "Get comments for ${ISSUE_KEY} (read_comments)"

# 2f: Get issue transitions
run_test "$GATE_READONLY" GET "/jira/rest/api/3/issue/${ISSUE_KEY}/transitions" 200 \
    "Get transitions for ${ISSUE_KEY} (read_transitions)"

# 2g: Current user (read_metadata)
run_test "$GATE_READONLY" GET "/jira/rest/api/3/myself" 200 \
    "Get current user (read_metadata)"

run_test_content "$GATE_READONLY" "/jira/rest/api/3/myself" \
    "emailAddress" \
    "Current user response contains 'emailAddress' field"

# 2h: Issue types (read_metadata)
run_test "$GATE_READONLY" GET "/jira/rest/api/3/issuetype" 200 \
    "List issue types (read_metadata)"

# 2i: Priorities (read_metadata)
run_test "$GATE_READONLY" GET "/jira/rest/api/3/priority" 200 \
    "List priorities (read_metadata)"

# 2j: Statuses (read_metadata)
run_test "$GATE_READONLY" GET "/jira/rest/api/3/status" 200 \
    "List statuses (read_metadata)"

# 2k: Fields (read_metadata)
run_test "$GATE_READONLY" GET "/jira/rest/api/3/field" 200 \
    "List fields (read_metadata)"

# 2l: Boards (read_boards) — may fail if Jira Software is not enabled
run_test "$GATE_READONLY" GET "/jira/rest/agile/1.0/board" 200 \
    "List boards (read_boards)"

echo ""

# ---------------------------------------------------------------------------
# Test Group 3: Write Operations Blocked — Gate returns 403 before JIRA
# ---------------------------------------------------------------------------
log "=== Test Group 3: Write Operations Blocked (Gate enforces read-only) ==="

# 3a: Create issue — Gate blocks before reaching Jira
run_test "$GATE_READONLY" POST "/jira/rest/api/3/issue" 403 \
    "Create issue blocked (create_issue not in scope)" \
    '{"fields":{"project":{"key":"PULP"},"summary":"gate-test-should-not-exist","issuetype":{"name":"Bug"}}}'

# 3b: Update issue — Gate blocks
run_test "$GATE_READONLY" PUT "/jira/rest/api/3/issue/${ISSUE_KEY}" 403 \
    "Update issue blocked (update_issue not in scope)" \
    '{"fields":{"summary":"gate-test-should-not-update"}}'

# 3c: Delete issue — Gate blocks
run_test "$GATE_READONLY" DELETE "/jira/rest/api/3/issue/${ISSUE_KEY}" 403 \
    "Delete issue blocked (delete_issue not in scope)"

# 3d: Add comment — Gate blocks
run_test "$GATE_READONLY" POST "/jira/rest/api/3/issue/${ISSUE_KEY}/comment" 403 \
    "Add comment blocked (add_comment not in scope)" \
    '{"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"gate-test"}]}]}}'

# 3e: Transition issue — Gate blocks
run_test "$GATE_READONLY" POST "/jira/rest/api/3/issue/${ISSUE_KEY}/transitions" 403 \
    "Transition issue blocked (transition_issue not in scope)" \
    '{"transition":{"id":"21"}}'

# 3f: Assign issue — Gate blocks
run_test "$GATE_READONLY" PUT "/jira/rest/api/3/issue/${ISSUE_KEY}/assignee" 403 \
    "Assign issue blocked (assign_issue not in scope)" \
    '{"accountId":"000000:00000000-0000-0000-0000-000000000000"}'

# 3g: Delete comment — Gate blocks
run_test "$GATE_READONLY" DELETE "/jira/rest/api/3/issue/${ISSUE_KEY}/comment/12345" 403 \
    "Delete comment blocked (delete_comment not in scope)"

# 3h: Update comment — Gate blocks
run_test "$GATE_READONLY" PUT "/jira/rest/api/3/issue/${ISSUE_KEY}/comment/12345" 403 \
    "Update comment blocked (update_comment not in scope)" \
    '{"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"gate-test"}]}]}}'

# 3i: Add worklog — Gate blocks
run_test "$GATE_READONLY" POST "/jira/rest/api/3/issue/${ISSUE_KEY}/worklog" 403 \
    "Add worklog blocked (add_worklog not in scope)" \
    '{"timeSpentSeconds":3600}'

# 3j: Move to sprint — Gate blocks
run_test "$GATE_READONLY" POST "/jira/rest/agile/1.0/sprint/1/issue" 403 \
    "Move to sprint blocked (move_to_sprint not in scope)" \
    '{"issues":["PULP-1"]}'

echo ""

# ---------------------------------------------------------------------------
# Test Group 4: Credential Isolation
# ---------------------------------------------------------------------------
log "=== Test Group 4: Credential Isolation ==="

# 4a: Verify real data comes back for allowed requests (credential was injected)
run_test_content "$GATE_READONLY" "/jira/rest/api/3/project/${JIRA_PROJECT}" \
    "key" \
    "Real Jira API data returned (credential injected by Gate)"

# 4b: Verify a denied request gets a Gate error body, not a Jira error
DENIED_BODY=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
    -c "curl -s 'http://${GATE_READONLY}:8443/jira/rest/api/3/issue' -X POST -H 'Authorization: Bearer dummy-token' -H 'Content-Type: application/json' -d '{\"fields\":{}}'" 2>/dev/null) || DENIED_BODY=""

if echo "$DENIED_BODY" | grep -qi "forbidden\|not authorized\|denied\|not permitted"; then
    pass "Denied request returns Gate error (not forwarded to Jira)"
else
    fail "Denied request may have been forwarded to Jira"
    echo "    Response (first 300 chars): ${DENIED_BODY:0:300}"
fi

# 4c: Cross-service isolation — GitHub requests should be denied on jira-only profile
run_test "$GATE_READONLY" GET "/github/repos/pulp/pulpcore/pulls" 403 \
    "GitHub request blocked (only jira in scope, not github)"

# 4d: GitLab requests should be denied on jira-only profile
run_test "$GATE_READONLY" GET "/gitlab/api/v4/projects/12345/merge_requests" 403 \
    "GitLab request blocked (only jira in scope, not gitlab)"

echo ""

# ---------------------------------------------------------------------------
# Test Group 5: Project Scope Enforcement (detailed)
# ---------------------------------------------------------------------------
log "=== Test Group 5: Real Data Verification ==="

# 5a: Verify search results contain expected fields
SEARCH_VERIFY=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
    -c "curl -s 'http://${GATE_READONLY}:8443/jira/rest/api/3/search?jql=project%3D${JIRA_PROJECT}&maxResults=1' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || SEARCH_VERIFY=""

if echo "$SEARCH_VERIFY" | python3 -c "
import json,sys
data = json.load(sys.stdin)
assert 'total' in data, 'missing total'
assert 'issues' in data, 'missing issues'
" 2>/dev/null; then
    pass "Search response has expected structure (total, issues)"
else
    fail "Search response missing expected structure"
    echo "    Response (first 300 chars): ${SEARCH_VERIFY:0:300}"
fi

# 5b: Verify issue response has expected fields
if [[ -n "$ISSUE_KEY" ]]; then
    ISSUE_VERIFY=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s 'http://${GATE_READONLY}:8443/jira/rest/api/3/issue/${ISSUE_KEY}' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || ISSUE_VERIFY=""

    if echo "$ISSUE_VERIFY" | python3 -c "
import json,sys
data = json.load(sys.stdin)
assert 'key' in data, 'missing key'
assert 'fields' in data, 'missing fields'
assert 'summary' in data['fields'], 'missing fields.summary'
" 2>/dev/null; then
        pass "Issue response has expected structure (key, fields, summary)"
    else
        fail "Issue response missing expected structure"
        echo "    Response (first 300 chars): ${ISSUE_VERIFY:0:300}"
    fi

    # 5c: Verify comment response has expected structure
    COMMENT_VERIFY=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s 'http://${GATE_READONLY}:8443/jira/rest/api/3/issue/${ISSUE_KEY}/comment' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || COMMENT_VERIFY=""

    if echo "$COMMENT_VERIFY" | python3 -c "
import json,sys
data = json.load(sys.stdin)
assert 'comments' in data, 'missing comments'
assert 'total' in data, 'missing total'
" 2>/dev/null; then
        pass "Comment response has expected structure (comments, total)"
    else
        fail "Comment response missing expected structure"
        echo "    Response (first 300 chars): ${COMMENT_VERIFY:0:300}"
    fi
fi

# 5d: Verify project list contains PULP
run_test_content "$GATE_READONLY" "/jira/rest/api/3/project/${JIRA_PROJECT}" \
    "${JIRA_PROJECT}" \
    "Project ${JIRA_PROJECT} exists and is accessible"

echo ""

# ===========================================================================
# Summary
# ===========================================================================
log "=== Test Summary ==="
for r in "${RESULTS[@]}"; do
    echo "  $r"
done
echo ""
echo "  Total: $((PASS_COUNT + FAIL_COUNT))  Passed: ${PASS_COUNT}  Failed: ${FAIL_COUNT}"
echo ""

if [[ "$FAIL_COUNT" -gt 0 ]]; then
    echo "  SOME TESTS FAILED — Gate JIRA security enforcement may have gaps"
    exit 1
else
    log "All tests passed — Gate correctly enforces JIRA scope with real credentials."
fi
