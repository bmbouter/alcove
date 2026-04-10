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

// Package main implements the Alcove CLI, a client for the Alcove Bridge API.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// CLIConfig holds the user-level CLI configuration.
type CLIConfig struct {
	Server   string  `yaml:"server"`
	Provider string  `yaml:"provider,omitempty"`
	Model    string  `yaml:"model,omitempty"`
	Budget   float64 `yaml:"budget,omitempty"`
	Timeout  string  `yaml:"timeout,omitempty"` // duration string like "30m"
	Output   string  `yaml:"output,omitempty"`
	Repo     string  `yaml:"repo,omitempty"`
}

func main() {
	root := &cobra.Command{
		Use:           "alcove",
		Short:         "Alcove — sandboxed AI coding agents",
		Long:          "Alcove CLI for dispatching and managing AI coding tasks via the Bridge API.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().String("server", "", "Bridge server URL (overrides config and ALCOVE_SERVER)")
	root.PersistentFlags().String("output", "", "Output format: json or table (default: table)")
	root.PersistentFlags().StringP("username", "u", "", "Username for Basic Auth (overrides ALCOVE_USERNAME)")
	root.PersistentFlags().StringP("password", "p", "", "Password for Basic Auth (overrides ALCOVE_PASSWORD)")

	root.AddCommand(
		newRunCmd(),
		newListCmd(),
		newLogsCmd(),
		newStatusCmd(),
		newCancelCmd(),
		newLoginCmd(),
		newConfigCmd(),
		newVersionCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// resolveServer determines the Bridge URL from flag, env, or config file.
func resolveServer(cmd *cobra.Command) (string, error) {
	// 1. Flag
	if s, _ := cmd.Flags().GetString("server"); s != "" {
		return strings.TrimRight(s, "/"), nil
	}
	// 2. Environment variable
	if s := os.Getenv("ALCOVE_SERVER"); s != "" {
		return strings.TrimRight(s, "/"), nil
	}
	// 3. Config file
	cfg, err := loadConfig()
	if err == nil && cfg.Server != "" {
		return strings.TrimRight(cfg.Server, "/"), nil
	}
	return "", fmt.Errorf("no Bridge server configured; use --server, ALCOVE_SERVER, or 'alcove login'")
}

// resolveBasicAuth determines username/password from flags or environment variables.
// Returns empty strings if not configured.
func resolveBasicAuth(cmd *cobra.Command) (string, string) {
	// 1. Flags
	username, _ := cmd.Flags().GetString("username")
	password, _ := cmd.Flags().GetString("password")
	if username != "" {
		return username, password
	}

	// 2. Environment variables
	username = os.Getenv("ALCOVE_USERNAME")
	password = os.Getenv("ALCOVE_PASSWORD")
	return username, password
}

func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "alcove")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "alcove")
}

func loadConfig() (*CLIConfig, error) {
	// Check multiple config locations in priority order
	configPaths := []string{
		filepath.Join(configDir(), "config.yaml"),  // Current XDG standard
		filepath.Join(os.Getenv("HOME"), ".alcove.yaml"), // Convenience location
	}

	// Add XDG_CONFIG_HOME location if set
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && xdg != os.Getenv("HOME")+"/.config" {
		xdgPath := filepath.Join(xdg, "alcove", "config.yaml")
		configPaths = append([]string{xdgPath}, configPaths...)
	}

	for _, path := range configPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // Try next location
		}
		var cfg CLIConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing config file %s: %w", path, err)
		}
		return &cfg, nil
	}

	return nil, fmt.Errorf("no config file found in any of: %v", configPaths)
}

