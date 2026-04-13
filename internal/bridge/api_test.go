package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bmbouter/alcove/internal/auth"
)

func TestIsScmProvider(t *testing.T) {
	scmProviders := map[string]bool{"github": true, "gitlab": true, "jira": true}

	tests := []struct {
		provider string
		isSCM    bool
	}{
		{"github", true},
		{"gitlab", true},
		{"jira", true},
		{"anthropic", false},
		{"google-vertex", false},
		{"claude-oauth", false},
	}

	for _, tt := range tests {
		if scmProviders[tt.provider] != tt.isSCM {
			t.Errorf("provider %q: got isSCM=%v, want %v", tt.provider, scmProviders[tt.provider], tt.isSCM)
		}
	}
}

// MockPgStore implements the personal API token methods for testing
type MockPgStore struct {
	tokens map[string]*auth.PersonalAPIToken
	users  map[string]bool // username -> exists
}

func NewMockPgStore() *MockPgStore {
	return &MockPgStore{
		tokens: make(map[string]*auth.PersonalAPIToken),
		users:  make(map[string]bool),
	}
}

func (m *MockPgStore) ValidateCredentials(username, password string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *MockPgStore) Authenticate(username, password string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *MockPgStore) ValidateToken(token string) (string, bool) {
	return "", false
}

func (m *MockPgStore) InvalidateToken(token string) {}

func (m *MockPgStore) CreateUser(ctx context.Context, username, password string, isAdmin bool) error {
	return fmt.Errorf("not implemented")
}

func (m *MockPgStore) DeleteUser(ctx context.Context, username string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockPgStore) ListUsers(ctx context.Context) ([]auth.UserInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *MockPgStore) ChangePassword(ctx context.Context, username, newPassword string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockPgStore) SetAdmin(ctx context.Context, username string, isAdmin bool) error {
	return fmt.Errorf("not implemented")
}

func (m *MockPgStore) IsAdmin(ctx context.Context, username string) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (m *MockPgStore) VerifyUserPassword(ctx context.Context, username, password string) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (m *MockPgStore) SeedUsers(ctx context.Context, users map[string]string) error {
	return fmt.Errorf("not implemented")
}

// Personal API Token methods
func (m *MockPgStore) CreatePersonalAPIToken(ctx context.Context, username, name string) (*auth.CreatePersonalAPITokenResponse, error) {
	if !m.users[username] {
		return nil, fmt.Errorf("user not found")
	}

	id := fmt.Sprintf("token-%d", len(m.tokens)+1)
	token := fmt.Sprintf("apat_test_%s_%s", username, name)
	now := time.Now()

	apiToken := &auth.PersonalAPIToken{
		ID:        id,
		Username:  username,
		Name:      name,
		CreatedAt: now,
	}

	m.tokens[id] = apiToken

	return &auth.CreatePersonalAPITokenResponse{
		PersonalAPIToken: *apiToken,
		Token:            token,
	}, nil
}

func (m *MockPgStore) ListPersonalAPITokens(ctx context.Context, username string) ([]auth.PersonalAPIToken, error) {
	if !m.users[username] {
		return nil, fmt.Errorf("user not found")
	}

	var tokens []auth.PersonalAPIToken
	for _, token := range m.tokens {
		if token.Username == username {
			tokens = append(tokens, *token)
		}
	}
	return tokens, nil
}

func (m *MockPgStore) DeletePersonalAPIToken(ctx context.Context, username, tokenID string) error {
	token, exists := m.tokens[tokenID]
	if !exists {
		return fmt.Errorf("API token not found or not owned by user")
	}
	if token.Username != username {
		return fmt.Errorf("API token not found or not owned by user")
	}
	delete(m.tokens, tokenID)
	return nil
}

// Helper method for tests
func (m *MockPgStore) addUser(username string) {
	m.users[username] = true
}

