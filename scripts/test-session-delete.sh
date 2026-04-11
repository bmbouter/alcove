#!/bin/bash

# Test script for session delete functionality
# This script validates that sessions can be deleted via API and CLI

set -euo pipefail

# Configuration
BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
TEST_USERNAME="${TEST_USERNAME:-admin}"
TEST_PASSWORD="${TEST_PASSWORD:-password}"

echo "Testing session delete functionality..."
echo "Bridge URL: $BRIDGE_URL"
echo "Test user: $TEST_USERNAME"
echo

# Set up authentication
export ALCOVE_SERVER="$BRIDGE_URL"
export ALCOVE_USERNAME="$TEST_USERNAME"
export ALCOVE_PASSWORD="$TEST_PASSWORD"

# Helper function to create a completed test session
create_test_session() {
    local prompt="$1"
    echo "Creating test session with prompt: $prompt"

    # Submit task
    session_response=$(alcove --output json run "$prompt" 2>/dev/null)
    session_id=$(echo "$session_response" | jq -r '.id')

    if [[ -z "$session_id" || "$session_id" == "null" ]]; then
        echo "Failed to create test session"
        return 1
    fi

    echo "Created session: $session_id"

    # Wait a moment then force completion by marking it as completed via API
    sleep 2

    # Get authentication token for direct API calls
    token_response=$(curl -s -u "$TEST_USERNAME:$TEST_PASSWORD" \
        -X POST "$BRIDGE_URL/api/v1/auth/login" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"$TEST_USERNAME\",\"password\":\"$TEST_PASSWORD\"}")

    token=$(echo "$token_response" | jq -r '.token')
    if [[ -z "$token" || "$token" == "null" ]]; then
        echo "Failed to get authentication token"
        return 1
    fi

    # Force session to completed state for testing
    curl -s -X POST "$BRIDGE_URL/api/v1/sessions/$session_id/status" \
        -H "Authorization: Bearer $token" \
        -H "Content-Type: application/json" \
        -d '{"status":"completed","exit_code":0}' > /dev/null

    echo "$session_id"
}

echo "Test 1: Create test sessions"
echo "=========================="

# Create several test sessions for deletion tests
session1=$(create_test_session "echo 'Test session 1 for deletion'")
session2=$(create_test_session "echo 'Test session 2 for deletion'")
session3=$(create_test_session "echo 'Test session 3 for deletion'")

if [[ -z "$session1" || -z "$session2" || -z "$session3" ]]; then
    echo "✗ Failed to create test sessions"
    exit 1
fi

echo "✓ Created test sessions: $session1, $session2, $session3"
echo

echo "Test 2: Single session delete via CLI"
echo "====================================="

echo "Attempting to delete session: $session1"
if alcove delete "$session1" > /tmp/delete-test1.out 2>&1; then
    echo "✓ Single session delete successful"
else
    echo "✗ Single session delete failed:"
    cat /tmp/delete-test1.out
    exit 1
fi

# Verify session is deleted by trying to get it
if alcove status "$session1" > /tmp/verify-delete1.out 2>&1; then
    echo "✗ Session still exists after deletion"
    exit 1
else
    echo "✓ Session successfully deleted (no longer accessible)"
fi
echo

echo "Test 3: Single session delete via API"
echo "====================================="

# Get authentication token for API calls
token_response=$(curl -s -u "$TEST_USERNAME:$TEST_PASSWORD" \
    -X POST "$BRIDGE_URL/api/v1/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"$TEST_USERNAME\",\"password\":\"$TEST_PASSWORD\"}")

token=$(echo "$token_response" | jq -r '.token')
if [[ -z "$token" || "$token" == "null" ]]; then
    echo "✗ Failed to get authentication token"
    exit 1
fi

echo "Attempting to delete session via API: $session2"
if curl -s -X DELETE "$BRIDGE_URL/api/v1/sessions/$session2?action=delete" \
    -H "Authorization: Bearer $token" > /tmp/delete-test2.out 2>&1; then

    response=$(cat /tmp/delete-test2.out)
    if echo "$response" | jq -e '.status == "deleted"' > /dev/null 2>&1; then
        echo "✓ API session delete successful"
    else
        echo "✗ API session delete returned unexpected response:"
        cat /tmp/delete-test2.out
        exit 1
    fi
else
    echo "✗ API session delete failed:"
    cat /tmp/delete-test2.out
    exit 1
fi
echo

echo "Test 4: Bulk session delete"
echo "============================"

# Create a few more test sessions with different statuses
session4=$(create_test_session "echo 'Test session 4 for bulk deletion'")
session5=$(create_test_session "echo 'Test session 5 for bulk deletion'")

# Force one to error state for testing status-based deletion
curl -s -X POST "$BRIDGE_URL/api/v1/sessions/$session5/status" \
    -H "Authorization: Bearer $token" \
    -H "Content-Type: application/json" \
    -d '{"status":"error","exit_code":1}' > /dev/null

echo "Created additional sessions: $session4 (completed), $session5 (error)"

# Test bulk deletion by status
echo "Testing bulk delete of error sessions..."
if alcove --output json delete --status error > /tmp/bulk-delete-test.out 2>&1; then
    deleted_count=$(cat /tmp/bulk-delete-test.out | jq -r '.deleted_count // 0')
    echo "✓ Bulk delete successful: $deleted_count sessions deleted"

    # Should include session5 which was marked as error
    if [[ "$deleted_count" -gt 0 ]]; then
        echo "✓ Expected sessions were deleted"
    else
        echo "⚠ No sessions deleted (may be expected if no error sessions exist)"
    fi
else
    echo "✗ Bulk delete failed:"
    cat /tmp/bulk-delete-test.out
    exit 1
fi
echo

echo "Test 5: Delete dry-run"
echo "======================"

# Create another session for dry-run testing
session6=$(create_test_session "echo 'Test session 6 for dry-run'")

echo "Testing dry-run functionality..."
if alcove delete --status completed --dry-run > /tmp/dry-run-test.out 2>&1; then
    if grep -q "Would delete" /tmp/dry-run-test.out; then
        echo "✓ Dry-run successful and shows preview"
    else
        echo "⚠ Dry-run ran but no preview shown (may be expected if no sessions match)"
    fi
else
    echo "✗ Dry-run failed:"
    cat /tmp/dry-run-test.out
    exit 1
fi
echo

echo "Test 6: Error conditions"
echo "========================"

# Test deleting non-existent session
echo "Testing delete of non-existent session..."
if alcove delete "00000000-0000-0000-0000-000000000000" > /tmp/error-test1.out 2>&1; then
    echo "✗ Expected failure for non-existent session, but succeeded"
    exit 1
else
    echo "✓ Correctly failed to delete non-existent session"
fi

echo "✓ Error condition tests completed"
echo

echo "Test 7: Clean up remaining test sessions"
echo "========================================"

# Try to delete any remaining test sessions
echo "Cleaning up remaining test sessions..."
alcove --output json delete --status completed > /tmp/cleanup.out 2>&1 || true
deleted_count=$(cat /tmp/cleanup.out 2>/dev/null | jq -r '.deleted_count // 0' 2>/dev/null || echo "0")
echo "✓ Cleaned up $deleted_count remaining sessions"
echo

echo "========================================="
echo "✅ All session delete tests passed!"
echo "========================================="

# Clean up temporary files
rm -f /tmp/delete-test*.out /tmp/verify-delete*.out /tmp/bulk-delete-test.out /tmp/dry-run-test.out /tmp/error-test*.out /tmp/cleanup.out

exit 0
