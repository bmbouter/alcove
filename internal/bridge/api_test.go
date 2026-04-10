package bridge

import "testing"

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