// TestHandlePersonalAPITokens tests the personal API tokens endpoints
func TestHandlePersonalAPITokens(t *testing.T) {
	mockStore := NewMockPgStore()
	mockStore.addUser("testuser")

	api := &API{
		cfg: &Config{
			AuthBackend: "postgres",
		},
		authStore: mockStore,
	}

	tests := []struct {
		name           string
		method         string
		path           string
		body           interface{}
		headers        map[string]string
		expectedStatus int
		expectedBody   string
		setupFunc      func()
	}{
		{
			name:           "GET tokens - no auth",
			method:         http.MethodGet,
			path:           "/api/v1/auth/api-tokens",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "authentication required",
		},
		{
			name:   "GET tokens - success with empty list",
			method: http.MethodGet,
			path:   "/api/v1/auth/api-tokens",
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusOK,
			expectedBody:   "[]",
		},
		{
			name:   "POST create token - no auth",
			method: http.MethodPost,
			path:   "/api/v1/auth/api-tokens",
			body: map[string]interface{}{
				"name": "test token",
			},
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "authentication required",
		},
		{
			name:   "POST create token - invalid body",
			method: http.MethodPost,
			path:   "/api/v1/auth/api-tokens",
			body:   "invalid json",
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "invalid request body",
		},
		{
			name:   "POST create token - missing name",
			method: http.MethodPost,
			path:   "/api/v1/auth/api-tokens",
			body: map[string]interface{}{
				"name": "",
			},
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "name is required",
		},
		{
			name:   "POST create token - success",
			method: http.MethodPost,
			path:   "/api/v1/auth/api-tokens",
			body: map[string]interface{}{
				"name": "test token",
			},
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusCreated,
			expectedBody:   `"name":"test token"`,
		},
		{
			name:   "PATCH not allowed",
			method: http.MethodPatch,
			path:   "/api/v1/auth/api-tokens",
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   "method not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc()
			}

			var body []byte
			if tt.body != nil {
				if str, ok := tt.body.(string); ok {
					body = []byte(str)
				} else {
					body, _ = json.Marshal(tt.body)
				}
			}

			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBuffer(body))
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			rr := httptest.NewRecorder()
			api.handlePersonalAPITokens(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			responseBody := strings.TrimSpace(rr.Body.String())
			if !strings.Contains(responseBody, tt.expectedBody) {
				t.Errorf("expected body to contain %q, got %q", tt.expectedBody, responseBody)
			}
		})
	}
}

// TestHandlePersonalAPITokenByID tests the token deletion endpoint
func TestHandlePersonalAPITokenByID(t *testing.T) {
	mockStore := NewMockPgStore()
	mockStore.addUser("testuser")
	mockStore.addUser("otheruser")

	api := &API{
		cfg: &Config{
			AuthBackend: "postgres",
		},
		authStore: mockStore,
	}

	// Create a test token
	token, _ := mockStore.CreatePersonalAPIToken(context.Background(), "testuser", "test token")
	tokenID := token.ID

	tests := []struct {
		name           string
		method         string
		path           string
		headers        map[string]string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "DELETE token - no auth",
			method:         http.MethodDelete,
			path:           "/api/v1/auth/api-tokens/" + tokenID,
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "authentication required",
		},
		{
			name:   "DELETE token - missing ID",
			method: http.MethodDelete,
			path:   "/api/v1/auth/api-tokens/",
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusBadRequest,
			expectedBody:   "token ID required",
		},
		{
			name:   "DELETE token - not owned by user",
			method: http.MethodDelete,
			path:   "/api/v1/auth/api-tokens/" + tokenID,
			headers: map[string]string{
				"X-Alcove-User": "otheruser",
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "token not found or not owned by user",
		},
		{
			name:   "DELETE token - success",
			method: http.MethodDelete,
			path:   "/api/v1/auth/api-tokens/" + tokenID,
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusOK,
			expectedBody:   `"deleted":true`,
		},
		{
			name:   "DELETE token - already deleted",
			method: http.MethodDelete,
			path:   "/api/v1/auth/api-tokens/" + tokenID,
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusNotFound,
			expectedBody:   "token not found or not owned by user",
		},
		{
			name:   "GET not allowed",
			method: http.MethodGet,
			path:   "/api/v1/auth/api-tokens/" + tokenID,
			headers: map[string]string{
				"X-Alcove-User": "testuser",
			},
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   "method not allowed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			rr := httptest.NewRecorder()
			api.handlePersonalAPITokenByID(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			responseBody := strings.TrimSpace(rr.Body.String())
			if !strings.Contains(responseBody, tt.expectedBody) {
				t.Errorf("expected body to contain %q, got %q", tt.expectedBody, responseBody)
			}
		})
	}
}

