package main

import (
	"os"
	"testing"
)

func TestCategorize(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"TASK_ID", "Infrastructure"},
		{"SESSION_ID", "Infrastructure"},
		{"ANTHROPIC_API_KEY", "LLM Provider"},
		{"CLAUDE_MODEL", "LLM Provider"},
		{"GITHUB_TOKEN", "SCM Tokens"},
		{"JIRA_TOKEN", "SCM Tokens"},
		{"GITHUB_API_URL", "SCM Gateway URLs"},
		{"HTTP_PROXY", "Network Proxy"},
		{"ALCOVE_PLUGINS", "Plugins & Catalog"},
		{"GIT_AUTHOR_NAME", "Git Config"},
		{"HOME", "Runtime"},
		{"PATH", "Runtime"},
		{"MY_API_KEY", "Generic Secrets"},
		{"CUSTOM_SECRET", "Generic Secrets"},
		{"DB_PASSWORD", "Generic Secrets"},
		{"SOME_RANDOM_VAR", "Other"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := categorize(tt.key)
			if got != tt.want {
				t.Errorf("categorize(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestIsDummy(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"alcove-session-abc123", true},
		{"alcove-session-f8a3b2c1-1234-5678-9abc-def012345678", true},
		{"sk-placeholder-routed-through-gate", true},
		{"sk-ant-real-token", false},
		{"ghp_realtoken123", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := isDummy(tt.value)
			if got != tt.want {
				t.Errorf("isDummy(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestIsGateProxy(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"http://gate-abc123:8443", true},
		{"http://gate-abc123:8443/github", true},
		{"http://localhost:8080", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := isGateProxy(tt.value)
			if got != tt.want {
				t.Errorf("isGateProxy(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		key, value string
		want       string
	}{
		{"GITHUB_TOKEN", "alcove-session-abc", "dummy"},
		{"ANTHROPIC_API_KEY", "sk-placeholder-routed-through-gate", "dummy"},
		{"ANTHROPIC_BASE_URL", "http://gate-abc:8443", "gate-proxy"},
		{"MY_API_KEY", "sk-real-secret", "sensitive"},
		{"CUSTOM_SECRET", "real-value", "sensitive"},
		{"SESSION_TOKEN", "real-session-token", "sensitive"},
		{"TASK_ID", "abc-123", "safe"},
		{"HOME", "/home/skiff", "safe"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := classify(tt.key, tt.value)
			if got != tt.want {
				t.Errorf("classify(%q, %q) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}

func TestMask(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"sk-ant-real-long-token-here", "sk-ant-r..."},
		{"short", "***"},
		{"12345678", "***"},
		{"123456789", "12345678..."},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got := mask(tt.value)
			if got != tt.want {
				t.Errorf("mask(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestCheckInstallationOutsideSkiff(t *testing.T) {
	// Ensure TASK_ID is unset
	t.Setenv("TASK_ID", "")
	os.Unsetenv("TASK_ID")
	report := checkInstallation()
	if report != nil {
		t.Error("expected nil report outside Skiff")
	}
}

func TestCheckInstallationInsideSkiff(t *testing.T) {
	t.Setenv("TASK_ID", "test-123")
	t.Setenv("ALCOVE_PLUGINS", `[{"name":"code-review","source":"claude-plugins-official"}]`)
	t.Setenv("ALCOVE_SKILL_REPOS", `[{"url":"https://github.com/org/skills","name":"my-skills"}]`)
	t.Setenv("ALCOVE_MCP_CONFIG", `{"github":{"command":"npx"}}`)
	// Use a temp dir as HOME so we don't pick up the real ~/.claude.json
	t.Setenv("HOME", t.TempDir())

	report := checkInstallation()
	if report == nil {
		t.Fatal("expected non-nil report inside Skiff")
	}
	if len(report.Plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(report.Plugins))
	}
	if report.Plugins[0].Name != "code-review" {
		t.Errorf("expected plugin name 'code-review', got %q", report.Plugins[0].Name)
	}
	if len(report.SkillRepos) != 1 {
		t.Errorf("expected 1 skill repo, got %d", len(report.SkillRepos))
	}
	if len(report.MCPServers) != 1 {
		t.Errorf("expected 1 MCP server, got %d", len(report.MCPServers))
	}
}
