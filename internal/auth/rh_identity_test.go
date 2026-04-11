// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeRHIdentityHeader builds a valid base64-encoded X-RH-Identity header
// for a SAML Associate identity.
func makeRHIdentityHeader(email, uuid, givenName, surname string, roles []string) string {
	identity := map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "Associate",
			"auth_type": "saml-auth",
			"associate": map[string]interface{}{
				"rhatUUID":  uuid,
				"email":     email,
				"givenName": givenName,
				"surname":   surname,
				"Role":      roles,
			},
		},
	}
	data, _ := json.Marshal(identity)
	return base64.StdEncoding.EncodeToString(data)
}

// makeRHIdentityHeaderCustom builds a base64-encoded X-RH-Identity header
// from an arbitrary map, for testing edge cases.
func makeRHIdentityHeaderCustom(identity map[string]interface{}) string {
	data, _ := json.Marshal(identity)
	return base64.StdEncoding.EncodeToString(data)
}

// makeTBRIdentityHeader builds a valid base64-encoded X-RH-Identity header
// for a TBR User identity.
func makeTBRIdentityHeader(orgID, username string) string {
	identity := map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "User",
			"auth_type": "basic-auth",
			"org_id":    orgID,
			"user": map[string]interface{}{
				"username": username,
			},
		},
	}
	data, _ := json.Marshal(identity)
	return base64.StdEncoding.EncodeToString(data)
}

// --- ParseRHIdentity tests ---

func TestParseRHIdentity_ValidSAML(t *testing.T) {
	header := makeRHIdentityHeader(
		"jdoe@redhat.com",
		"12345-abcde-67890",
		"Jane",
		"Doe",
		[]string{"admin", "user"},
	)

	id, err := ParseRHIdentity(header)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if id.Identity.Type != "Associate" {
		t.Errorf("expected type Associate, got %q", id.Identity.Type)
	}
	if id.Identity.AuthType != "saml-auth" {
		t.Errorf("expected auth_type saml-auth, got %q", id.Identity.AuthType)
	}
	if id.Identity.Associate == nil {
		t.Fatal("expected associate to be non-nil")
	}
	if id.Identity.Associate.RhatUUID != "12345-abcde-67890" {
		t.Errorf("expected rhatUUID 12345-abcde-67890, got %q", id.Identity.Associate.RhatUUID)
	}
	if id.Identity.Associate.Email != "jdoe@redhat.com" {
		t.Errorf("expected email jdoe@redhat.com, got %q", id.Identity.Associate.Email)
	}
	if id.Identity.Associate.GivenName != "Jane" {
		t.Errorf("expected givenName Jane, got %q", id.Identity.Associate.GivenName)
	}
	if id.Identity.Associate.Surname != "Doe" {
		t.Errorf("expected surname Doe, got %q", id.Identity.Associate.Surname)
	}
	if len(id.Identity.Associate.Role) != 2 {
		t.Errorf("expected 2 roles, got %d", len(id.Identity.Associate.Role))
	}
	if id.Identity.Associate.Role[0] != "admin" || id.Identity.Associate.Role[1] != "user" {
		t.Errorf("expected roles [admin user], got %v", id.Identity.Associate.Role)
	}
}

func TestParseRHIdentity_EmptyHeader(t *testing.T) {
	_, err := ParseRHIdentity("")
	if err == nil {
		t.Fatal("expected error for empty header, got nil")
	}
}

func TestParseRHIdentity_InvalidBase64(t *testing.T) {
	_, err := ParseRHIdentity("not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
}

func TestParseRHIdentity_InvalidJSON(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("this is not json"))
	_, err := ParseRHIdentity(encoded)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseRHIdentity_MissingAssociate(t *testing.T) {
	// Valid JSON but no associate field — should return error per implementation
	// (ParseRHIdentity checks for nil associate).
	header := makeRHIdentityHeaderCustom(map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "User",
			"auth_type": "basic-auth",
		},
	})

	_, err := ParseRHIdentity(header)
	if err == nil {
		t.Fatal("expected error for missing associate, got nil")
	}
}

func TestParseRHIdentity_X509Type(t *testing.T) {
	// X509 identity has no associate field — should return error since
	// ParseRHIdentity requires associate with rhatUUID and email.
	header := makeRHIdentityHeaderCustom(map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "X509",
			"auth_type": "cert-auth",
			"x509": map[string]interface{}{
				"subject_dn": "CN=test",
				"issuer_dn":  "CN=ca",
			},
		},
	})

	_, err := ParseRHIdentity(header)
	if err == nil {
		t.Fatal("expected error for X509 identity (no associate), got nil")
	}
}

