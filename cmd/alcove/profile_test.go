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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// setupTestConfig creates a temp config directory and writes the given YAML.
// It sets XDG_CONFIG_HOME and HOME to temp directories to fully isolate from
// the user's real config, and returns a cleanup function that restores the
// original environment.
func setupTestConfig(t *testing.T, configYAML string) func() {
	t.Helper()
	tmpDir := t.TempDir()
	alcoveDir := filepath.Join(tmpDir, "alcove")
	if err := os.MkdirAll(alcoveDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alcoveDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatal(err)
	}

	origXDG := os.Getenv("XDG_CONFIG_HOME")
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("ALCOVE_PROFILE")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	// Set HOME to a non-existent dir so fallback paths don't find real config
	os.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))
	os.Unsetenv("ALCOVE_PROFILE")

	return func() {
		if origXDG != "" {
			os.Setenv("XDG_CONFIG_HOME", origXDG)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
		if origHome != "" {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
		if origProfile != "" {
			os.Setenv("ALCOVE_PROFILE", origProfile)
		} else {
			os.Unsetenv("ALCOVE_PROFILE")
		}
	}
}

// newTestCmd creates a root cobra command with the persistent flags needed by resolve functions.
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	cmd.PersistentFlags().String("server", "", "")
	cmd.PersistentFlags().String("output", "", "")
	cmd.PersistentFlags().StringP("username", "u", "", "")
	cmd.PersistentFlags().StringP("password", "p", "", "")
	cmd.PersistentFlags().String("proxy-url", "", "")
	cmd.PersistentFlags().String("no-proxy", "", "")
	cmd.PersistentFlags().String("profile", "", "")
	return cmd
}

const testConfigMultiProfile = `
active_profile: staging
profiles:
  staging:
    server: https://staging.example.com
    username: stage-user
    password: stage-pass
    proxy_url: http://proxy.staging:3128
    output: json
    defaults:
      repo: org/staging-repo
      timeout: 15m
  production:
    server: https://prod.example.com
    username: prod-user
    password: prod-pass
    defaults:
      repo: org/prod-repo
      budget: 10.0
server: http://localhost:8080
username: local-user
password: local-pass
output: table
`

func TestResolveProfile_FlagOverride(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	cmd.ParseFlags([]string{"--profile", "production"})

	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Server != "https://prod.example.com" {
		t.Errorf("expected server https://prod.example.com, got %s", profile.Server)
	}
	if profile.Username != "prod-user" {
		t.Errorf("expected username prod-user, got %s", profile.Username)
	}
}

func TestResolveProfile_EnvOverride(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	os.Setenv("ALCOVE_PROFILE", "production")
	defer os.Unsetenv("ALCOVE_PROFILE")

	cmd := newTestCmd()

	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Server != "https://prod.example.com" {
		t.Errorf("expected server https://prod.example.com, got %s", profile.Server)
	}
}

func TestResolveProfile_ActiveProfile(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()

	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// active_profile is "staging" in the test config
	if profile.Server != "https://staging.example.com" {
		t.Errorf("expected server https://staging.example.com, got %s", profile.Server)
	}
	if profile.Username != "stage-user" {
		t.Errorf("expected username stage-user, got %s", profile.Username)
	}
}

func TestResolveProfile_DefaultFallback(t *testing.T) {
	configYAML := `
server: http://localhost:8080
username: local-user
password: local-pass
output: table
`
	cleanup := setupTestConfig(t, configYAML)
	defer cleanup()

	cmd := newTestCmd()

	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Server != "http://localhost:8080" {
		t.Errorf("expected server http://localhost:8080, got %s", profile.Server)
	}
	if profile.Username != "local-user" {
		t.Errorf("expected username local-user, got %s", profile.Username)
	}
	if profile.Output != "table" {
		t.Errorf("expected output table, got %s", profile.Output)
	}
}

