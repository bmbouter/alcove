#!/usr/bin/env bash
# test-gate-real.sh — End-to-end Gate security tests with REAL GitHub API.
#
# This test dispatches a task through Bridge with a reduced-scope security
# profile, then sends HTTP requests to the Gate container on the internal
# network to verify:
#   - Allowed operations succeed and return real GitHub data
#   - Denied operations are blocked by Gate (403) before reaching GitHub
#   - Repo scope is enforced (requests to out-of-scope repos get 403)
#
# Prerequisites:
#   - Bridge running at localhost:8080 with AUTH_BACKEND=postgres
#   - A GitHub PAT registered as credential "github" in the credential store
#   - The repo bmbouter/alcove-testing exists on GitHub
#   - Container images built (make build-images)
#
# Usage:
#   ADMIN_PASSWORD=admin123 ./scripts/test-gate-real.sh

set -euo pipefail

BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin123}"
TEST_REPO="bmbouter/alcove-testing"
PROFILE_NAME="gate-test-readonly-$$"
TEST_IMAGE="localhost/alcove-skiff-base:dev"
INTERNAL_NET="alcove-internal"

PASS_COUNT=0
FAIL_COUNT=0
RESULTS=()
TASK_ID=""

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

    # Remove task containers if they were started.
    if [[ -n "$TASK_ID" ]]; then
        podman rm -f "skiff-${TASK_ID}" 2>/dev/null || true
        podman rm -f "gate-${TASK_ID}" 2>/dev/null || true
    fi

    # Delete the test profile.
    if [[ -n "${TOKEN:-}" ]]; then
        curl -s -X DELETE "${BRIDGE_URL}/api/v1/security-profiles/${PROFILE_NAME}" \
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

# ---------------------------------------------------------------------------
# Step 2: Create a read-only security profile
# ---------------------------------------------------------------------------
log "Creating read-only security profile '${PROFILE_NAME}'..."

PROFILE_RESP=$(curl -s -w "\n%{http_code}" -X POST "${BRIDGE_URL}/api/v1/security-profiles" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
        \"name\": \"${PROFILE_NAME}\",
        \"description\": \"Read-only access to ${TEST_REPO} for Gate integration tests\",
        \"tools\": {
            \"github\": {
                \"rules\": [{
                    \"repos\": [\"${TEST_REPO}\"],
                    \"operations\": [\"clone\", \"read_prs\", \"read_contents\", \"read_issues\"]
                }]
            }
        }
    }")

PROFILE_CODE=$(echo "$PROFILE_RESP" | tail -1)
if [[ "$PROFILE_CODE" != "201" ]]; then
    echo "ERROR: Failed to create profile (HTTP ${PROFILE_CODE})"
    echo "$PROFILE_RESP" | head -1
    exit 1
fi
log "Profile created."

# ---------------------------------------------------------------------------
# Step 3: Dispatch a task with the profile to start Gate + Skiff
# ---------------------------------------------------------------------------
log "Dispatching task to start Gate container..."

TASK_RESP=$(curl -s -X POST "${BRIDGE_URL}/api/v1/sessions" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
        \"prompt\": \"Gate integration test - this task exists only to start the Gate container\",
        \"provider\": \"default\",
        \"timeout\": 120,
        \"debug\": true,
        \"profiles\": [\"${PROFILE_NAME}\"]
    }")

TASK_ID=$(echo "$TASK_RESP" | python3 -c "import json,sys; print(json.load(sys.stdin).get('task_id',''))" 2>/dev/null)

if [[ -z "$TASK_ID" || "$TASK_ID" == "None" ]]; then
    echo "ERROR: Failed to dispatch task. Response:"
    echo "$TASK_RESP"
    exit 1
fi
log "Task dispatched: ${TASK_ID}"

GATE_NAME="gate-${TASK_ID}"

# ---------------------------------------------------------------------------
# Step 4: Wait for Gate container to be ready
# ---------------------------------------------------------------------------
log "Waiting for Gate container to start..."

for i in $(seq 1 30); do
    if podman ps --format "{{.Names}}" | grep -q "^${GATE_NAME}$"; then
        break
    fi
    if [[ $i -eq 30 ]]; then
        echo "ERROR: Gate container ${GATE_NAME} did not start within 15 seconds."
        podman ps -a --format "table {{.Names}}\t{{.Status}}" | grep "${TASK_ID}" || true
        exit 1
    fi
    sleep 0.5
done

log "Gate container is running. Waiting for healthz..."

for i in $(seq 1 30); do
    HEALTH=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s -o /dev/null -w '%{http_code}' http://${GATE_NAME}:8443/healthz" 2>/dev/null) || true
    if [[ "$HEALTH" == "200" ]]; then
        break
    fi
    if [[ $i -eq 30 ]]; then
        echo "ERROR: Gate healthz did not return 200 within 15 seconds (got: ${HEALTH:-timeout})."
        exit 1
    fi
    sleep 0.5
done

log "Gate is healthy and ready for testing."
echo ""

# ---------------------------------------------------------------------------
# Test helpers — run requests from inside the internal network
# ---------------------------------------------------------------------------