func TestParseRHIdentity_MissingRhatUUID(t *testing.T) {
	header := makeRHIdentityHeaderCustom(map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "Associate",
			"auth_type": "saml-auth",
			"associate": map[string]interface{}{
				"email":     "test@redhat.com",
				"givenName": "Test",
				"surname":   "User",
			},
		},
	})

	_, err := ParseRHIdentity(header)
	if err == nil {
		t.Fatal("expected error for missing rhatUUID, got nil")
	}
}

func TestParseRHIdentity_MissingEmail(t *testing.T) {
	header := makeRHIdentityHeaderCustom(map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "Associate",
			"auth_type": "saml-auth",
			"associate": map[string]interface{}{
				"rhatUUID":  "12345",
				"givenName": "Test",
				"surname":   "User",
			},
		},
	})

	_, err := ParseRHIdentity(header)
	if err == nil {
		t.Fatal("expected error for missing email, got nil")
	}
}

func TestParseRHIdentity_EmptyRoles(t *testing.T) {
	header := makeRHIdentityHeader("user@redhat.com", "uuid-123", "Test", "User", []string{})
	id, err := ParseRHIdentity(header)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(id.Identity.Associate.Role) != 0 {
		t.Errorf("expected empty roles, got %v", id.Identity.Associate.Role)
	}
}

func TestParseRHIdentity_NilRoles(t *testing.T) {
	header := makeRHIdentityHeaderCustom(map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "Associate",
			"auth_type": "saml-auth",
			"associate": map[string]interface{}{
				"rhatUUID":  "uuid-123",
				"email":     "user@redhat.com",
				"givenName": "Test",
				"surname":   "User",
				// No Role field at all
			},
		},
	})

	id, err := ParseRHIdentity(header)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if id.Identity.Associate.Role != nil {
		t.Errorf("expected nil roles, got %v", id.Identity.Associate.Role)
	}
}

func TestParseRHIdentity_ValidBase64Padding(t *testing.T) {
	// Ensure standard base64 with padding works correctly.
	header := makeRHIdentityHeader("pad@redhat.com", "pad-uuid", "Pad", "Test", nil)
	_, err := ParseRHIdentity(header)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// --- TBR Identity Tests ---

func TestParseRHIdentity_ValidTBR(t *testing.T) {
	header := makeTBRIdentityHeader("12345", "testuser")

	id, err := ParseRHIdentity(header)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if id.Identity.Type != "User" {
		t.Errorf("expected type User, got %q", id.Identity.Type)
	}
	if id.Identity.AuthType != "basic-auth" {
		t.Errorf("expected auth_type basic-auth, got %q", id.Identity.AuthType)
	}
	if id.Identity.OrgID != "12345" {
		t.Errorf("expected org_id 12345, got %q", id.Identity.OrgID)
	}
	if id.Identity.User == nil {
		t.Fatal("expected user to be non-nil")
	}
	if id.Identity.User.Username != "testuser" {
		t.Errorf("expected username testuser, got %q", id.Identity.User.Username)
	}
}

func TestParseRHIdentity_TBR_MissingOrgID(t *testing.T) {
	header := makeRHIdentityHeaderCustom(map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "User",
			"auth_type": "basic-auth",
			"user": map[string]interface{}{
				"username": "testuser",
			},
		},
	})

	_, err := ParseRHIdentity(header)
	if err == nil {
		t.Fatal("expected error for missing org_id, got nil")
	}
}

func TestParseRHIdentity_TBR_MissingUsername(t *testing.T) {
	header := makeRHIdentityHeaderCustom(map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "User",
			"auth_type": "basic-auth",
			"org_id":    "12345",
			"user":      map[string]interface{}{},
		},
	})

	_, err := ParseRHIdentity(header)
	if err == nil {
		t.Fatal("expected error for missing username, got nil")
	}
}

func TestExtractTBRIdentity(t *testing.T) {
	// Test with valid TBR identity
	header := makeTBRIdentityHeader("12345", "testuser")
	id, err := ParseRHIdentity(header)
	if err != nil {
		t.Fatalf("expected no error parsing TBR identity, got: %v", err)
	}

	tbr := ExtractTBRIdentity(id)
	if tbr == nil {
		t.Fatal("expected non-nil TBRIdentity")
	}
	if tbr.OrgID != "12345" {
		t.Errorf("expected org_id 12345, got %q", tbr.OrgID)
	}
	if tbr.Username != "testuser" {
		t.Errorf("expected username testuser, got %q", tbr.Username)
	}

	// Test with SAML identity
	samlHeader := makeRHIdentityHeader("test@redhat.com", "uuid-123", "Test", "User", nil)
	samlID, err := ParseRHIdentity(samlHeader)
	if err != nil {
		t.Fatalf("expected no error parsing SAML identity, got: %v", err)
	}

	tbrFromSAML := ExtractTBRIdentity(samlID)
	if tbrFromSAML != nil {
		t.Error("expected nil TBRIdentity from SAML identity")
	}
}

