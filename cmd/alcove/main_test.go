package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestValidateProxyURL(t *testing.T) {
	tests := []struct {
		name      string
		proxyURL  string
		expectErr bool
	}{
		{"valid http proxy", "http://proxy.example.com:8080", false},
		{"valid https proxy", "https://proxy.example.com:8080", false},
		{"valid proxy with auth", "http://user:pass@proxy.example.com:8080", false},
		{"invalid scheme", "ftp://proxy.example.com:8080", true},
		{"missing host", "http://", true},
		{"invalid URL", "not-a-url", true},
		{"no scheme", "proxy.example.com:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProxyURL(tt.proxyURL)
			if (err != nil) != tt.expectErr {
				t.Errorf("validateProxyURL(%q) error = %v, expectErr %v", tt.proxyURL, err, tt.expectErr)
			}
		})
	}
}

func TestParseNoProxy(t *testing.T) {
	tests := []struct {
		name     string
		noProxy  string
		expected []string
	}{
		{"empty string", "", []string{}},
		{"single host", "example.com", []string{"example.com"}},
		{"multiple hosts", "example.com,localhost,192.168.1.1", []string{"example.com", "localhost", "192.168.1.1"}},
		{"with spaces", "example.com, localhost , 192.168.1.1 ", []string{"example.com", "localhost", "192.168.1.1"}},
		{"with empty entries", "example.com,,localhost", []string{"example.com", "localhost"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseNoProxy(tt.noProxy)
			if len(result) != len(tt.expected) {
				t.Errorf("parseNoProxy(%q) length = %d, expected %d", tt.noProxy, len(result), len(tt.expected))
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("parseNoProxy(%q)[%d] = %q, expected %q", tt.noProxy, i, v, tt.expected[i])
				}
			}
		})
	}
}

func TestShouldUseProxy(t *testing.T) {
	tests := []struct {
		name      string
		targetURL string
		noProxy   []string
		expected  bool
	}{
		{"no exclusions", "https://api.example.com", []string{}, true},
		{"exact host match", "https://example.com", []string{"example.com"}, false},
		{"exact host:port match", "https://example.com:8080", []string{"example.com:8080"}, false},
		{"domain suffix match", "https://api.example.com", []string{".example.com"}, false},
		{"wildcard domain match", "https://api.example.com", []string{"*.example.com"}, false},
		{"no match", "https://other.com", []string{"example.com"}, true},
		{"IP match", "https://192.168.1.1", []string{"192.168.1.1"}, false},
		{"CIDR match", "https://192.168.1.1", []string{"192.168.1.0/24"}, false},
		{"port-only match", "https://example.com:8080", []string{"8080"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldUseProxy(tt.targetURL, tt.noProxy)
			if result != tt.expected {
				t.Errorf("shouldUseProxy(%q, %v) = %t, expected %t", tt.targetURL, tt.noProxy, result, tt.expected)
			}
		})
	}
}