func TestResolveProfile_NotFound(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	cmd.ParseFlags([]string{"--profile", "nonexistent"})

	_, err := resolveProfile(cmd)
	if err == nil {
		t.Fatal("expected error for nonexistent profile, got nil")
	}
	expected := `profile "nonexistent" not found in config`
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestResolveProfile_NoConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	origHome := os.Getenv("HOME")
	os.Setenv("XDG_CONFIG_HOME", tmpDir)
	// Set HOME to a non-existent dir so fallback paths don't find real config
	os.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))
	defer func() {
		if origXDG != "" {
			os.Setenv("XDG_CONFIG_HOME", origXDG)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
		if origHome != "" {
			os.Setenv("HOME", origHome)
		} else {
			os.Unsetenv("HOME")
		}
	}()

	cmd := newTestCmd()

	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return empty profile when no config
	if profile.Server != "" {
		t.Errorf("expected empty server, got %s", profile.Server)
	}
}

func TestResolveProfile_FlagOverridesEnv(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	os.Setenv("ALCOVE_PROFILE", "staging")
	defer os.Unsetenv("ALCOVE_PROFILE")

	cmd := newTestCmd()
	cmd.ParseFlags([]string{"--profile", "production"})

	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// --profile flag should win over ALCOVE_PROFILE env
	if profile.Server != "https://prod.example.com" {
		t.Errorf("expected production server, got %s", profile.Server)
	}
}

