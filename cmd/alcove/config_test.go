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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestResolveStringConfig(t *testing.T) {
	tests := []struct {
		name         string
		flagValue    string
		envVar       string
		envValue     string
		configValue  string
		defaultValue string
		expected     string
		description  string
	}{
		{
			name:         "flag_overrides_all",
			flagValue:    "flag-value",
			envVar:       "TEST_ENV",
			envValue:     "env-value",
			configValue:  "config-value",
			defaultValue: "default-value",
			expected:     "flag-value",
			description:  "CLI flag should override environment, config, and default",
		},
		{
			name:         "env_overrides_config_and_default",
			flagValue:    "",
			envVar:       "TEST_ENV",
			envValue:     "env-value",
			configValue:  "config-value",
			defaultValue: "default-value",
			expected:     "env-value",
			description:  "Environment variable should override config and default",
		},
		{
			name:         "config_overrides_default",
			flagValue:    "",
			envVar:       "TEST_ENV",
			envValue:     "",
			configValue:  "config-value",
			defaultValue: "default-value",
			expected:     "config-value",
			description:  "Config should override default",
		},
		{
			name:         "default_when_nothing_set",
			flagValue:    "",
			envVar:       "TEST_ENV",
			envValue:     "",
			configValue:  "",
			defaultValue: "default-value",
			expected:     "default-value",
			description:  "Default should be used when nothing else is set",
		},
		{
			name:         "no_env_var",
			flagValue:    "",
			envVar:       "",
			envValue:     "",
			configValue:  "config-value",
			defaultValue: "default-value",
			expected:     "config-value",
			description:  "Should work when no env var is specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test command
			cmd := &cobra.Command{}
			cmd.Flags().String("test-flag", "", "test flag")

			// Set flag value if specified
			if tt.flagValue != "" {
				cmd.Flags().Set("test-flag", tt.flagValue)
			}

			// Set environment variable if specified
			if tt.envVar != "" && tt.envValue != "" {
				oldValue := os.Getenv(tt.envVar)
				os.Setenv(tt.envVar, tt.envValue)
				defer os.Setenv(tt.envVar, oldValue)
			}

			result := resolveStringConfig(cmd, "test-flag", tt.envVar, tt.configValue, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("%s: expected %q, got %q", tt.description, tt.expected, result)
			}
		})
	}
}

func TestResolveFloat64Config(t *testing.T) {
	tests := []struct {
		name         string
		flagValue    float64
		configValue  float64
		defaultValue float64
		expected     float64
		description  string
	}{
		{
			name:         "flag_overrides_config_and_default",
			flagValue:    5.5,
			configValue:  3.3,
			defaultValue: 1.1,
			expected:     5.5,
			description:  "CLI flag should override config and default",
		},
		{
			name:         "config_overrides_default",
			flagValue:    0,
			configValue:  3.3,
			defaultValue: 1.1,
			expected:     3.3,
			description:  "Config should override default when flag not set",
		},
		{
			name:         "default_when_nothing_set",
			flagValue:    0,
			configValue:  0,
			defaultValue: 1.1,
			expected:     1.1,
			description:  "Default should be used when nothing else is set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test command
			cmd := &cobra.Command{}
			cmd.Flags().Float64("test-flag", 0, "test flag")

			// Set flag value if specified
			if tt.flagValue > 0 {
				cmd.Flags().Set("test-flag", strconv.FormatFloat(tt.flagValue, 'f', -1, 64))
			}

			result := resolveFloat64Config(cmd, "test-flag", tt.configValue, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("%s: expected %f, got %f", tt.description, tt.expected, result)
			}
		})
	}
}