func loadToken() (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir(), "credentials"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// resolveStringConfig resolves a string configuration value with proper precedence:
// CLI flag → Environment variable → Config file → Default value
func resolveStringConfig(cmd *cobra.Command, flagName, envVar, configValue, defaultValue string) string {
	// 1. CLI flag
	if flagValue, _ := cmd.Flags().GetString(flagName); flagValue != "" {
		return flagValue
	}
	// 2. Environment variable
	if envVar != "" {
		if envValue := os.Getenv(envVar); envValue != "" {
			return envValue
		}
	}
	// 3. Config file
	if configValue != "" {
		return configValue
	}
	// 4. Default value
	return defaultValue
}

// resolveFloat64Config resolves a float64 configuration value with proper precedence:
// CLI flag → Config file → Default value
func resolveFloat64Config(cmd *cobra.Command, flagName string, configValue float64, defaultValue float64) float64 {
	// 1. CLI flag
	if flagValue, _ := cmd.Flags().GetFloat64(flagName); flagValue > 0 {
		return flagValue
	}
	// 2. Config file
	if configValue > 0 {
		return configValue
	}
	// 3. Default value
	return defaultValue
}

// resolveDurationConfig resolves a duration configuration value with proper precedence:
// CLI flag → Config file → Default value
func resolveDurationConfig(cmd *cobra.Command, flagName string, configValue string, defaultValue time.Duration) time.Duration {
	// 1. CLI flag
	if flagValue, _ := cmd.Flags().GetDuration(flagName); flagValue > 0 {
		return flagValue
	}
	// 2. Config file
	if configValue != "" {
		if duration, err := time.ParseDuration(configValue); err == nil && duration > 0 {
			return duration
		}
	}
	// 3. Default value
	return defaultValue
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// apiRequest performs an authenticated HTTP request to the Bridge API.
func apiRequest(cmd *cobra.Command, method, path string, body interface{}) (*http.Response, error) {
	server, err := resolveServer(cmd)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, server+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Try Basic Auth first
	username, password := resolveBasicAuth(cmd)
	if username != "" {
		// Use Basic Auth
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Authorization", "Basic "+auth)
	} else {
		// Fall back to Bearer token
		token, err := loadToken()
		if err == nil && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	return newHTTPClient().Do(req)
}

func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func isJSONOutput(cmd *cobra.Command) bool {
	// Load config to get defaults
	cfg, _ := loadConfig() // Ignore error, use empty config if not available
	if cfg == nil {
		cfg = &CLIConfig{} // Use empty config if loading failed
	}

	// Resolve output format with precedence: CLI flag → Config file → Default (table)
	output := resolveStringConfig(cmd, "output", "", cfg.Output, "table")
	return strings.EqualFold(output, "json")
}

// ---------- run ----------

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "Submit a task to the Bridge",
		Long:  "Dispatch a coding task. By default returns the session ID immediately. Use --watch for live streaming.",
		Args:  cobra.ExactArgs(1),
		RunE:  runRun,
	}
	cmd.Flags().String("repo", "", "Target repository (e.g., org/repo)")
	cmd.Flags().String("provider", "", "LLM provider name")
	cmd.Flags().String("model", "", "Model override (e.g., claude-sonnet-4-20250514)")
	cmd.Flags().Float64("budget", 0, "Budget limit in USD (e.g., 5.00)")
	cmd.Flags().Duration("timeout", 0, "Task timeout (e.g., 30m, 1h)")
	cmd.Flags().Bool("watch", false, "Stream transcript via SSE after dispatch")
	cmd.Flags().Bool("debug", false, "Keep containers after exit for log inspection")
	return cmd
}

type runRequest struct {
	Prompt   string  `json:"prompt"`
	Repo     string  `json:"repo,omitempty"`
	Provider string  `json:"provider,omitempty"`
	Timeout  int     `json:"timeout,omitempty"`
	Model    string  `json:"model,omitempty"`
	Budget   float64 `json:"budget_usd,omitempty"`
	Debug    bool    `json:"debug,omitempty"`
}

type runResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func runRun(cmd *cobra.Command, args []string) error {
	// Load config to get defaults
	cfg, _ := loadConfig() // Ignore error, use empty config if not available
	if cfg == nil {
		cfg = &CLIConfig{} // Use empty config if loading failed
	}

	reqBody := runRequest{Prompt: args[0]}

	// Apply config resolution for each field
	reqBody.Repo = resolveStringConfig(cmd, "repo", "", cfg.Repo, "")
	reqBody.Provider = resolveStringConfig(cmd, "provider", "", cfg.Provider, "")
	reqBody.Model = resolveStringConfig(cmd, "model", "", cfg.Model, "")

	// Budget resolution
	budget := resolveFloat64Config(cmd, "budget", cfg.Budget, 0)
	if budget > 0 {
		reqBody.Budget = budget
	}

	// Timeout resolution
	timeout := resolveDurationConfig(cmd, "timeout", cfg.Timeout, 0)
	if timeout > 0 {
		reqBody.Timeout = int(timeout.Seconds())
	}

	reqBody.Debug, _ = cmd.Flags().GetBool("debug")

	resp, err := apiRequest(cmd, http.MethodPost, "/api/v1/tasks", reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result runResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Session dispatched: %s\n", result.ID)

	watch, _ := cmd.Flags().GetBool("watch")
	if watch {
		return streamSSE(cmd, result.ID, "/api/v1/sessions/"+result.ID+"/transcript")
	}

	fmt.Println(result.ID)
	return nil
}

// ---------- list ----------

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sessions",
		RunE:  runList,
	}
	cmd.Flags().String("status", "", "Filter by status (running, completed, error, cancelled, timeout)")
	cmd.Flags().String("repo", "", "Filter by repository")
	cmd.Flags().Duration("since", 0, "Show sessions from the last duration (e.g., 24h, 7d)")
	return cmd
}