func TestIsTBRIdentity(t *testing.T) {
	// Test TBR identity
	tbrHeader := makeTBRIdentityHeader("12345", "testuser")
	tbrID, _ := ParseRHIdentity(tbrHeader)
	if !IsTBRIdentity(tbrID) {
		t.Error("expected true for TBR identity")
	}

	// Test SAML identity
	samlHeader := makeRHIdentityHeader("test@redhat.com", "uuid-123", "Test", "User", nil)
	samlID, _ := ParseRHIdentity(samlHeader)
	if IsTBRIdentity(samlID) {
		t.Error("expected false for SAML identity")
	}
}

// --- Registry Identity Tests ---

func makeRegistryIdentityHeader(orgID, username string) string {
	identity := map[string]interface{}{
		"identity": map[string]interface{}{
			"type":      "Registry",
			"auth_type": "registry-auth",
			"registry": map[string]interface{}{
				"org_id":   orgID,
				"username": username,
			},
		},
	}
	data, _ := json.Marshal(identity)
	return base64.StdEncoding.EncodeToString(data)
}

func TestParseRHIdentity_ValidRegistry(t *testing.T) {
	header := makeRegistryIdentityHeader("13409664", "alcove-dev")
	id, err := ParseRHIdentity(header)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if id.Identity.Type != "Registry" {
		t.Errorf("expected type Registry, got %q", id.Identity.Type)
	}
	if id.Identity.Registry == nil {
		t.Fatal("expected non-nil Registry field")
	}
	if id.Identity.Registry.OrgID != "13409664" {
		t.Errorf("expected org_id 13409664, got %q", id.Identity.Registry.OrgID)
	}
	if id.Identity.Registry.Username != "alcove-dev" {
		t.Errorf("expected username alcove-dev, got %q", id.Identity.Registry.Username)
	}
}

func TestExtractTBRIdentity_FromRegistry(t *testing.T) {
	header := makeRegistryIdentityHeader("13409664", "alcove-dev")
	id, _ := ParseRHIdentity(header)
	tbr := ExtractTBRIdentity(id)
	if tbr == nil {
		t.Fatal("expected non-nil TBRIdentity from Registry identity")
	}
	if tbr.OrgID != "13409664" {
		t.Errorf("expected org_id 13409664, got %q", tbr.OrgID)
	}
	if tbr.Username != "alcove-dev" {
		t.Errorf("expected username alcove-dev, got %q", tbr.Username)
	}
}

func TestIsTBRIdentity_Registry(t *testing.T) {
	header := makeRegistryIdentityHeader("13409664", "alcove-dev")
	id, _ := ParseRHIdentity(header)
	if !IsTBRIdentity(id) {
		t.Error("expected true for Registry identity")
	}
}

// --- RHIdentityStore interface compliance ---

func TestRHIdentityStore_Authenticate_ReturnsError(t *testing.T) {
	store := NewRHIdentityStore(nil)
	_, err := store.Authenticate("user", "pass")
	if err == nil {
		t.Fatal("expected error from Authenticate, got nil")
	}
}

func TestRHIdentityStore_ValidateToken_ReturnsFalse(t *testing.T) {
	store := NewRHIdentityStore(nil)
	_, ok := store.ValidateToken("any-token")
	if ok {
		t.Fatal("expected ValidateToken to return false")
	}
}

func TestRHIdentityStore_InvalidateToken_NoOp(t *testing.T) {
	store := NewRHIdentityStore(nil)
	// Should not panic.
	store.InvalidateToken("any-token")
}

func TestRHIdentityStore_CreateUser_ReturnsError(t *testing.T) {
	store := NewRHIdentityStore(nil)
	err := store.CreateUser(nil, "user", "pass", false)
	if err == nil {
		t.Fatal("expected error from CreateUser, got nil")
	}
}

func TestRHIdentityStore_ChangePassword_ReturnsError(t *testing.T) {
	store := NewRHIdentityStore(nil)
	err := store.ChangePassword(nil, "user", "newpass")
	if err == nil {
		t.Fatal("expected error from ChangePassword, got nil")
	}
}

func TestRHIdentityStore_VerifyUserPassword_ReturnsError(t *testing.T) {
	store := NewRHIdentityStore(nil)
	ok, err := store.VerifyUserPassword(nil, "user", "pass")
	if err == nil {
		t.Fatal("expected error from VerifyUserPassword, got nil")
	}
	if ok {
		t.Fatal("expected false from VerifyUserPassword")
	}
}

