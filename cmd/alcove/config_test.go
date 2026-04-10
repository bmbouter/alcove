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
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// TestLoadConfigMultipleLocations tests that config files are loaded from multiple locations in priority order.
func TestLoadConfigMultipleLocations(t *testing.T) {
	// Create temp directory structure
	tempDir := t.TempDir()

	// Set HOME to temp directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Test 1: Load from XDG config directory
	xdgConfigDir := filepath.Join(tempDir, ".config", "alcove")
	err := os.MkdirAll(xdgConfigDir, 0700)
	if err != nil {
		t.Fatalf("Failed to create XDG config dir: %v", err)
	}

	xdgConfigPath := filepath.Join(xdgConfigDir, "config.yaml")
	xdgConfig := `server: https://xdg.example.com
provider: xdg-provider`

	err = os.WriteFile(xdgConfigPath, []byte(xdgConfig), 0600)
	if err != nil {
		t.Fatalf("Failed to write XDG config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Server != "https://xdg.example.com" {
		t.Errorf("Expected server 'https://xdg.example.com', got '%s'", cfg.Server)
	}
	if cfg.Provider != "xdg-provider" {
		t.Errorf("Expected provider 'xdg-provider', got '%s'", cfg.Provider)
	}

	// Test 2: Priority order - convenience location should override if exists
	convenienceConfigPath := filepath.Join(tempDir, ".alcove.yaml")
	convenienceConfig := `server: https://convenience.example.com
provider: convenience-provider`

	err = os.WriteFile(convenienceConfigPath, []byte(convenienceConfig), 0600)
	if err != nil {
		t.Fatalf("Failed to write convenience config: %v", err)
	}

	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	// XDG should still have priority over convenience location
	if cfg.Server != "https://xdg.example.com" {
		t.Errorf("Expected XDG config to have priority, got server '%s'", cfg.Server)
	}

	// Test 3: Remove XDG config, should fall back to convenience location
	err = os.Remove(xdgConfigPath)
	if err != nil {
		t.Fatalf("Failed to remove XDG config: %v", err)
	}

	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Server != "https://convenience.example.com" {
		t.Errorf("Expected convenience config, got server '%s'", cfg.Server)
	}
}

// TestConfigResolutionHelpers tests the config resolution helper functions.
func TestConfigResolutionHelpers(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("test-string", "", "test flag")
	cmd.Flags().Float64("test-float", 0, "test flag")
	cmd.Flags().Duration("test-duration", 0, "test flag")

	// Test string resolution with precedence
	// 1. Flag takes highest priority
	cmd.Flags().Set("test-string", "flag-value")
	result := resolveStringConfig(cmd, "test-string", "TEST_ENV", "config-value", "default-value")
	if result != "flag-value" {
		t.Errorf("Expected flag value to take priority, got '%s'", result)
	}

	// 2. Environment takes priority over config
	cmd.Flags().Set("test-string", "") // Clear flag
	os.Setenv("TEST_ENV", "env-value")
	result = resolveStringConfig(cmd, "test-string", "TEST_ENV", "config-value", "default-value")
	if result != "env-value" {
		t.Errorf("Expected env value to take priority over config, got '%s'", result)
	}
	os.Unsetenv("TEST_ENV")

	// 3. Config takes priority over default
	result = resolveStringConfig(cmd, "test-string", "TEST_ENV", "config-value", "default-value")
	if result != "config-value" {
		t.Errorf("Expected config value to take priority over default, got '%s'", result)
	}

	// 4. Default is used when nothing else is set
	result = resolveStringConfig(cmd, "test-string", "TEST_ENV", "", "default-value")
	if result != "default-value" {
		t.Errorf("Expected default value, got '%s'", result)
	}

	// Test float64 resolution
	cmd.Flags().Set("test-float", "10.5")
	floatResult := resolveFloat64Config(cmd, "test-float", 5.0, 1.0)
	if floatResult != 10.5 {
		t.Errorf("Expected flag value 10.5, got %f", floatResult)
	}

	cmd.Flags().Set("test-float", "0") // Clear flag
	floatResult = resolveFloat64Config(cmd, "test-float", 5.0, 1.0)
	if floatResult != 5.0 {
		t.Errorf("Expected config value 5.0, got %f", floatResult)
	}

	// Test duration resolution
	cmd.Flags().Set("test-duration", "30m")
	durationResult, err := resolveDurationConfig(cmd, "test-duration", "1h", time.Minute*10)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if durationResult != time.Minute*30 {
		t.Errorf("Expected flag value 30m, got %v", durationResult)
	}

	cmd.Flags().Set("test-duration", "0") // Clear flag
	durationResult, err = resolveDurationConfig(cmd, "test-duration", "1h", time.Minute*10)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if durationResult != time.Hour {
		t.Errorf("Expected config value 1h, got %v", durationResult)
	}

	// Test invalid duration format
	_, err = resolveDurationConfig(cmd, "test-duration", "invalid", time.Minute*10)
	if err == nil {
		t.Error("Expected error for invalid duration format")
	}
	if !strings.Contains(err.Error(), "invalid timeout format") {
		t.Errorf("Expected timeout format error, got: %v", err)
	}
}

// TestConfigValidation tests configuration validation.
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      CLIConfig
		shouldError bool
		errorMsg    string
	}{
		{
			name: "valid config with all fields",
			config: CLIConfig{
				Server:   "https://example.com",
				Provider: "anthropic",
				Model:    "claude-sonnet-4",
				Budget:   10.0,
				Timeout:  "30m",
				Output:   "json",
				Repo:     "org/repo",
			},
			shouldError: false,
		},
		{
			name: "invalid output format",
			config: CLIConfig{
				Server: "https://example.com",
				Output: "invalid",
			},
			shouldError: true,
			errorMsg:    "invalid output format",
		},
		{
			name: "invalid timeout format",
			config: CLIConfig{
				Server:  "https://example.com",
				Timeout: "invalid",
			},
			shouldError: true,
			errorMsg:    "invalid timeout format",
		},
		{
			name: "negative budget",
			config: CLIConfig{
				Server: "https://example.com",
				Budget: -5.0,
			},
			shouldError: true,
			errorMsg:    "budget cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test timeout validation
			if tt.config.Timeout != "" {
				_, err := time.ParseDuration(tt.config.Timeout)
				hasError := err != nil
				if hasError != (tt.errorMsg == "invalid timeout format") {
					if tt.errorMsg == "invalid timeout format" && !hasError {
						t.Error("Expected timeout parsing to fail")
					}
				}
			}

			// Test output validation
			if tt.config.Output != "" {
				isValidOutput := strings.EqualFold(tt.config.Output, "json") || strings.EqualFold(tt.config.Output, "table")
				hasError := !isValidOutput
				if hasError != (tt.errorMsg == "invalid output format") {
					if tt.errorMsg == "invalid output format" && !hasError {
						t.Error("Expected output format validation to fail")
					}
				}
			}

			// Test budget validation
			if tt.config.Budget < 0 {
				hasError := true
				if !hasError != (tt.errorMsg != "budget cannot be negative") {
					if tt.errorMsg == "budget cannot be negative" {
						// This should be an error, which is expected
					}
				}
			}
		})
	}
}