type listResponse struct {
	Sessions []sessionSummary `json:"sessions"`
}

type sessionSummary struct {
	ID        string `json:"id"`
	Prompt    string `json:"prompt"`
	Repo      string `json:"repo,omitempty"`
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	Duration  string `json:"duration,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}

func runList(cmd *cobra.Command, _ []string) error {
	// Load config to get defaults
	cfg, _ := loadConfig() // Ignore error, use empty config if not available
	if cfg == nil {
		cfg = &CLIConfig{} // Use empty config if loading failed
	}

	var params []string
	if s, _ := cmd.Flags().GetString("status"); s != "" {
		params = append(params, "status="+s)
	}

	// Use config default for repo if not specified
	repo := resolveStringConfig(cmd, "repo", "", cfg.Repo, "")
	if repo != "" {
		params = append(params, "repo="+repo)
	}

	if d, _ := cmd.Flags().GetDuration("since"); d > 0 {
		since := time.Now().Add(-d).Format(time.RFC3339)
		params = append(params, "since="+since)
	}

	path := "/api/v1/sessions"
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
	}

	resp, err := apiRequest(cmd, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result listResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	if len(result.Sessions) == 0 {
		fmt.Fprintln(os.Stderr, "No sessions found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tREPO\tPROVIDER\tDURATION\tPROMPT")
	for _, s := range result.Sessions {
		prompt := s.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Status, s.Repo, s.Provider, s.Duration, prompt)
	}
	return w.Flush()
}

// ---------- logs ----------

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs [session-id]",
		Short: "Fetch or stream session logs",
		Args:  cobra.ExactArgs(1),
		RunE:  runLogs,
	}
	cmd.Flags().BoolP("follow", "f", false, "Stream logs via SSE")
	cmd.Flags().Bool("proxy", false, "Show Gate proxy log instead of transcript")
	cmd.Flags().Bool("denied", false, "Show only denied proxy requests")
	return cmd
}

func runLogs(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	follow, _ := cmd.Flags().GetBool("follow")
	proxy, _ := cmd.Flags().GetBool("proxy")
	denied, _ := cmd.Flags().GetBool("denied")

	if follow {
		path := fmt.Sprintf("/api/v1/sessions/%s/transcript", sessionID)
		if proxy {
			path = fmt.Sprintf("/api/v1/sessions/%s/proxy-log", sessionID)
		}
		return streamSSE(cmd, sessionID, path)
	}

	path := fmt.Sprintf("/api/v1/sessions/%s/transcript", sessionID)
	if proxy {
		path = fmt.Sprintf("/api/v1/sessions/%s/proxy-log", sessionID)
	}
	if denied {
		path += "?decision=deny"
	}

	resp, err := apiRequest(cmd, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

// ---------- status ----------

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [session-id]",
		Short: "Show session status",
		Args:  cobra.ExactArgs(1),
		RunE:  runStatus,
	}
}

type statusResponse struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Prompt     string `json:"prompt"`
	Repo       string `json:"repo,omitempty"`
	Provider   string `json:"provider"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Duration   string `json:"duration,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Artifacts  []struct {
		Type string `json:"type"`
		URL  string `json:"url,omitempty"`
		Ref  string `json:"ref,omitempty"`
	} `json:"artifacts,omitempty"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/sessions/"+sessionID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stdout, "Session:    %s\n", result.ID)
	fmt.Fprintf(os.Stdout, "Status:     %s\n", result.Status)
	fmt.Fprintf(os.Stdout, "Provider:   %s\n", result.Provider)
	if result.Repo != "" {
		fmt.Fprintf(os.Stdout, "Repository: %s\n", result.Repo)
	}
	fmt.Fprintf(os.Stdout, "Started:    %s\n", result.StartedAt)
	if result.FinishedAt != "" {
		fmt.Fprintf(os.Stdout, "Finished:   %s\n", result.FinishedAt)
	}
	if result.Duration != "" {
		fmt.Fprintf(os.Stdout, "Duration:   %s\n", result.Duration)
	}
	if result.ExitCode != nil {
		fmt.Fprintf(os.Stdout, "Exit Code:  %d\n", *result.ExitCode)
	}
	fmt.Fprintf(os.Stdout, "Prompt:     %s\n", result.Prompt)

	if len(result.Artifacts) > 0 {
		fmt.Fprintln(os.Stdout, "\nArtifacts:")
		for _, a := range result.Artifacts {
			if a.URL != "" {
				fmt.Fprintf(os.Stdout, "  [%s] %s\n", a.Type, a.URL)
			} else {
				fmt.Fprintf(os.Stdout, "  [%s] %s\n", a.Type, a.Ref)
			}
		}
	}

	return nil
}