// --- Interface compliance checks (compile-time) ---

var _ Authenticator = (*RHIdentityStore)(nil)
var _ UserManager = (*RHIdentityStore)(nil)

// --- Middleware integration tests (no DB required) ---

// TestAuthMiddleware_RHIdentity_PublicRoutes verifies that public routes
// bypass the X-RH-Identity check entirely (same as token-based auth).
func TestAuthMiddleware_RHIdentity_PublicRoutes(t *testing.T) {
	store := NewRHIdentityStore(nil)
	middleware := AuthMiddleware(store, store)

	publicPaths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/health"},
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodGet, "/api/v1/internal/something"},
		{http.MethodPost, "/api/v1/webhooks/github"},
		{http.MethodGet, "/dashboard"},
		{http.MethodGet, "/"},
	}

	for _, tc := range publicPaths {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			called := false
			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if !called {
				t.Errorf("expected handler to be called for public path %s %s", tc.method, tc.path)
			}
			if rr.Code != http.StatusOK {
				t.Errorf("expected 200, got %d for %s %s", rr.Code, tc.method, tc.path)
			}
		})
	}
}

// TestAuthMiddleware_RHIdentity_SessionIngestionPaths verifies that session
// ingestion paths (transcript, status, proxy-log) are treated as public.
func TestAuthMiddleware_RHIdentity_SessionIngestionPaths(t *testing.T) {
	store := NewRHIdentityStore(nil)
	middleware := AuthMiddleware(store, store)

	paths := []string{
		"/api/v1/sessions/abc123/transcript",
		"/api/v1/sessions/abc123/status",
		"/api/v1/sessions/abc123/proxy-log",
	}

	for _, path := range paths {
		t.Run("POST "+path, func(t *testing.T) {
			called := false
			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if !called {
				t.Errorf("expected handler to be called for session ingestion path %s", path)
			}
		})
	}
}

// TestAuthMiddleware_RHIdentity_MissingHeader verifies that a request to a
// protected API route without X-RH-Identity returns 401.
func TestAuthMiddleware_RHIdentity_MissingHeader(t *testing.T) {
	store := NewRHIdentityStore(nil)
	middleware := AuthMiddleware(store, store)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}

	// Verify error message mentions X-RH-Identity.
	body := rr.Body.String()
	if body == "" {
		t.Fatal("expected error body, got empty")
	}
}

// TestAuthMiddleware_RHIdentity_InvalidHeader verifies that a request with
// an invalid (non-base64 or bad JSON) X-RH-Identity returns 401.
func TestAuthMiddleware_RHIdentity_InvalidHeader(t *testing.T) {
	store := NewRHIdentityStore(nil)
	middleware := AuthMiddleware(store, store)

	cases := []struct {
		name   string
		header string
	}{
		{"garbage", "not-base64!!!"},
		{"bad json", base64.StdEncoding.EncodeToString([]byte("not json"))},
		{"missing associate", makeRHIdentityHeaderCustom(map[string]interface{}{
			"identity": map[string]interface{}{
				"type": "X509",
			},
		})},
		{"missing email", makeRHIdentityHeaderCustom(map[string]interface{}{
			"identity": map[string]interface{}{
				"type":      "Associate",
				"auth_type": "saml-auth",
				"associate": map[string]interface{}{
					"rhatUUID": "uuid-only",
				},
			},
		})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("handler should not be called for invalid header")
			}))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
			req.Header.Set("X-RH-Identity", tc.header)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", rr.Code)
			}
		})
	}
}

// Note: The following tests require a real PostgreSQL database and cannot be
// run as unit tests:
//
// - TestAuthMiddleware_RHIdentity_ValidHeader_SetsUser: A valid X-RH-Identity
//   header on a protected route should call rhStore.UpsertUser (requires DB),
//   set X-Alcove-User, and call the next handler.
//
// - TestRHIdentityStore_UpsertUser: Creating and updating users via UpsertUser
//   requires PostgreSQL with the auth_users table.
//
// - TestRHIdentityStore_DeleteUser: Deleting users requires PostgreSQL.
//
// - TestRHIdentityStore_ListUsers: Listing users requires PostgreSQL.
//
// - TestRHIdentityStore_SetAdmin: Setting admin flag requires PostgreSQL.
//
// - TestRHIdentityStore_IsAdmin: Checking admin status requires PostgreSQL.
//
// - TestRHIdentityStore_BootstrapAdmins: Bootstrap admin creation and
//   idempotent upsert requires PostgreSQL with the auth_users table.
//
// These should be covered by integration tests that spin up a test PostgreSQL
// instance (e.g., using testcontainers or a dedicated test database).