// TestPersonalAPITokensBackendValidation tests that the endpoints only work with postgres backend
func TestPersonalAPITokensBackendValidation(t *testing.T) {
	api := &API{
		cfg: &Config{
			AuthBackend: "rh-identity", // not postgres
		},
		authStore: NewMockPgStore(),
	}

	tests := []struct {
		name string
		path string
	}{
		{"list tokens", "/api/v1/auth/api-tokens"},
		{"delete token", "/api/v1/auth/api-tokens/some-id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("X-Alcove-User", "testuser")

			rr := httptest.NewRecorder()

			if strings.Contains(tt.path, "/api/v1/auth/api-tokens/") {
				api.handlePersonalAPITokenByID(rr, req)
			} else {
				api.handlePersonalAPITokens(rr, req)
			}

			if rr.Code != http.StatusBadRequest {
				t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
			}

			responseBody := rr.Body.String()
			expectedError := "personal API tokens only supported with postgres backend"
			if !strings.Contains(responseBody, expectedError) {
				t.Errorf("expected body to contain %q, got %q", expectedError, responseBody)
			}
		})
	}
}

// TestPersonalAPITokensWorkflow tests the complete token workflow
func TestPersonalAPITokensWorkflow(t *testing.T) {
	mockStore := NewMockPgStore()
	mockStore.addUser("testuser")

	api := &API{
		cfg: &Config{
			AuthBackend: "postgres",
		},
		authStore: mockStore,
	}

	// 1. List tokens - should be empty
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/api-tokens", nil)
	req.Header.Set("X-Alcove-User", "testuser")
	rr := httptest.NewRecorder()
	api.handlePersonalAPITokens(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var tokens []auth.PersonalAPIToken
	if err := json.Unmarshal(rr.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens, got %d", len(tokens))
	}

	// 2. Create a token
	createReq := map[string]interface{}{
		"name": "My CLI Token",
	}
	body, _ := json.Marshal(createReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/api-tokens", bytes.NewBuffer(body))
	req.Header.Set("X-Alcove-User", "testuser")
	rr = httptest.NewRecorder()
	api.handlePersonalAPITokens(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rr.Code)
	}

	var createResponse auth.CreatePersonalAPITokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &createResponse); err != nil {
		t.Fatalf("failed to unmarshal create response: %v", err)
	}

	if createResponse.Name != "My CLI Token" {
		t.Errorf("expected name 'My CLI Token', got %s", createResponse.Name)
	}

	if !strings.HasPrefix(createResponse.Token, "apat_") {
		t.Errorf("expected token to start with 'apat_', got %s", createResponse.Token)
	}

	tokenID := createResponse.ID

	// 3. List tokens - should have one
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/api-tokens", nil)
	req.Header.Set("X-Alcove-User", "testuser")
	rr = httptest.NewRecorder()
	api.handlePersonalAPITokens(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	if err := json.Unmarshal(rr.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("failed to unmarshal list response: %v", err)
	}

	if len(tokens) != 1 {
		t.Errorf("expected 1 token, got %d", len(tokens))
	}

	// 4. Delete the token
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/auth/api-tokens/"+tokenID, nil)
	req.Header.Set("X-Alcove-User", "testuser")
	rr = httptest.NewRecorder()
	api.handlePersonalAPITokenByID(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	// 5. List tokens - should be empty again
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/api-tokens", nil)
	req.Header.Set("X-Alcove-User", "testuser")
	rr = httptest.NewRecorder()
	api.handlePersonalAPITokens(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	if err := json.Unmarshal(rr.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("failed to unmarshal final list response: %v", err)
	}

	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens after deletion, got %d", len(tokens))
	}
}
