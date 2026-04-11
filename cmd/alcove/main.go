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
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	Server   string `yaml:"server"`
	Output   string `yaml:"output,omitempty"`    // "json" or "table"
	Username string `yaml:"username,omitempty"`  // Basic Auth username
	Password string `yaml:"password,omitempty"`  // Basic Auth password
	ProxyURL string `yaml:"proxy_url,omitempty"` // HTTP proxy
	NoProxy  string `yaml:"no_proxy,omitempty"`  // Comma-separated no-proxy hosts
	Defaults struct {
		Repo     string  `yaml:"repo,omitempty"`     // Default repository
		Provider string  `yaml:"provider,omitempty"` // Default LLM provider
		Model    string  `yaml:"model,omitempty"`    // Default model
		Timeout  string  `yaml:"timeout,omitempty"`  // Default timeout (e.g., "30m")
		Budget   float64 `yaml:"budget,omitempty"`   // Default budget in USD
	} `yaml:"defaults,omitempty"`
}

// ProxyConfig holds HTTP proxy configuration.
type ProxyConfig struct {
	ProxyURL string
	NoProxy  []string
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
	root.PersistentFlags().String("proxy-url", "", "HTTP/HTTPS proxy URL (overrides environment)")
	root.PersistentFlags().String("no-proxy", "", "Comma-separated list of hosts to exclude from proxy (overrides NO_PROXY env var)")

	root.AddCommand(
		newRunCmd(),
		newListCmd(),
		newLogsCmd(),
		newStatusCmd(),
		newCancelCmd(),
		newDeleteCmd(),
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

// resolveBasicAuth determines username/password from flags, environment variables,
// or config file. Returns empty strings if not configured.
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
	if username != "" {
		return username, password
	}

	// 3. Config file
	if cfg, err := loadConfig(); err == nil {
		username = cfg.Username
		password = cfg.Password
	}
	return username, password
}

// resolveProxyConfig determines proxy configuration from flags or environment variables.
func resolveProxyConfig(cmd *cobra.Command) (*ProxyConfig, error) {
	config := &ProxyConfig{}

	// 1. CLI flags (highest priority)
	if proxyURL, _ := cmd.Flags().GetString("proxy-url"); proxyURL != "" {
		if err := validateProxyURL(proxyURL); err != nil {
			return nil, fmt.Errorf("invalid proxy URL: %w", err)
		}
		config.ProxyURL = proxyURL
	}

	if noProxy, _ := cmd.Flags().GetString("no-proxy"); noProxy != "" {
		config.NoProxy = parseNoProxy(noProxy)
	}

	// 2. Environment variables (if flags not set)
	if config.ProxyURL == "" {
		// Try HTTPS_PROXY first, then HTTP_PROXY (both case variants)
		proxyURL := os.Getenv("HTTPS_PROXY")
		if proxyURL == "" {
			proxyURL = os.Getenv("https_proxy")
		}
		if proxyURL == "" {
			proxyURL = os.Getenv("HTTP_PROXY")
		}
		if proxyURL == "" {
			proxyURL = os.Getenv("http_proxy")
		}
		if proxyURL != "" {
			if err := validateProxyURL(proxyURL); err != nil {
				return nil, fmt.Errorf("invalid proxy URL from environment: %w", err)
			}
			config.ProxyURL = proxyURL
		}
	}

	if len(config.NoProxy) == 0 {
		noProxy := os.Getenv("NO_PROXY")
		if noProxy == "" {
			noProxy = os.Getenv("no_proxy")
		}
		if noProxy != "" {
			config.NoProxy = parseNoProxy(noProxy)
		}
	}

	// 3. Config file
	if config.ProxyURL == "" {
		if cfg, err := loadConfig(); err == nil && cfg.ProxyURL != "" {
			if err := validateProxyURL(cfg.ProxyURL); err == nil {
				config.ProxyURL = cfg.ProxyURL
			}
			if cfg.NoProxy != "" && len(config.NoProxy) == 0 {
				config.NoProxy = parseNoProxy(cfg.NoProxy)
			}
		}
	}

	return config, nil
}

// validateProxyURL validates the proxy URL format.
func validateProxyURL(proxyURL string) error {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("parsing proxy URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("proxy URL must use http or https scheme, got %s", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("proxy URL must have a host")
	}
	return nil
}

// parseNoProxy parses a comma-separated list of hosts to exclude from proxy.
func parseNoProxy(noProxy string) []string {
	var hosts []string
	for _, host := range strings.Split(noProxy, ",") {
		host = strings.TrimSpace(host)
		if host != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// shouldUseProxy determines whether a target URL should use the proxy.
func shouldUseProxy(targetURL string, noProxy []string) bool {
	if len(noProxy) == 0 {
		return true
	}

	u, err := url.Parse(targetURL)
	if err != nil {
		return true // Default to proxy if we can't parse
	}

	host := u.Hostname()
	port := u.Port()
	hostPort := u.Host

	for _, pattern := range noProxy {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		// Exact match for host or host:port
		if pattern == host || pattern == hostPort {
			return false
		}

		// Domain suffix match (e.g., ".example.com" matches "api.example.com")
		if strings.HasPrefix(pattern, ".") && strings.HasSuffix(host, pattern) {
			return false
		}

		// Wildcard domain match (e.g., "*.example.com" matches "api.example.com")
		if strings.HasPrefix(pattern, "*.") {
			domain := strings.TrimPrefix(pattern, "*")
			if strings.HasSuffix(host, domain) {
				return false
			}
		}

		// Check for IP/CIDR match
		if ip := net.ParseIP(host); ip != nil {
			if patternIP := net.ParseIP(pattern); patternIP != nil {
				if ip.Equal(patternIP) {
					return false
				}
			} else if _, cidr, err := net.ParseCIDR(pattern); err == nil {
				if cidr.Contains(ip) {
					return false
				}
			}
		}

		// Port-only pattern
		if pattern == port {
			return false
		}
	}

	return true
}

// newHTTPTransport creates an HTTP transport with proxy configuration.
func newHTTPTransport(proxyConfig *ProxyConfig) *http.Transport {
	transport := &http.Transport{}

	if proxyConfig != nil && proxyConfig.ProxyURL != "" {
		// Create proxy function that respects NO_PROXY
		proxyURL, _ := url.Parse(proxyConfig.ProxyURL)
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			if shouldUseProxy(req.URL.String(), proxyConfig.NoProxy) {
				return proxyURL, nil
			}
			return nil, nil
		}
	} else {
		// Use standard Go proxy behavior
		transport.Proxy = http.ProxyFromEnvironment
	}

	return transport
}

func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "alcove")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "alcove")
}