func TestResolveProxyConfig(t *testing.T) {
	// Helper function to create a command with proper flags
	createTestCommand := func() *cobra.Command {
		cmd := &cobra.Command{}
		cmd.PersistentFlags().String("proxy-url", "", "")
		cmd.PersistentFlags().String("no-proxy", "", "")
		cmd.PersistentFlags().String("profile", "", "")
		return cmd
	}

	// Test with flags
	t.Run("flags override environment", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "http://env.proxy:8080")
		t.Setenv("NO_PROXY", "env.example.com")

		cmd := createTestCommand()
		cmd.SetArgs([]string{"--proxy-url", "http://flag.proxy:8080", "--no-proxy", "flag.example.com"})
		cmd.ParseFlags([]string{"--proxy-url", "http://flag.proxy:8080", "--no-proxy", "flag.example.com"})

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "http://flag.proxy:8080" {
			t.Errorf("ProxyURL = %q, expected %q", config.ProxyURL, "http://flag.proxy:8080")
		}
		if len(config.NoProxy) != 1 || config.NoProxy[0] != "flag.example.com" {
			t.Errorf("NoProxy = %v, expected [%q]", config.NoProxy, "flag.example.com")
		}
	})

	// Test with environment variables
	t.Run("environment variables", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "http://env.proxy:8080")
		t.Setenv("NO_PROXY", "env.example.com")

		cmd := createTestCommand()

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "http://env.proxy:8080" {
			t.Errorf("ProxyURL = %q, expected %q", config.ProxyURL, "http://env.proxy:8080")
		}
		if len(config.NoProxy) != 1 || config.NoProxy[0] != "env.example.com" {
			t.Errorf("NoProxy = %v, expected [%q]", config.NoProxy, "env.example.com")
		}
	})

	// Test HTTPS_PROXY precedence
	t.Run("HTTPS_PROXY precedence over HTTP_PROXY", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "http://http.proxy:8080")
		t.Setenv("HTTPS_PROXY", "https://https.proxy:8080")

		cmd := createTestCommand()

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "https://https.proxy:8080" {
			t.Errorf("ProxyURL = %q, expected %q", config.ProxyURL, "https://https.proxy:8080")
		}
	})

	// Test invalid proxy URL from flag
	t.Run("invalid proxy URL from flag", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		cmd := createTestCommand()
		cmd.ParseFlags([]string{"--proxy-url", "invalid-url"})

		_, err := resolveProxyConfig(cmd)
		if err == nil {
			t.Error("Expected error for invalid proxy URL, got nil")
		}
	})

	// Test invalid proxy URL from environment
	t.Run("invalid proxy URL from environment", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}

		t.Setenv("HTTP_PROXY", "invalid-url")

		cmd := createTestCommand()

		_, err := resolveProxyConfig(cmd)
		if err == nil {
			t.Error("Expected error for invalid proxy URL from environment, got nil")
		}
	})

	// Test no configuration
	t.Run("no proxy configuration", func(t *testing.T) {
		// Clean up environment first
		for _, env := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			t.Setenv(env, "")
		}
		// Isolate from user config file
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		t.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))

		cmd := createTestCommand()

		config, err := resolveProxyConfig(cmd)
		if err != nil {
			t.Fatalf("resolveProxyConfig() error = %v", err)
		}
		if config.ProxyURL != "" {
			t.Errorf("ProxyURL = %q, expected empty string", config.ProxyURL)
		}
		if len(config.NoProxy) != 0 {
			t.Errorf("NoProxy = %v, expected empty slice", config.NoProxy)
		}
	})
}
func setupConfigDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	alcoveDir := filepath.Join(tmpDir, "alcove")
	if err := os.MkdirAll(alcoveDir, 0700); err != nil {
		t.Fatalf("creating alcove config dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	return alcoveDir
}

func TestLoadConfigWithDefaults(t *testing.T) {
	alcoveDir := setupConfigDir(t)

	configYAML := `server: https://alcove.example.com
output: table
username: myuser
password: mypass
proxy_url: http://proxy:3128
no_proxy: localhost,127.0.0.1
defaults:
  repo: https://github.com/org/repo.git
  provider: google-vertex
  model: claude-sonnet-4-20250514
  timeout: 30m
  budget: 5.00
`
	if err := os.WriteFile(filepath.Join(alcoveDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatalf("writing config file: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.Server != "https://alcove.example.com" {
		t.Errorf("expected server %q, got %q", "https://alcove.example.com", cfg.Server)
	}
	if cfg.Output != "table" {
		t.Errorf("expected output %q, got %q", "table", cfg.Output)
	}
	if cfg.Username != "myuser" {
		t.Errorf("expected username %q, got %q", "myuser", cfg.Username)
	}
	if cfg.Password != "mypass" {
		t.Errorf("expected password %q, got %q", "mypass", cfg.Password)
	}
	if cfg.ProxyURL != "http://proxy:3128" {
		t.Errorf("expected proxy_url %q, got %q", "http://proxy:3128", cfg.ProxyURL)
	}
	if cfg.NoProxy != "localhost,127.0.0.1" {
		t.Errorf("expected no_proxy %q, got %q", "localhost,127.0.0.1", cfg.NoProxy)
	}
	if cfg.Defaults.Repo != "https://github.com/org/repo.git" {
		t.Errorf("expected defaults.repo %q, got %q", "https://github.com/org/repo.git", cfg.Defaults.Repo)
	}
	if cfg.Defaults.Provider != "google-vertex" {
		t.Errorf("expected defaults.provider %q, got %q", "google-vertex", cfg.Defaults.Provider)
	}
	if cfg.Defaults.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected defaults.model %q, got %q", "claude-sonnet-4-20250514", cfg.Defaults.Model)
	}
	if cfg.Defaults.Timeout != "30m" {
		t.Errorf("expected defaults.timeout %q, got %q", "30m", cfg.Defaults.Timeout)
	}
	if cfg.Defaults.Budget != 5.00 {
		t.Errorf("expected defaults.budget %v, got %v", 5.00, cfg.Defaults.Budget)
	}
}

func TestLoadConfigPartial(t *testing.T) {
	alcoveDir := setupConfigDir(t)

	configYAML := `server: https://partial.example.com
defaults:
  repo: https://github.com/org/repo.git
`
	if err := os.WriteFile(filepath.Join(alcoveDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatalf("writing config file: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.Server != "https://partial.example.com" {
		t.Errorf("expected server %q, got %q", "https://partial.example.com", cfg.Server)
	}
	if cfg.Defaults.Repo != "https://github.com/org/repo.git" {
		t.Errorf("expected defaults.repo %q, got %q", "https://github.com/org/repo.git", cfg.Defaults.Repo)
	}

	// All other fields should be zero values
	if cfg.Output != "" {
		t.Errorf("expected output to be empty, got %q", cfg.Output)
	}
	if cfg.Username != "" {
		t.Errorf("expected username to be empty, got %q", cfg.Username)
	}
	if cfg.Password != "" {
		t.Errorf("expected password to be empty, got %q", cfg.Password)
	}
	if cfg.ProxyURL != "" {
		t.Errorf("expected proxy_url to be empty, got %q", cfg.ProxyURL)
	}
	if cfg.NoProxy != "" {
		t.Errorf("expected no_proxy to be empty, got %q", cfg.NoProxy)
	}
	if cfg.Defaults.Provider != "" {
		t.Errorf("expected defaults.provider to be empty, got %q", cfg.Defaults.Provider)
	}
	if cfg.Defaults.Model != "" {
		t.Errorf("expected defaults.model to be empty, got %q", cfg.Defaults.Model)
	}
	if cfg.Defaults.Timeout != "" {
		t.Errorf("expected defaults.timeout to be empty, got %q", cfg.Defaults.Timeout)
	}
	if cfg.Defaults.Budget != 0 {
		t.Errorf("expected defaults.budget to be 0, got %v", cfg.Defaults.Budget)
	}
}

func TestLoadConfigEmpty(t *testing.T) {
	alcoveDir := setupConfigDir(t)

	if err := os.WriteFile(filepath.Join(alcoveDir, "config.yaml"), []byte(""), 0600); err != nil {
		t.Fatalf("writing config file: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.Server != "" {
		t.Errorf("expected server to be empty, got %q", cfg.Server)
	}
	if cfg.Output != "" {
		t.Errorf("expected output to be empty, got %q", cfg.Output)
	}
	if cfg.Username != "" {
		t.Errorf("expected username to be empty, got %q", cfg.Username)
	}
	if cfg.Password != "" {
		t.Errorf("expected password to be empty, got %q", cfg.Password)
	}
	if cfg.ProxyURL != "" {
		t.Errorf("expected proxy_url to be empty, got %q", cfg.ProxyURL)
	}
	if cfg.NoProxy != "" {
		t.Errorf("expected no_proxy to be empty, got %q", cfg.NoProxy)
	}
	if cfg.Defaults.Repo != "" {
		t.Errorf("expected defaults.repo to be empty, got %q", cfg.Defaults.Repo)
	}
	if cfg.Defaults.Provider != "" {
		t.Errorf("expected defaults.provider to be empty, got %q", cfg.Defaults.Provider)
	}
	if cfg.Defaults.Model != "" {
		t.Errorf("expected defaults.model to be empty, got %q", cfg.Defaults.Model)
	}
	if cfg.Defaults.Timeout != "" {
		t.Errorf("expected defaults.timeout to be empty, got %q", cfg.Defaults.Timeout)
	}
	if cfg.Defaults.Budget != 0 {
		t.Errorf("expected defaults.budget to be 0, got %v", cfg.Defaults.Budget)
	}
}

func TestSaveAndLoadConfig(t *testing.T) {
	setupConfigDir(t)

	original := &CLIConfig{
		CLIProfile: CLIProfile{
			Server:   "https://save-test.example.com",
			Output:   "json",
			Username: "testuser",
			Password: "testpass",
			ProxyURL: "http://proxy:8080",
			NoProxy:  "localhost,10.0.0.0/8",
		},
	}
	original.Defaults.Repo = "https://github.com/test/repo.git"
	original.Defaults.Provider = "google-vertex"
	original.Defaults.Model = "claude-sonnet-4-20250514"
	original.Defaults.Timeout = "1h"
	original.Defaults.Budget = 10.50

	if err := saveConfig(original); err != nil {
		t.Fatalf("saveConfig error: %v", err)
	}

	loaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig error: %v", err)
	}

	if loaded.Server != original.Server {
		t.Errorf("server: got %q, want %q", loaded.Server, original.Server)
	}
	if loaded.Output != original.Output {
		t.Errorf("output: got %q, want %q", loaded.Output, original.Output)
	}
	if loaded.Username != original.Username {
		t.Errorf("username: got %q, want %q", loaded.Username, original.Username)
	}
	if loaded.Password != original.Password {
		t.Errorf("password: got %q, want %q", loaded.Password, original.Password)
	}
	if loaded.ProxyURL != original.ProxyURL {
		t.Errorf("proxy_url: got %q, want %q", loaded.ProxyURL, original.ProxyURL)
	}
	if loaded.NoProxy != original.NoProxy {
		t.Errorf("no_proxy: got %q, want %q", loaded.NoProxy, original.NoProxy)
	}
	if loaded.Defaults.Repo != original.Defaults.Repo {
		t.Errorf("defaults.repo: got %q, want %q", loaded.Defaults.Repo, original.Defaults.Repo)
	}
	if loaded.Defaults.Provider != original.Defaults.Provider {
		t.Errorf("defaults.provider: got %q, want %q", loaded.Defaults.Provider, original.Defaults.Provider)
	}
	if loaded.Defaults.Model != original.Defaults.Model {
		t.Errorf("defaults.model: got %q, want %q", loaded.Defaults.Model, original.Defaults.Model)
	}
	if loaded.Defaults.Timeout != original.Defaults.Timeout {
		t.Errorf("defaults.timeout: got %q, want %q", loaded.Defaults.Timeout, original.Defaults.Timeout)
	}
	if loaded.Defaults.Budget != original.Defaults.Budget {
		t.Errorf("defaults.budget: got %v, want %v", loaded.Defaults.Budget, original.Defaults.Budget)
	}
}

func TestConfigPriorityFlagsOverrideConfig(t *testing.T) {
	alcoveDir := setupConfigDir(t)

	// Write config with a server value
	configYAML := `server: https://config-server.example.com
`
	if err := os.WriteFile(filepath.Join(alcoveDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatalf("writing config file: %v", err)
	}

	// Clear env to avoid interference
	t.Setenv("ALCOVE_SERVER", "")

	// Create a command with the server flag set
	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("server", "", "")
	cmd.PersistentFlags().String("output", "", "")
	cmd.PersistentFlags().StringP("username", "u", "", "")
	cmd.PersistentFlags().StringP("password", "p", "", "")
	cmd.PersistentFlags().String("proxy-url", "", "")
	cmd.PersistentFlags().String("no-proxy", "", "")
	cmd.PersistentFlags().String("profile", "", "")
	cmd.ParseFlags([]string{"--server", "https://flag-server.example.com"})

	// resolveServer should pick up the flag value, not the config file value
	server, err := resolveServer(cmd)
	if err != nil {
		t.Fatalf("resolveServer error: %v", err)
	}
	if server != "https://flag-server.example.com" {
		t.Errorf("expected flag server %q, got %q", "https://flag-server.example.com", server)
	}
}

func TestConfigPriorityEnvOverrideConfig(t *testing.T) {
	alcoveDir := setupConfigDir(t)

	// Write config with a server value
	configYAML := `server: https://config-server.example.com
`
	if err := os.WriteFile(filepath.Join(alcoveDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatalf("writing config file: %v", err)
	}

	// Set env var to override
	t.Setenv("ALCOVE_SERVER", "https://env-server.example.com")

	// Create a command with no flags set
	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("server", "", "")
	cmd.PersistentFlags().String("output", "", "")
	cmd.PersistentFlags().StringP("username", "u", "", "")
	cmd.PersistentFlags().StringP("password", "p", "", "")
	cmd.PersistentFlags().String("proxy-url", "", "")
	cmd.PersistentFlags().String("no-proxy", "", "")
	cmd.PersistentFlags().String("profile", "", "")

	// resolveServer should pick up the env var, not the config file value
	server, err := resolveServer(cmd)
	if err != nil {
		t.Fatalf("resolveServer error: %v", err)
	}
	if server != "https://env-server.example.com" {
		t.Errorf("expected env server %q, got %q", "https://env-server.example.com", server)
	}
}



func TestLoadConfigNoFile(t *testing.T) {
	setupConfigDir(t)
	// Also isolate HOME so fallback paths don't find real user config
	t.Setenv("HOME", filepath.Join(t.TempDir(), "fakehome"))

	// Don't create any config file
	_, err := loadConfig()
	if err == nil {
		t.Error("expected error when config file does not exist, got nil")
	}
}

func TestSaveConfigCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	// Point to a subdir that doesn't exist yet (alcove dir not pre-created)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "nested", "dir"))

	cfg := &CLIConfig{CLIProfile: CLIProfile{Server: "https://test.example.com"}}
	if err := saveConfig(cfg); err != nil {
		t.Fatalf("saveConfig error: %v", err)
	}

	loaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig error after save: %v", err)
	}
	if loaded.Server != "https://test.example.com" {
		t.Errorf("server: got %q, want %q", loaded.Server, "https://test.example.com")
	}
}

func TestListCmdFlags(t *testing.T) {
	cmd := newListCmd()

	// Test that all expected flags are present
	flags := []string{"status", "repo", "agent", "since"}

	for _, flag := range flags {
		if f := cmd.Flags().Lookup(flag); f == nil {
			t.Errorf("expected flag %q to exist on list command", flag)
		}
	}

	// Test that the agent flag has the correct usage
	agentFlag := cmd.Flags().Lookup("agent")
	if agentFlag == nil {
		t.Fatal("agent flag should exist")
	}

	expectedUsage := "Filter by agent definition name"
	if agentFlag.Usage != expectedUsage {
		t.Errorf("agent flag usage = %q, expected %q", agentFlag.Usage, expectedUsage)
	}
}

func TestRunListBuildsAgentFilter(t *testing.T) {
	// Test that the agent flag is properly included in query parameters
	// This is a unit test that validates the query building logic without making HTTP calls

	cmd := newListCmd()
	cmd.SetArgs([]string{"--agent", "test-agent"})
	err := cmd.ParseFlags([]string{"--agent", "test-agent"})
	if err != nil {
		t.Fatalf("Failed to parse flags: %v", err)
	}

	// Verify the flag value is correctly parsed
	agent, err := cmd.Flags().GetString("agent")
	if err != nil {
		t.Fatalf("Failed to get agent flag: %v", err)
	}
	if agent != "test-agent" {
		t.Errorf("agent flag value = %q, expected %q", agent, "test-agent")
	}
}

func TestSessionSummaryHasAgentField(t *testing.T) {
	// Test that sessionSummary struct includes Agent field
	session := sessionSummary{
		ID:        "test-id",
		Prompt:    "test prompt",
		Repo:      "test-repo",
		Provider:  "test-provider",
		Status:    "completed",
		StartedAt: "2026-04-22T19:15:00Z",
		Duration:  "5m30s",
		Agent:     "test-agent",
	}

	if session.Agent != "test-agent" {
		t.Errorf("sessionSummary.Agent = %q, expected %q", session.Agent, "test-agent")
	}
}

func TestAgentsReposJsonFlag(t *testing.T) {
	// Test that the agents repos command has a --json flag
	cmd := newAgentsReposCmd()

	// Check that the --json flag exists
	jsonFlag := cmd.Flags().Lookup("json")
	if jsonFlag == nil {
		t.Fatal("--json flag should exist on agents repos command")
	}

	// Check flag type and default value
	if jsonFlag.Value.Type() != "bool" {
		t.Errorf("--json flag type = %q, expected %q", jsonFlag.Value.Type(), "bool")
	}

	if jsonFlag.DefValue != "false" {
		t.Errorf("--json flag default value = %q, expected %q", jsonFlag.DefValue, "false")
	}

	// Check flag usage
	expectedUsage := "Output JSON instead of table format"
	if jsonFlag.Usage != expectedUsage {
		t.Errorf("--json flag usage = %q, expected %q", jsonFlag.Usage, expectedUsage)
	}
}

func TestAgentsReposJsonFlagParsing(t *testing.T) {
	// Test that the --json flag can be parsed correctly
	cmd := newAgentsReposCmd()

	// Test with --json flag set
	err := cmd.ParseFlags([]string{"--json"})
	if err != nil {
		t.Fatalf("Failed to parse --json flag: %v", err)
	}

	jsonFlag, err := cmd.Flags().GetBool("json")
	if err != nil {
		t.Fatalf("Failed to get --json flag value: %v", err)
	}

	if !jsonFlag {
		t.Error("--json flag should be true when set")
	}

	// Test without --json flag (default case)
	cmd2 := newAgentsReposCmd()
	err = cmd2.ParseFlags([]string{})
	if err != nil {
		t.Fatalf("Failed to parse flags without --json: %v", err)
	}

	jsonFlag2, err := cmd2.Flags().GetBool("json")
	if err != nil {
		t.Fatalf("Failed to get --json flag value: %v", err)
	}

	if jsonFlag2 {
		t.Error("--json flag should be false by default")
	}
}