func TestResolveServer_UsesProfile(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	// Ensure no env vars interfere
	origServer := os.Getenv("ALCOVE_SERVER")
	os.Unsetenv("ALCOVE_SERVER")
	defer func() {
		if origServer != "" {
			os.Setenv("ALCOVE_SERVER", origServer)
		}
	}()

	cmd := newTestCmd()
	cmd.ParseFlags([]string{"--profile", "production"})

	server, err := resolveServer(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server != "https://prod.example.com" {
		t.Errorf("expected https://prod.example.com, got %s", server)
	}
}

func TestResolveBasicAuth_UsesProfile(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	origUser := os.Getenv("ALCOVE_USERNAME")
	origPass := os.Getenv("ALCOVE_PASSWORD")
	os.Unsetenv("ALCOVE_USERNAME")
	os.Unsetenv("ALCOVE_PASSWORD")
	defer func() {
		if origUser != "" {
			os.Setenv("ALCOVE_USERNAME", origUser)
		}
		if origPass != "" {
			os.Setenv("ALCOVE_PASSWORD", origPass)
		}
	}()

	cmd := newTestCmd()
	cmd.ParseFlags([]string{"--profile", "production"})

	username, password := resolveBasicAuth(cmd)
	if username != "prod-user" {
		t.Errorf("expected username prod-user, got %s", username)
	}
	if password != "prod-pass" {
		t.Errorf("expected password prod-pass, got %s", password)
	}
}

func TestProfileList_Empty(t *testing.T) {
	configYAML := `
server: http://localhost:8080
`
	cleanup := setupTestConfig(t, configYAML)
	defer cleanup()

	cmd := newTestCmd()
	err := runProfileList(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProfileList_WithProfiles(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	// Just verify it doesn't error; output goes to stdout/stderr
	err := runProfileList(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProfileUse_Success(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	err := runProfileUse(cmd, []string{"production"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the active_profile was saved
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.ActiveProfile != "production" {
		t.Errorf("expected active_profile production, got %s", cfg.ActiveProfile)
	}
}

func TestProfileUse_NotFound(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	err := runProfileUse(cmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent profile, got nil")
	}
}

func TestProfileAdd_Success(t *testing.T) {
	configYAML := `
server: http://localhost:8080
`
	cleanup := setupTestConfig(t, configYAML)
	defer cleanup()

	cmd := newProfileAddCmd()
	// Set local flags on the profile add command
	cmd.Flags().Set("server", "https://new.example.com")
	cmd.Flags().Set("username", "newuser")

	err := runProfileAdd(cmd, []string{"newprofile"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the profile was created
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	profile, ok := cfg.Profiles["newprofile"]
	if !ok {
		t.Fatal("expected profile newprofile to exist")
	}
	if profile.Server != "https://new.example.com" {
		t.Errorf("expected server https://new.example.com, got %s", profile.Server)
	}
	if profile.Username != "newuser" {
		t.Errorf("expected username newuser, got %s", profile.Username)
	}
}

func TestProfileAdd_AlreadyExists(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newProfileAddCmd()
	err := runProfileAdd(cmd, []string{"staging"})
	if err == nil {
		t.Fatal("expected error for existing profile, got nil")
	}
}

func TestProfileRemove_Success(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	err := runProfileRemove(cmd, []string{"production"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if _, ok := cfg.Profiles["production"]; ok {
		t.Error("expected production profile to be removed")
	}
}

func TestProfileRemove_ClearsActive(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	// staging is the active profile
	err := runProfileRemove(cmd, []string{"staging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.ActiveProfile != "" {
		t.Errorf("expected active_profile to be cleared, got %s", cfg.ActiveProfile)
	}
}

func TestProfileRemove_NotFound(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	err := runProfileRemove(cmd, []string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent profile, got nil")
	}
}

func TestConfigSet_OnActiveProfile(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	// active_profile is "staging", so config set should modify staging
	err := runConfigSet(cmd, []string{"server", "https://new-staging.example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	profile := cfg.Profiles["staging"]
	if profile.Server != "https://new-staging.example.com" {
		t.Errorf("expected server https://new-staging.example.com, got %s", profile.Server)
	}
}

func TestConfigSet_OnSpecificProfile(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	cmd.ParseFlags([]string{"--profile", "production"})

	err := runConfigSet(cmd, []string{"username", "new-prod-user"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	profile := cfg.Profiles["production"]
	if profile.Username != "new-prod-user" {
		t.Errorf("expected username new-prod-user, got %s", profile.Username)
	}
}

func TestConfigSet_OnTopLevel(t *testing.T) {
	configYAML := `
server: http://localhost:8080
`
	cleanup := setupTestConfig(t, configYAML)
	defer cleanup()

	cmd := newTestCmd()
	err := runConfigSet(cmd, []string{"server", "http://localhost:9090"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if cfg.Server != "http://localhost:9090" {
		t.Errorf("expected server http://localhost:9090, got %s", cfg.Server)
	}
}

func TestBackwardCompat_FlatConfig(t *testing.T) {
	// Verify that a config file with no profiles works exactly like before
	configYAML := `
server: http://localhost:8080
username: admin
password: secret
output: json
defaults:
  repo: myorg/myrepo
  timeout: 30m
  budget: 5.0
`
	cleanup := setupTestConfig(t, configYAML)
	defer cleanup()

	origServer := os.Getenv("ALCOVE_SERVER")
	os.Unsetenv("ALCOVE_SERVER")
	defer func() {
		if origServer != "" {
			os.Setenv("ALCOVE_SERVER", origServer)
		}
	}()

	origUser := os.Getenv("ALCOVE_USERNAME")
	origPass := os.Getenv("ALCOVE_PASSWORD")
	os.Unsetenv("ALCOVE_USERNAME")
	os.Unsetenv("ALCOVE_PASSWORD")
	defer func() {
		if origUser != "" {
			os.Setenv("ALCOVE_USERNAME", origUser)
		}
		if origPass != "" {
			os.Setenv("ALCOVE_PASSWORD", origPass)
		}
	}()

	cmd := newTestCmd()

	server, err := resolveServer(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server != "http://localhost:8080" {
		t.Errorf("expected http://localhost:8080, got %s", server)
	}

	username, password := resolveBasicAuth(cmd)
	if username != "admin" {
		t.Errorf("expected username admin, got %s", username)
	}
	if password != "secret" {
		t.Errorf("expected password secret, got %s", password)
	}

	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Defaults.Repo != "myorg/myrepo" {
		t.Errorf("expected repo myorg/myrepo, got %s", profile.Defaults.Repo)
	}
	if profile.Defaults.Budget != 5.0 {
		t.Errorf("expected budget 5.0, got %f", profile.Defaults.Budget)
	}
}

func TestResolveProfile_ProfileDefaults(t *testing.T) {
	cleanup := setupTestConfig(t, testConfigMultiProfile)
	defer cleanup()

	cmd := newTestCmd()
	// active_profile is "staging" which has defaults
	profile, err := resolveProfile(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Defaults.Repo != "org/staging-repo" {
		t.Errorf("expected repo org/staging-repo, got %s", profile.Defaults.Repo)
	}
	if profile.Defaults.Timeout != "15m" {
		t.Errorf("expected timeout 15m, got %s", profile.Defaults.Timeout)
	}
}
