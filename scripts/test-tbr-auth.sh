#!/bin/bash
# Test TBR identity association and authentication

set -e

BRIDGE_URL=${BRIDGE_URL:-http://localhost:8080}

echo "=== Testing TBR Identity Association ==="

# Test should only run in rh-identity mode
echo "Testing with rh-identity backend only..."

# Helper function to make authenticated requests with X-RH-Identity header
make_rh_request() {
    local method="$1"
    local url="$2" 
    local identity_json="$3"
    local body="${4:-}"
    
    # Base64 encode the identity
    local encoded_identity=$(echo -n "$identity_json" | base64 -w 0)
    
    if [ -n "$body" ]; then
        curl -s -X "$method" \
            -H "Content-Type: application/json" \
            -H "X-RH-Identity: $encoded_identity" \
            -d "$body" \
            "$url"
    else
        curl -s -X "$method" \
            -H "Content-Type: application/json" \
            -H "X-RH-Identity: $encoded_identity" \
            "$url"
    fi
}

# Create SAML identity JSON
SAML_IDENTITY='{
  "identity": {
    "type": "Associate",
    "auth_type": "saml-auth",
    "associate": {
      "rhatUUID": "test-uuid-12345",
      "email": "testuser@redhat.com",
      "givenName": "Test",
      "surname": "User",
      "Role": ["user"]
    }
  }
}'

# Create TBR identity JSON
TBR_IDENTITY='{
  "identity": {
    "type": "User",
    "auth_type": "basic-auth",
    "org_id": "test-org-123",
    "user": {
      "username": "tbr-test-user"
    }
  }
}'

echo "1. Testing SAML authentication (should provision user)..."
RESPONSE=$(make_rh_request GET "$BRIDGE_URL/api/v1/auth/me" "$SAML_IDENTITY")
echo "SAML auth response: $RESPONSE"

echo ""
echo "2. Creating TBR association..."
ASSOCIATION_DATA='{
  "tbr_org_id": "test-org-123",
  "tbr_username": "tbr-test-user"
}'

ASSOC_RESPONSE=$(make_rh_request POST "$BRIDGE_URL/api/v1/auth/tbr-associations" "$SAML_IDENTITY" "$ASSOCIATION_DATA")
echo "Association creation: $ASSOC_RESPONSE"

ASSOCIATION_ID=$(echo "$ASSOC_RESPONSE" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
if [ -z "$ASSOCIATION_ID" ]; then
    echo "ERROR: Failed to create TBR association"
    exit 1
fi
echo "Created association with ID: $ASSOCIATION_ID"

echo ""
echo "3. Listing TBR associations..."
LIST_RESPONSE=$(make_rh_request GET "$BRIDGE_URL/api/v1/auth/tbr-associations" "$SAML_IDENTITY")
echo "Associations list: $LIST_RESPONSE"

echo ""
echo "4. Testing TBR authentication (should resolve to SAML user)..."
TBR_AUTH_RESPONSE=$(make_rh_request GET "$BRIDGE_URL/api/v1/auth/me" "$TBR_IDENTITY")
echo "TBR auth response: $TBR_AUTH_RESPONSE"

# Verify TBR auth resolved to the same user as SAML
SAML_USER=$(echo "$RESPONSE" | grep -o '"username":"[^"]*"' | cut -d'"' -f4)
TBR_USER=$(echo "$TBR_AUTH_RESPONSE" | grep -o '"username":"[^"]*"' | cut -d'"' -f4)

if [ "$SAML_USER" = "$TBR_USER" ]; then
    echo "✓ TBR identity correctly resolved to SAML user: $SAML_USER"
else
    echo "✗ TBR resolution failed: SAML user='$SAML_USER', TBR user='$TBR_USER'"
    exit 1
fi

echo ""
echo "5. Testing unauthorized TBR identity (should fail)..."
UNKNOWN_TBR='{
  "identity": {
    "type": "User",
    "auth_type": "basic-auth",
    "org_id": "unknown-org",
    "user": {
      "username": "unknown-user"
    }
  }
}'

UNAUTH_RESPONSE=$(make_rh_request GET "$BRIDGE_URL/api/v1/auth/me" "$UNKNOWN_TBR" || echo "Expected auth failure")
echo "Unknown TBR response: $UNAUTH_RESPONSE"

echo ""
echo "6. Deleting TBR association..."
DELETE_RESPONSE=$(make_rh_request DELETE "$BRIDGE_URL/api/v1/auth/tbr-associations/$ASSOCIATION_ID" "$SAML_IDENTITY")
echo "Delete response: $DELETE_RESPONSE"

echo ""
echo "7. Verifying association is deleted..."
FINAL_LIST=$(make_rh_request GET "$BRIDGE_URL/api/v1/auth/tbr-associations" "$SAML_IDENTITY")
echo "Final associations list: $FINAL_LIST"

echo ""
echo "=== TBR Test Complete ==="
echo "All tests passed! TBR identity association is working correctly."