func TestResolveDurationConfig(t *testing.T) {
	tests := []struct {
		name         string
		flagValue    time.Duration
		configValue  string
		defaultValue time.Duration
		expected     time.Duration
		description  string
	}{
		{
			name:         "flag_overrides_config_and_default",
			flagValue:    time.Hour,
			configValue:  "30m",
			defaultValue: time.Minute * 15,
			expected:     time.Hour,
			description:  "CLI flag should override config and default",
		},
		{
			name:         "config_overrides_default",
			flagValue:    0,
			configValue:  "30m",
			defaultValue: time.Minute * 15,
			expected:     time.Minute * 30,
			description:  "Config should override default when flag not set",
		},
		{
			name:         "default_when_nothing_set",
			flagValue:    0,
			configValue:  "",
			defaultValue: time.Minute * 15,
			expected:     time.Minute * 15,
			description:  "Default should be used when nothing else is set",
		},
		{
			name:         "invalid_config_uses_default",
			flagValue:    0,
			configValue:  "invalid-duration",
			defaultValue: time.Minute * 15,
			expected:     time.Minute * 15,
			description:  "Invalid config duration should fall back to default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test command
			cmd := &cobra.Command{}
			cmd.Flags().Duration("test-flag", 0, "test flag")

			// Set flag value if specified
			if tt.flagValue > 0 {
				cmd.Flags().Set("test-flag", tt.flagValue.String())
			}

			result := resolveDurationConfig(cmd, "test-flag", tt.configValue, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("%s: expected %v, got %v", tt.description, tt.expected, result)
			}
		})
	}
}

func TestLoadConfigMultipleLocations(t *testing.T) {
	// Create temporary directories for test
	tempDir := t.TempDir()

	// Mock the home directory
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", oldHome)

	// Test 1: Config in XDG_CONFIG_HOME location
	xdgDir := filepath.Join(tempDir, "xdg-config", "alcove")
	os.MkdirAll(xdgDir, 0755)

	configContent := `server: https://xdg.example.com
provider: anthropic`

	os.WriteFile(filepath.Join(xdgDir, "config.yaml"), []byte(configContent), 0600)

	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "xdg-config"))
	defer os.Setenv("XDG_CONFIG_HOME", oldXDG)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("Expected no error loading XDG config, got: %v", err)
	}
	if cfg.Server != "https://xdg.example.com" {
		t.Errorf("Expected server 'https://xdg.example.com', got '%s'", cfg.Server)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("Expected provider 'anthropic', got '%s'", cfg.Provider)
	}

	// Clean up XDG config
	os.Remove(filepath.Join(xdgDir, "config.yaml"))
	os.Setenv("XDG_CONFIG_HOME", oldXDG)

	// Test 2: Config in standard ~/.config/alcove location
	standardDir := filepath.Join(tempDir, ".config", "alcove")
	os.MkdirAll(standardDir, 0755)

	configContent2 := `server: https://standard.example.com
model: claude-sonnet-4`

	os.WriteFile(filepath.Join(standardDir, "config.yaml"), []byte(configContent2), 0600)

	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("Expected no error loading standard config, got: %v", err)
	}
	if cfg.Server != "https://standard.example.com" {
		t.Errorf("Expected server 'https://standard.example.com', got '%s'", cfg.Server)
	}
	if cfg.Model != "claude-sonnet-4" {
		t.Errorf("Expected model 'claude-sonnet-4', got '%s'", cfg.Model)
	}

	// Clean up standard config
	os.Remove(filepath.Join(standardDir, "config.yaml"))

	// Test 3: Config in convenience ~/.alcove.yaml location
	configContent3 := `server: https://convenience.example.com
budget: 10.50`

	os.WriteFile(filepath.Join(tempDir, ".alcove.yaml"), []byte(configContent3), 0600)

	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("Expected no error loading convenience config, got: %v", err)
	}
	if cfg.Server != "https://convenience.example.com" {
		t.Errorf("Expected server 'https://convenience.example.com', got '%s'", cfg.Server)
	}
	if cfg.Budget != 10.50 {
		t.Errorf("Expected budget 10.50, got %f", cfg.Budget)
	}

	// Clean up convenience config
	os.Remove(filepath.Join(tempDir, ".alcove.yaml"))

	// Test 4: No config file found
	_, err = loadConfig()
	if err == nil {
		t.Error("Expected error when no config file found")
	}
	if !strings.Contains(err.Error(), "no config file found") {
		t.Errorf("Expected 'no config file found' error, got: %v", err)
	}
}