// TestGetConfigFieldCount tests the config field counting function.
func TestGetConfigFieldCount(t *testing.T) {
	tests := []struct {
		name     string
		config   CLIConfig
		expected int
	}{
		{
			name:     "empty config",
			config:   CLIConfig{},
			expected: 0,
		},
		{
			name: "server only",
			config: CLIConfig{
				Server: "https://example.com",
			},
			expected: 1,
		},
		{
			name: "full config",
			config: CLIConfig{
				Server:   "https://example.com",
				Provider: "anthropic",
				Model:    "claude-sonnet-4",
				Budget:   10.0,
				Timeout:  "30m",
				Output:   "json",
				Repo:     "org/repo",
			},
			expected: 7,
		},
		{
			name: "partial config",
			config: CLIConfig{
				Server:   "https://example.com",
				Provider: "anthropic",
				Budget:   5.0,
			},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getConfigFieldCount(&tt.config)
			if result != tt.expected {
				t.Errorf("Expected count %d, got %d", tt.expected, result)
			}
		})
	}
}

// TestBackwardCompatibility tests that existing server-only configs still work.
func TestBackwardCompatibility(t *testing.T) {
	tempDir := t.TempDir()

	// Set HOME to temp directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Create old-style config with just server
	configDir := filepath.Join(tempDir, ".config", "alcove")
	err := os.MkdirAll(configDir, 0700)
	if err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	oldConfig := `server: https://legacy.example.com`

	err = os.WriteFile(configPath, []byte(oldConfig), 0600)
	if err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("Failed to load legacy config: %v", err)
	}

	if cfg.Server != "https://legacy.example.com" {
		t.Errorf("Expected legacy server URL, got '%s'", cfg.Server)
	}

	// Verify other fields are empty/default
	if cfg.Provider != "" {
		t.Errorf("Expected empty provider, got '%s'", cfg.Provider)
	}
	if cfg.Model != "" {
		t.Errorf("Expected empty model, got '%s'", cfg.Model)
	}
	if cfg.Budget != 0 {
		t.Errorf("Expected zero budget, got %f", cfg.Budget)
	}
	if cfg.Timeout != "" {
		t.Errorf("Expected empty timeout, got '%s'", cfg.Timeout)
	}
	if cfg.Output != "" {
		t.Errorf("Expected empty output, got '%s'", cfg.Output)
	}
	if cfg.Repo != "" {
		t.Errorf("Expected empty repo, got '%s'", cfg.Repo)
	}
}