// ---------- cancel ----------

func newCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel [session-id]",
		Short: "Cancel a running session",
		Args:  cobra.ExactArgs(1),
		RunE:  runCancel,
	}
}

func runCancel(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	resp, err := apiRequest(cmd, http.MethodDelete, "/api/v1/sessions/"+sessionID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	if isJSONOutput(cmd) {
		return outputJSON(map[string]string{"session_id": sessionID, "status": "cancelling"})
	}

	fmt.Fprintf(os.Stderr, "Cancel requested for session %s\n", sessionID)
	return nil
}

// ---------- login ----------

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login [bridge-url]",
		Short: "Authenticate to a Bridge instance",
		Long:  "Store the Bridge URL and authenticate. Credentials are saved to ~/.config/alcove/credentials.",
		Args:  cobra.ExactArgs(1),
		RunE:  runLogin,
	}
}

func runLogin(cmd *cobra.Command, args []string) error {
	// Check for conflicting auth methods
	username, password := resolveBasicAuth(cmd)
	if username != "" || password != "" {
		return fmt.Errorf("cannot use --username/--password flags or ALCOVE_USERNAME/ALCOVE_PASSWORD environment variables with the login command; use either Basic Auth or token-based auth, not both")
	}

	bridgeURL := strings.TrimRight(args[0], "/")

	// Prompt for username and password
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, "Username: ")
	username, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading username: %w", err)
	}
	username = strings.TrimSpace(username)
	fmt.Fprint(os.Stderr, "Password: ")
	password, err = reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading password: %w", err)
	}
	password = strings.TrimSpace(password)

	// Authenticate
	loginBody := map[string]string{"username": username, "password": password}
	data, err := json.Marshal(loginBody)
	if err != nil {
		return err
	}

	resp, err := http.Post(bridgeURL+"/api/v1/auth/login", "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("connecting to bridge: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("authentication failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("decoding token: %w", err)
	}

	// Save config and credentials
	dir := configDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	cfg := CLIConfig{Server: bridgeURL}
	cfgData, _ := yaml.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), cfgData, 0600); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte(tokenResp.Token), 0600); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Logged in to %s\n", bridgeURL)
	fmt.Fprintf(os.Stderr, "Config saved to %s\n", filepath.Join(dir, "config.yaml"))
	return nil
}

// ---------- config ----------

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "validate",
			Short: "Validate the current configuration",
			RunE:  runConfigValidate,
		},
		&cobra.Command{
			Use:   "init",
			Short: "Create example config file with all options documented",
			RunE:  runConfigInit,
		},
		&cobra.Command{
			Use:   "show",
			Short: "Display current effective configuration with precedence",
			RunE:  runConfigShow,
		},
	)
	return cmd
}

