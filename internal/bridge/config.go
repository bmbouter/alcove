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

// Package bridge implements the Alcove Bridge controller — the central
// coordinator that provides the REST API and dispatches tasks to Skiff pods.
package bridge

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/bmbouter/alcove/internal"
	"gopkg.in/yaml.v3"
)

// Config holds all Bridge configuration.
type Config struct {
	HailURL       string
	LedgerURL     string
	Port          string
	RuntimeType   string // "podman" or "kubernetes"
	DebugMode     bool
	DatabaseEncryptionKey string
	AuthBackend           string
	Version               string // set programmatically, not from config file

	LLMCredentials map[string]string // provider name -> API key

	// System LLM configuration (for AI-powered Bridge features).
	SystemLLM SystemLLMConfig

	// RHIdentityAdmins is a list of email addresses to bootstrap as admins
	// when using the rh-identity auth backend.
	RHIdentityAdmins []string
}

// SystemLLMConfig holds configuration for the Bridge system LLM.
type SystemLLMConfig struct {
	Provider           string `yaml:"provider"`
	Model              string `yaml:"model"`
	APIKey             string `yaml:"api_key"`
	OAuthToken         string `yaml:"oauth_token"`
	ServiceAccountJSON string `yaml:"service_account_json"`
	ProjectID          string `yaml:"project_id"`
	Region             string `yaml:"region"`
}

// LoadConfig reads configuration from the config file and environment variables.
// Environment variables always take precedence over the config file.
func LoadConfig() (*Config, error) {
	cfg := &Config{}

	// Load config file first (provides defaults).
	cfg.loadConfigFile()

	// Environment variables override config file values.
	if v := os.Getenv("HAIL_URL"); v != "" {
		cfg.HailURL = v
	}
	if cfg.HailURL == "" {
		cfg.HailURL = "nats://localhost:4222"
	}
	if v := os.Getenv("LEDGER_DATABASE_URL"); v != "" {
		cfg.LedgerURL = v
	}
	if cfg.LedgerURL == "" {
		cfg.LedgerURL = "postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable"
	}
	if v := os.Getenv("BRIDGE_PORT"); v != "" {
		cfg.Port = v
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if v := os.Getenv("RUNTIME"); v != "" {
		cfg.RuntimeType = v
	}
	if cfg.RuntimeType == "" {
		cfg.RuntimeType = "podman"
	}
	if v := os.Getenv("AUTH_BACKEND"); v != "" {
		cfg.AuthBackend = v
	}
	if cfg.AuthBackend == "" {
		cfg.AuthBackend = "memory"
	}

	if v := os.Getenv("ALCOVE_DATABASE_ENCRYPTION_KEY"); v != "" {
		cfg.DatabaseEncryptionKey = v
	}
	if cfg.DatabaseEncryptionKey == "" {
		log.Fatalf(`FATAL: ALCOVE_DATABASE_ENCRYPTION_KEY is not set. This key encrypts stored credentials.

For local development:
  cp alcove.yaml.example alcove.yaml
  # Edit database_encryption_key in alcove.yaml

For Kubernetes:
  Set ALCOVE_DATABASE_ENCRYPTION_KEY via a Kubernetes Secret.`)
	}

	// Load debug mode from environment.
	if os.Getenv("ALCOVE_DEBUG") != "" {
		cfg.DebugMode = true
	}

	// System LLM configuration from environment.
	if v := os.Getenv("BRIDGE_LLM_PROVIDER"); v != "" {
		cfg.SystemLLM.Provider = v
	}
	if v := os.Getenv("BRIDGE_LLM_API_KEY"); v != "" {
		cfg.SystemLLM.APIKey = v
	}
	if v := os.Getenv("BRIDGE_LLM_MODEL"); v != "" {
		cfg.SystemLLM.Model = v
	}
	if v := os.Getenv("BRIDGE_LLM_OAUTH_TOKEN"); v != "" {
		cfg.SystemLLM.OAuthToken = v
	}
	if v := os.Getenv("BRIDGE_LLM_SERVICE_ACCOUNT_JSON"); v != "" {
		cfg.SystemLLM.ServiceAccountJSON = v
	}
	if v := os.Getenv("BRIDGE_LLM_PROJECT"); v != "" {
		cfg.SystemLLM.ProjectID = v
	}
	if v := os.Getenv("BRIDGE_LLM_REGION"); v != "" {
		cfg.SystemLLM.Region = v
	}

	// Load LLM credentials from environment.
	cfg.LLMCredentials = make(map[string]string)
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.LLMCredentials["anthropic"] = v
	}
	if v := os.Getenv("VERTEX_API_KEY"); v != "" {
		cfg.LLMCredentials["vertex"] = v
	}

	// RH Identity admin bootstrap from environment (comma-separated emails).
	if v := os.Getenv("RH_IDENTITY_ADMINS"); v != "" {
		cfg.RHIdentityAdmins = splitAndTrim(v)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// splitAndTrim splits a comma-separated string and trims whitespace from each element.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// loadConfigFile reads configuration from a config file.
// It searches for the config file in this order:
//  1. Path specified by ALCOVE_CONFIG_FILE env var
//  2. ./alcove.yaml
//  3. /etc/alcove/alcove.yaml
func (c *Config) loadConfigFile() {
	paths := []string{}
	if v := os.Getenv("ALCOVE_CONFIG_FILE"); v != "" {
		paths = append(paths, v)
	}
	paths = append(paths, "./alcove.yaml", "/etc/alcove/alcove.yaml")

	for _, path := range paths {
		if err := c.parseConfigFile(path); err == nil {
			log.Printf("loaded config from %s", path)
			return
		}
	}
}

// configFile represents the YAML configuration file structure.
type configFile struct {
	DatabaseEncryptionKey string   `yaml:"database_encryption_key"`
	DatabaseURL           string   `yaml:"database_url"`
	NatsURL               string   `yaml:"nats_url"`
	AuthBackend           string   `yaml:"auth_backend"`
	Port                  string   `yaml:"port"`
	Runtime               string   `yaml:"runtime"`
	RHIdentityAdmins      []string         `yaml:"rh_identity_admins"`
	SystemLLM             *SystemLLMConfig `yaml:"system_llm"`
}

// parseConfigFile reads and parses a YAML config file.
func (c *Config) parseConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var cf configFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return fmt.Errorf("parsing config file %s: %w", path, err)
	}

	if cf.DatabaseEncryptionKey != "" {
		c.DatabaseEncryptionKey = cf.DatabaseEncryptionKey
	}
	if cf.DatabaseURL != "" {
		c.LedgerURL = cf.DatabaseURL
	}
	if cf.NatsURL != "" {
		c.HailURL = cf.NatsURL
	}
	if cf.AuthBackend != "" {
		c.AuthBackend = cf.AuthBackend
	}
	if cf.Port != "" {
		c.Port = cf.Port
	}
	if cf.Runtime != "" {
		c.RuntimeType = cf.Runtime
	}
	if len(cf.RHIdentityAdmins) > 0 {
		c.RHIdentityAdmins = cf.RHIdentityAdmins
	}
	if cf.SystemLLM != nil {
		c.SystemLLM = *cf.SystemLLM
	}
	return nil
}

