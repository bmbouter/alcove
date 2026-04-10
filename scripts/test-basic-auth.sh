#!/bin/bash

# Test script for Basic Auth functionality
# This script validates that the CLI and Bridge API support HTTP Basic Authentication
# as an alternative to token-based authentication.

set -euo pipefail

# Configuration
BRIDGE_URL="${BRIDGE_URL:-http://localhost:8080}"
TEST_USERNAME="${TEST_USERNAME:-admin}"
TEST_PASSWORD="${TEST_PASSWORD:-password}"

echo "Testing Basic Auth functionality..."
echo "Bridge URL: $BRIDGE_URL"
echo "Test user: $TEST_USERNAME"
echo

# Test 1: CLI Basic Auth flags
echo "Test 1: CLI Basic Auth with flags"
echo "Running: alcove --server $BRIDGE_URL --username $TEST_USERNAME --password $TEST_PASSWORD list"
if alcove --server "$BRIDGE_URL" --username "$TEST_USERNAME" --password "$TEST_PASSWORD" list > /tmp/basic-auth-test1.out 2>&1; then
    echo "✓ Basic Auth with CLI flags successful"
else
    echo "✗ Basic Auth with CLI flags failed:"
    cat /tmp/basic-auth-test1.out
    exit 1
fi
echo

# Test 2: CLI Basic Auth with environment variables
echo "Test 2: CLI Basic Auth with environment variables"
export ALCOVE_USERNAME="$TEST_USERNAME"
export ALCOVE_PASSWORD="$TEST_PASSWORD"
export ALCOVE_SERVER="$BRIDGE_URL"
echo "Running: alcove list (using environment variables)"
if alcove list > /tmp/basic-auth-test2.out 2>&1; then
    echo "✓ Basic Auth with environment variables successful"
else
    echo "✗ Basic Auth with environment variables failed:"
    cat /tmp/basic-auth-test2.out
    exit 1
fi
unset ALCOVE_USERNAME ALCOVE_PASSWORD ALCOVE_SERVER
echo

# Test 3: Direct API Basic Auth (curl)
echo "Test 3: Direct API Basic Auth with curl"
echo "Running: curl -u $TEST_USERNAME:*** $BRIDGE_URL/api/v1/auth/me"
if curl -s -u "$TEST_USERNAME:$TEST_PASSWORD" "$BRIDGE_URL/api/v1/auth/me" > /tmp/basic-auth-test3.out 2>&1; then
    echo "✓ Direct API Basic Auth successful"
    if grep -q '"username"' /tmp/basic-auth-test3.out; then
        echo "✓ API returned expected user data"
    else
        echo "✗ API did not return expected user data:"
        cat /tmp/basic-auth-test3.out
        exit 1
    fi
else
    echo "✗ Direct API Basic Auth failed:"
    cat /tmp/basic-auth-test3.out
    exit 1
fi
echo

# Test 4: Invalid credentials should fail
echo "Test 4: Invalid Basic Auth credentials"
echo "Running: curl -u invalid:credentials $BRIDGE_URL/api/v1/auth/me"
if curl -s -w "%{http_code}" -u "invalid:credentials" "$BRIDGE_URL/api/v1/auth/me" > /tmp/basic-auth-test4.out 2>&1; then
    HTTP_CODE=$(tail -c 3 /tmp/basic-auth-test4.out)
    if [ "$HTTP_CODE" = "401" ]; then
        echo "✓ Invalid credentials correctly rejected (401)"
    else
        echo "✗ Invalid credentials should return 401 but got $HTTP_CODE"
        exit 1
    fi
else
    echo "✗ Failed to test invalid credentials"
    exit 1
fi
echo

# Test 5: Conflict prevention - login command with Basic Auth flags
echo "Test 5: Login command conflict prevention"
echo "Running: alcove --username test login $BRIDGE_URL (should fail)"
if alcove --username "test" login "$BRIDGE_URL" > /tmp/basic-auth-test5.out 2>&1; then
    echo "✗ Login command should reject Basic Auth flags but didn't"
    exit 1
else
    if grep -q "cannot use.*flags.*with.*login" /tmp/basic-auth-test5.out; then
        echo "✓ Login command correctly rejects Basic Auth flags"
    else
        echo "✗ Login command failed but with wrong error message:"
        cat /tmp/basic-auth-test5.out
        exit 1
    fi
fi
echo

# Test 6: Authentication precedence (Basic Auth > Bearer Token)
echo "Test 6: Authentication precedence test"
echo "This test requires both Basic Auth and a valid token to verify precedence"
# This test would need a setup with both auth methods available
echo "⚠ Skipping precedence test (requires complex setup)"
echo

echo "🎉 All Basic Auth tests passed!"
echo
echo "Summary:"
echo "✓ CLI flags work for Basic Auth"
echo "✓ Environment variables work for Basic Auth"
echo "✓ Direct API calls work with Basic Auth"
echo "✓ Invalid credentials are rejected"
echo "✓ Login command prevents Basic Auth flag conflicts"
echo "⚠ Authentication precedence test skipped"
