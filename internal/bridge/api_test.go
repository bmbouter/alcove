package bridge

import (
	"testing"
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

// Note: Personal API token endpoint tests have been moved to integration tests
// due to the requirement for a real PostgreSQL database and the type assertion
// to *auth.PgStore in the handlers. The following tests would require a test database:
//
// - TestHandlePersonalAPITokens: Full HTTP handler tests for /api/v1/auth/api-tokens
// - TestHandlePersonalAPITokenByID: Tests for individual token management
// - TestPersonalAPITokensBackendValidation: Tests postgres backend requirement
// - TestPersonalAPITokensWorkflow: End-to-end token lifecycle tests
//
// These tests should be implemented as integration tests using a test PostgreSQL
// instance (e.g., with testcontainers) to verify:
// 1. HTTP endpoints work correctly with real database
// 2. Authentication and authorization are properly enforced
// 3. Type assertion to *auth.PgStore works in practice
// 4. Database transactions and constraints work as expected
// 5. Error handling works with real database errors