func (c *Config) validate() error {
	if c.RuntimeType != "podman" && c.RuntimeType != "kubernetes" {
		return fmt.Errorf("invalid runtime %q: must be \"podman\" or \"kubernetes\"", c.RuntimeType)
	}
	if c.AuthBackend != "memory" && c.AuthBackend != "postgres" && c.AuthBackend != "rh-identity" {
		return fmt.Errorf("invalid AUTH_BACKEND %q: must be \"memory\", \"postgres\", or \"rh-identity\"", c.AuthBackend)
	}
	return nil
}

func defaultProviders() []internal.Provider {
	providers := []internal.Provider{}

	if os.Getenv("VERTEX_PROJECT") != "" {
		providers = append(providers, internal.Provider{
			Name:  "vertex",
			Type:  "google-vertex",
			Model: envOrDefault("VERTEX_MODEL", "claude-sonnet-4-20250514"),
		})
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		providers = append(providers, internal.Provider{
			Name:  "anthropic",
			Type:  "anthropic",
			Model: envOrDefault("ANTHROPIC_MODEL", "claude-sonnet-4-20250514"),
		})
	}

	// Always have at least a placeholder provider so the system can start.
	if len(providers) == 0 {
		providers = append(providers, internal.Provider{
			Name:  "default",
			Type:  "anthropic",
			Model: "claude-sonnet-4-20250514",
		})
	}

	return providers
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// LLMKeyForProvider returns the API key for the given provider name,
// or an empty string if no credential is configured.
func (c *Config) LLMKeyForProvider(providerName string) string {
	if c.LLMCredentials == nil {
		return ""
	}
	return c.LLMCredentials[providerName]
}