func runConfigValidate(cmd *cobra.Command, _ []string) error {
	dir := configDir()
	credsPath := filepath.Join(dir, "credentials")

	var issues []string

	cfg, err := loadConfig()
	if err != nil {
		issues = append(issues, fmt.Sprintf("config: cannot read config file: %v", err))
	} else {
		if cfg.Server == "" {
			issues = append(issues, "config: 'server' is not set")
		} else {
			fmt.Fprintf(os.Stderr, "config: server = %s\n", cfg.Server)
		}

		// Validate new config fields
		if cfg.Output != "" && !strings.EqualFold(cfg.Output, "json") && !strings.EqualFold(cfg.Output, "table") {
			issues = append(issues, fmt.Sprintf("config: invalid output format '%s', must be 'json' or 'table'", cfg.Output))
		}

		if cfg.Timeout != "" {
			if _, err := time.ParseDuration(cfg.Timeout); err != nil {
				issues = append(issues, fmt.Sprintf("config: invalid timeout duration '%s': %v", cfg.Timeout, err))
			}
		}

		if cfg.Budget < 0 {
			issues = append(issues, fmt.Sprintf("config: budget must be positive, got %f", cfg.Budget))
		}
	}

	token, err := loadToken()
	if err != nil {
		issues = append(issues, fmt.Sprintf("credentials: cannot read %s: %v", credsPath, err))
	} else if token == "" {
		issues = append(issues, "credentials: token is empty")
	} else {
		fmt.Fprintf(os.Stderr, "credentials: token present (%d chars)\n", len(token))
	}

	if envServer := os.Getenv("ALCOVE_SERVER"); envServer != "" {
		fmt.Fprintf(os.Stderr, "env: ALCOVE_SERVER = %s (overrides config file)\n", envServer)
	}

	if len(issues) > 0 {
		fmt.Fprintln(os.Stderr, "\nIssues:")
		for _, iss := range issues {
			fmt.Fprintf(os.Stderr, "  - %s\n", iss)
		}
		return fmt.Errorf("configuration has %d issue(s)", len(issues))
	}

	fmt.Fprintln(os.Stderr, "\nConfiguration is valid.")
	return nil
}