func loadConfig() (*CLIConfig, error) {
	data, err := os.ReadFile(filepath.Join(configDir(), "config.yaml"))
	if err != nil {
		return nil, err
	}
	var cfg CLIConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadToken() (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir(), "credentials"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func newHTTPClient(proxyConfig *ProxyConfig) *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: newHTTPTransport(proxyConfig),
	}
}

// apiRequest performs an authenticated HTTP request to the Bridge API.
func apiRequest(cmd *cobra.Command, method, path string, body interface{}) (*http.Response, error) {
	server, err := resolveServer(cmd)
	if err != nil {
		return nil, err
	}

	proxyConfig, err := resolveProxyConfig(cmd)
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

	return newHTTPClient(proxyConfig).Do(req)
}

func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func isJSONOutput(cmd *cobra.Command) bool {
	if f, _ := cmd.Flags().GetString("output"); f == "json" {
		return true
	}
	if os.Getenv("ALCOVE_OUTPUT") == "json" {
		return true
	}
	if cfg, err := loadConfig(); err == nil && cfg.Output == "json" {
		return true
	}
	return false
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
	reqBody := runRequest{Prompt: args[0]}
	reqBody.Repo, _ = cmd.Flags().GetString("repo")
	reqBody.Provider, _ = cmd.Flags().GetString("provider")
	reqBody.Model, _ = cmd.Flags().GetString("model")
	if b, _ := cmd.Flags().GetFloat64("budget"); b > 0 {
		reqBody.Budget = b
	}
	if t, _ := cmd.Flags().GetDuration("timeout"); t > 0 {
		reqBody.Timeout = int(t.Seconds())
	}
	reqBody.Debug, _ = cmd.Flags().GetBool("debug")

	// Fall back to config file defaults
	if cfg, err := loadConfig(); err == nil {
		if reqBody.Repo == "" {
			reqBody.Repo = cfg.Defaults.Repo
		}
		if reqBody.Provider == "" {
			reqBody.Provider = cfg.Defaults.Provider
		}
		if reqBody.Model == "" {
			reqBody.Model = cfg.Defaults.Model
		}
		if reqBody.Budget == 0 && cfg.Defaults.Budget > 0 {
			reqBody.Budget = cfg.Defaults.Budget
		}
		if reqBody.Timeout == 0 && cfg.Defaults.Timeout != "" {
			if d, err := time.ParseDuration(cfg.Defaults.Timeout); err == nil {
				reqBody.Timeout = int(d.Seconds())
			}
		}
	}

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
	var params []string
	if s, _ := cmd.Flags().GetString("status"); s != "" {
		params = append(params, "status="+s)
	}
	if r, _ := cmd.Flags().GetString("repo"); r != "" {
		params = append(params, "repo="+r)
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

func newDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete [session-id]",
		Short: "Delete completed/errored/timed-out sessions",
		Long: `Delete sessions in terminal states (completed, error, timeout, cancelled).

Examples:
  # Delete a specific session
  alcove delete 12345678-abcd-1234-abcd-123456789012

  # Delete all error sessions older than 7 days
  alcove delete --status error --before 7d

  # Delete all completed sessions before a specific date
  alcove delete --status completed --before 2023-01-01T00:00:00Z

  # Dry run to see what would be deleted
  alcove delete --status error --before 30d --dry-run
`,
		RunE: runDelete,
	}

	cmd.Flags().String("status", "", "Delete sessions with specific status: completed, error, timeout, cancelled")
	cmd.Flags().String("before", "", "Delete sessions finished before date/time (RFC3339) or duration (e.g., '7d', '30d')")
	cmd.Flags().Bool("dry-run", false, "Show what would be deleted without actually deleting")

	return cmd
}