# run_test METHOD PATH EXPECTED_CODE LABEL [BODY]
run_test() {
    local method="$1" path="$2" expected_code="$3" label="$4" body="${5:-}"

    local curl_args="-s -o /dev/null -w '%{http_code}' -X ${method} http://${GATE_NAME}:8443${path} -H 'Authorization: Bearer dummy-token'"
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

# run_test_content PATH EXPECTED_SUBSTR LABEL
run_test_content() {
    local path="$1" expected_substr="$2" label="$3"

    local body
    body=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
        -c "curl -s 'http://${GATE_NAME}:8443${path}' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || body=""

    if echo "$body" | grep -q "$expected_substr"; then
        pass "${label} (response contains '${expected_substr}')"
    else
        fail "${label} (response does NOT contain '${expected_substr}')"
        echo "    Response (first 200 chars): ${body:0:200}"
    fi
}

# ===========================================================================
# TRUE POSITIVE TESTS — allowed operations with real GitHub data
# ===========================================================================
log "=== True Positive Tests (allowed by scope, real GitHub API) ==="

run_test GET "/github/repos/${TEST_REPO}/contents/README.md" 200 \
    "Read file contents (read_contents)"

run_test_content "/github/repos/${TEST_REPO}/contents/README.md" "name" \
    "File contents returns real GitHub data"

run_test GET "/github/repos/${TEST_REPO}/pulls" 200 \
    "List pull requests (read_prs)"

run_test GET "/github/repos/${TEST_REPO}/issues" 200 \
    "List issues (read_issues)"

# Note: GET /repos/:owner/:repo (no sub-path) maps to the generic "read"
# operation in Gate, NOT "read_contents". We intentionally did NOT include
# "read" in our profile, so Gate correctly blocks it. This is tested below
# under the denied-operation section.

echo ""

# ===========================================================================
# TRUE NEGATIVE TESTS — denied operations (op not in scope)
# ===========================================================================
log "=== True Negative Tests (operation not in scope, Gate should block) ==="

run_test POST "/github/repos/${TEST_REPO}/pulls" 403 \
    "Create PR (create_pr not in scope)" \
    '{"title":"gate-test","head":"main","base":"main"}'

run_test POST "/github/repos/${TEST_REPO}/issues" 403 \
    "Create issue (create_issue not in scope)" \
    '{"title":"gate-test"}'

run_test PUT "/github/repos/${TEST_REPO}/pulls/1/merge" 403 \
    "Merge PR (merge_pr not in scope)"

run_test DELETE "/github/repos/${TEST_REPO}/git/refs/heads/test-branch" 403 \
    "Delete branch (delete_branch not in scope)"

run_test POST "/github/repos/${TEST_REPO}/issues/1/comments" 403 \
    "Comment on issue (create_comment not in scope)" \
    '{"body":"gate-test"}'

run_test PATCH "/github/repos/${TEST_REPO}/pulls/1" 403 \
    "Update PR (update_pr not in scope)" \
    '{"title":"gate-test"}'

run_test PUT "/github/repos/${TEST_REPO}/contents/test-file.txt" 403 \
    "Write file (write_contents not in scope)" \
    '{"content":"dGVzdA==","message":"test"}'

run_test POST "/github/repos/${TEST_REPO}/releases" 403 \
    "Create release (write_releases not in scope)" \
    '{"tag_name":"v0.0.0-test"}'

run_test GET "/github/repos/${TEST_REPO}" 403 \
    "Read repo info (generic 'read' op not in scope)"

echo ""

# ===========================================================================
# REPO SCOPE TESTS — repos not in the profile
# ===========================================================================
log "=== Repo Scope Tests (repo not in scope, Gate should block) ==="

run_test GET "/github/repos/octocat/Hello-World/contents/README" 403 \
    "Read public repo not in scope (octocat/Hello-World)"

run_test GET "/github/repos/torvalds/linux/pulls" 403 \
    "Read PRs on repo not in scope (torvalds/linux)"

run_test GET "/github/repos/bmbouter/pulp-service/contents/README.md" 403 \
    "Read different owned repo not in scope"

echo ""

# ===========================================================================
# CREDENTIAL ISOLATION — verify real data comes back for allowed requests
# ===========================================================================
log "=== Credential Isolation Tests ==="

run_test_content "/github/repos/${TEST_REPO}/contents/README.md" \
    "README.md" \
    "Real GitHub API data returned (token was injected by Gate)"

# Verify a denied request gets a Gate error body, not a GitHub error.
DENIED_BODY=$(podman run --rm --entrypoint bash --network "${INTERNAL_NET}" "${TEST_IMAGE}" \
    -c "curl -s 'http://${GATE_NAME}:8443/github/repos/octocat/Hello-World/pulls' -H 'Authorization: Bearer dummy-token'" 2>/dev/null) || DENIED_BODY=""

if echo "$DENIED_BODY" | grep -qi "forbidden\|not authorized\|denied\|scope"; then
    pass "Denied request returns Gate error (not forwarded to GitHub)"
else
    fail "Denied request may have been forwarded to GitHub"
    echo "    Response (first 200 chars): ${DENIED_BODY:0:200}"
fi

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
    echo "  SOME TESTS FAILED — Gate security enforcement may have gaps"
    exit 1
else
    log "All tests passed — Gate correctly enforces reduced scope with real credentials."
fi