func runConfigInit(cmd *cobra.Command, _ []string) error {
	dir := configDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Check if config already exists
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file already exists at %s, remove it first to reinitialize", configPath)
	}

	// Create config directory if it doesn't exist
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Example config content with documentation
	configContent := `# Alcove CLI Configuration
# This file contains default values for common CLI options.
# Command-line flags and environment variables take precedence over these values.

# Server URL - required (can also be set via ALCOVE_SERVER env var or --server flag)
server: ""

# Default provider for tasks (optional)
# Example: anthropic, openai
#provider: ""

# Default model override (optional)
# Example: claude-sonnet-4-20250514, gpt-4
#model: ""

# Default budget limit in USD (optional)
# Example: 5.00
#budget: 0

# Default timeout for tasks (optional, accepts Go duration syntax)
# Examples: "30m", "1h", "90s"
#timeout: ""

# Default output format: "table" or "json" (optional)
# Default is "table" if not specified
#output: "table"

# Default repository for tasks (optional)
# Example: myorg/myproject
#repo: ""
`

	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Example config file created at: %s\n", configPath)
	fmt.Fprintf(os.Stderr, "\nEdit the file to set your default values. Uncomment and modify the lines you want to use.\n")
	fmt.Fprintf(os.Stderr, "You can validate your configuration with: alcove config validate\n")

	return nil
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	fmt.Println("Current Effective Configuration:")
	fmt.Println("===============================")

	// Load config file
	cfg, configErr := loadConfig()
	if cfg == nil {
		cfg = &CLIConfig{}
	}

	// Show server resolution
	fmt.Printf("\nServer Configuration:\n")
	server, serverErr := resolveServer(cmd)
	if serverErr != nil {
		fmt.Printf("  ERROR: %v\n", serverErr)
	} else {
		fmt.Printf("  Effective: %s\n", server)
	}

	// Show sources for server
	if flagServer, _ := cmd.Flags().GetString("server"); flagServer != "" {
		fmt.Printf("  Source: CLI flag (--server)\n")
	} else if envServer := os.Getenv("ALCOVE_SERVER"); envServer != "" {
		fmt.Printf("  Source: Environment variable (ALCOVE_SERVER)\n")
	} else if cfg.Server != "" {
		fmt.Printf("  Source: Config file\n")
	} else {
		fmt.Printf("  Source: Not configured\n")
	}

	// Show other configuration values
	fmt.Printf("\nDefault Values:\n")

	provider := resolveStringConfig(cmd, "provider", "", cfg.Provider, "")
	if provider != "" {
		fmt.Printf("  Provider: %s (from config file)\n", provider)
	} else {
		fmt.Printf("  Provider: <not set>\n")
	}

	model := resolveStringConfig(cmd, "model", "", cfg.Model, "")
	if model != "" {
		fmt.Printf("  Model: %s (from config file)\n", model)
	} else {
		fmt.Printf("  Model: <not set>\n")
	}

	budget := resolveFloat64Config(cmd, "budget", cfg.Budget, 0)
	if budget > 0 {
		fmt.Printf("  Budget: $%.2f (from config file)\n", budget)
	} else {
		fmt.Printf("  Budget: <not set>\n")
	}

	timeout := resolveDurationConfig(cmd, "timeout", cfg.Timeout, 0)
	if timeout > 0 {
		fmt.Printf("  Timeout: %v (from config file)\n", timeout)
	} else {
		fmt.Printf("  Timeout: <not set>\n")
	}

	output := resolveStringConfig(cmd, "output", "", cfg.Output, "table")
	if cfg.Output != "" {
		fmt.Printf("  Output: %s (from config file)\n", output)
	} else {
		fmt.Printf("  Output: %s (default)\n", output)
	}

	repo := resolveStringConfig(cmd, "repo", "", cfg.Repo, "")
	if repo != "" {
		fmt.Printf("  Repo: %s (from config file)\n", repo)
	} else {
		fmt.Printf("  Repo: <not set>\n")
	}

	// Show config file location info
	fmt.Printf("\nConfig File Information:\n")
	if configErr != nil {
		fmt.Printf("  Status: No valid config file found\n")
		fmt.Printf("  Error: %v\n", configErr)
		fmt.Printf("  Run 'alcove config init' to create an example config file\n")
	} else {
		// Try to find which config file was used
		configPaths := []string{
			filepath.Join(configDir(), "config.yaml"),
			filepath.Join(os.Getenv("HOME"), ".alcove.yaml"),
		}
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && xdg != os.Getenv("HOME")+"/.config" {
			xdgPath := filepath.Join(xdg, "alcove", "config.yaml")
			configPaths = append([]string{xdgPath}, configPaths...)
		}

		for _, path := range configPaths {
			if _, err := os.Stat(path); err == nil {
				fmt.Printf("  Location: %s\n", path)
				break
			}
		}
		fmt.Printf("  Status: Valid\n")
	}

	// Show authentication status
	fmt.Printf("\nAuthentication:\n")
	token, tokenErr := loadToken()
	if tokenErr != nil {
		fmt.Printf("  Token: Not found (%v)\n", tokenErr)
		fmt.Printf("  Use 'alcove login <server-url>' to authenticate\n")
	} else if token != "" {
		fmt.Printf("  Token: Present (%d characters)\n", len(token))
	} else {
		fmt.Printf("  Token: Empty\n")
	}

	// Show environment variables that could affect configuration
	fmt.Printf("\nEnvironment Variables:\n")
	for _, env := range []string{"ALCOVE_SERVER", "ALCOVE_USERNAME", "ALCOVE_PASSWORD", "XDG_CONFIG_HOME"} {
		if value := os.Getenv(env); value != "" {
			if strings.Contains(env, "PASSWORD") || strings.Contains(env, "TOKEN") {
				fmt.Printf("  %s: <set>\n", env)
			} else {
				fmt.Printf("  %s: %s\n", env, value)
			}
		}
	}

	return nil
}

// ---------- version ----------

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print client version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if isJSONOutput(cmd) {
				return outputJSON(map[string]string{"version": Version})
			}
			fmt.Printf("alcove version %s\n", Version)
			return nil
		},
	}
}

// ---------- SSE streaming ----------

func streamSSE(cmd *cobra.Command, sessionID, path string) error {
	server, err := resolveServer(cmd)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	// Try Basic Auth first
	username, password := resolveBasicAuth(cmd)
	if username != "" {
		// Use Basic Auth
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Authorization", "Basic "+auth)
	} else {
		// Fall back to Bearer token or query param for SSE compatibility
		token, err := loadToken()
		if err == nil && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	client := &http.Client{Timeout: 0} // no timeout for SSE
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to SSE stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer for potentially large SSE messages.
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// SSE protocol: lines starting with "data:" contain the payload.
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				fmt.Fprintln(os.Stderr, "\nStream ended.")
				return nil
			}
			fmt.Println(data)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading SSE stream: %w", err)
	}

	return nil
}