func runDelete(cmd *cobra.Command, args []string) error {
	status, _ := cmd.Flags().GetString("status")
	before, _ := cmd.Flags().GetString("before")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Single session deletion
	if len(args) == 1 {
		sessionID := args[0]

		if dryRun {
			return fmt.Errorf("--dry-run is not supported for single session deletion")
		}

		// Make delete request with action=delete to distinguish from cancel
		resp, err := apiRequest(cmd, http.MethodDelete, "/api/v1/sessions/"+sessionID+"?action=delete", nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
		}

		if isJSONOutput(cmd) {
			return outputJSON(map[string]string{"session_id": sessionID, "status": "deleted"})
		}

		fmt.Fprintf(os.Stderr, "Session %s deleted\n", sessionID)
		return nil
	}

	// Bulk deletion
	if len(args) > 1 {
		return fmt.Errorf("too many arguments: either provide one session ID or use flags for bulk deletion")
	}

	// No session ID provided - use bulk deletion with filters
	if status == "" && before == "" {
		return fmt.Errorf("either provide a session ID or use --status and/or --before flags for bulk deletion")
	}

	// Validate status if provided
	if status != "" {
		validStatuses := map[string]bool{"completed": true, "error": true, "timeout": true, "cancelled": true}
		if !validStatuses[status] {
			return fmt.Errorf("invalid status: must be one of completed, error, timeout, cancelled")
		}
	}

	if dryRun {
		// Dry run: list sessions that would be deleted
		return runDeleteDryRun(cmd, status, before)
	}

	// Build request body for bulk deletion
	reqBody := map[string]any{}
	if status != "" {
		reqBody["status"] = status
	}
	if before != "" {
		reqBody["before"] = before
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := apiRequest(cmd, http.MethodDelete, "/api/v1/sessions", bytes.NewReader(reqJSON))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	deletedCount := result["deleted_count"]
	fmt.Fprintf(os.Stderr, "Deleted %v sessions\n", deletedCount)
	return nil
}

func runDeleteDryRun(cmd *cobra.Command, status, before string) error {
	// Build query parameters for listing sessions
	params := url.Values{}
	if status != "" {
		params.Set("status", status)
	}
	if before != "" {
		// For listing, we need to convert the "before" parameter to "until"
		var untilTime time.Time
		var err error

		if strings.HasSuffix(before, "d") {
			// Duration format like "7d", "30d"
			daysStr := strings.TrimSuffix(before, "d")
			days, parseErr := strconv.Atoi(daysStr)
			if parseErr != nil {
				return fmt.Errorf("invalid before parameter: must be RFC3339 datetime or duration like '7d'")
			}
			untilTime = time.Now().UTC().AddDate(0, 0, -days)
		} else {
			// RFC3339 datetime format
			untilTime, err = time.Parse(time.RFC3339, before)
			if err != nil {
				return fmt.Errorf("invalid before parameter: must be RFC3339 datetime or duration like '7d'")
			}
		}
		params.Set("until", untilTime.Format(time.RFC3339))
	}

	// Get all pages of sessions that match the criteria
	page := 1
	perPage := 100
	totalSessions := 0

	for {
		params.Set("page", strconv.Itoa(page))
		params.Set("per_page", strconv.Itoa(perPage))

		resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/sessions?"+params.Encode(), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
		}

		var result struct {
			Sessions []map[string]any `json:"sessions"`
			Count    int              `json:"count"`
			Total    int              `json:"total"`
			Pages    int              `json:"pages"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}

		if page == 1 {
			if result.Total == 0 {
				fmt.Fprintf(os.Stderr, "No sessions found matching the criteria\n")
				return nil
			}
			fmt.Fprintf(os.Stderr, "Would delete %d sessions:\n", result.Total)
		}

		for _, session := range result.Sessions {
			fmt.Fprintf(os.Stderr, "  %s (%s) - %s\n",
				session["id"], session["status"], session["started_at"])
		}

		totalSessions += result.Count

		if page >= result.Pages {
			break
		}
		page++
	}

	fmt.Fprintf(os.Stderr, "\nTotal sessions to delete: %d\n", totalSessions)
	fmt.Fprintf(os.Stderr, "To confirm deletion, run without --dry-run\n")
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

	// Get proxy configuration for login request
	proxyConfig, err := resolveProxyConfig(cmd)
	if err != nil {
		return err
	}

	// Prompt for username and password
	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stderr, "Username: ")
	username, err = reader.ReadString('\n')
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

	client := newHTTPClient(proxyConfig)
	resp, err := client.Post(bridgeURL+"/api/v1/auth/login", "application/json", bytes.NewReader(data))
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
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &CLIConfig{}
	}
	cfg.Server = bridgeURL
	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	dir := configDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials"), []byte(tokenResp.Token), 0600); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Logged in to %s\n", bridgeURL)
	return nil
}

// ---------- config ----------

func saveConfig(cfg *CLIConfig) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Configuration saved to %s\n", path)
	return nil
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration management",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the current configuration",
		RunE:  runConfigValidate,
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig()
			if err != nil {
				fmt.Fprintf(os.Stderr, "No config file found at %s\n", filepath.Join(configDir(), "config.yaml"))
				return nil
			}
			data, _ := yaml.Marshal(cfg)
			fmt.Print(string(data))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := loadConfig()
			if cfg == nil {
				cfg = &CLIConfig{}
			}
			key, value := args[0], args[1]
			switch key {
			case "server":
				cfg.Server = value
			case "output":
				cfg.Output = value
			case "username":
				cfg.Username = value
			case "password":
				cfg.Password = value
			case "proxy_url":
				cfg.ProxyURL = value
			case "no_proxy":
				cfg.NoProxy = value
			case "defaults.repo":
				cfg.Defaults.Repo = value
			case "defaults.provider":
				cfg.Defaults.Provider = value
			case "defaults.model":
				cfg.Defaults.Model = value
			case "defaults.timeout":
				cfg.Defaults.Timeout = value
			case "defaults.budget":
				if b, err := strconv.ParseFloat(value, 64); err == nil {
					cfg.Defaults.Budget = b
				} else {
					return fmt.Errorf("invalid budget value: %s", value)
				}
			default:
				return fmt.Errorf("unknown config key: %s\nValid keys: server, output, username, password, proxy_url, no_proxy, defaults.repo, defaults.provider, defaults.model, defaults.timeout, defaults.budget", key)
			}
			return saveConfig(cfg)
		},
	})
	return cmd
}

func runConfigValidate(cmd *cobra.Command, _ []string) error {
	dir := configDir()
	configPath := filepath.Join(dir, "config.yaml")
	credsPath := filepath.Join(dir, "credentials")

	var issues []string

	cfg, err := loadConfig()
	if err != nil {
		issues = append(issues, fmt.Sprintf("config: cannot read %s: %v", configPath, err))
	} else {
		if cfg.Server == "" {
			issues = append(issues, "config: 'server' is not set")
		} else {
			fmt.Fprintf(os.Stderr, "config: server = %s\n", cfg.Server)
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

	proxyConfig, err := resolveProxyConfig(cmd)
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

	client := &http.Client{
		Timeout:   0, // no timeout for SSE
		Transport: newHTTPTransport(proxyConfig),
	}
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
