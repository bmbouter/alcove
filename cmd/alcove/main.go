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
	// Try multiple locations in priority order:
	// 1. ~/.config/alcove/config.yaml (XDG standard, current location)
	// 2. ~/.alcove.yaml (convenience location)
	// 3. $XDG_CONFIG_HOME/alcove/config.yaml (if XDG_CONFIG_HOME is set)

	var configPaths []string

	// XDG standard location
	configPaths = append(configPaths, filepath.Join(configDir(), "config.yaml"))

	// Convenience location
	if home, err := os.UserHomeDir(); err == nil {
		configPaths = append(configPaths, filepath.Join(home, ".alcove.yaml"))
	}

	// XDG_CONFIG_HOME location (if different from first)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		xdgPath := filepath.Join(xdg, "alcove", "config.yaml")
		if xdgPath != configPaths[0] {
			configPaths = append(configPaths, xdgPath)
		}
	}

	var lastErr error
	for _, path := range configPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}

		var cfg CLIConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid YAML in %s: %w", path, err)
		}
		return &cfg, nil
	}

	return nil, lastErr
}

func loadToken() (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir(), "credentials"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// resolveStringConfig resolves string config values with precedence: CLI flag > Environment variable > Config file > Default
func resolveStringConfig(cmd *cobra.Command, flagName, envVar, configValue, defaultValue string) string {
	// 1. CLI flag (highest priority)
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

// resolveFloat64Config resolves float64 config values with precedence: CLI flag > Config file > Default
func resolveFloat64Config(cmd *cobra.Command, flagName string, configValue float64, defaultValue float64) float64 {
	// 1. CLI flag (highest priority)
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

// resolveDurationConfig resolves duration config values with precedence: CLI flag > Config file > Default
func resolveDurationConfig(cmd *cobra.Command, flagName string, configValue string, defaultValue time.Duration) (time.Duration, error) {
	// 1. CLI flag (highest priority)
	if flagValue, _ := cmd.Flags().GetDuration(flagName); flagValue > 0 {
		return flagValue, nil
	}
	// 2. Config file
	if configValue != "" {
		parsed, err := time.ParseDuration(configValue)
		if err != nil {
			return 0, fmt.Errorf("invalid timeout format in config: %w", err)
		}
		return parsed, nil
	}
	// 3. Default value
	return defaultValue, nil
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

// resolveOutputFormat resolves the output format with config file support
func resolveOutputFormat(cmd *cobra.Command) string {
	cfg, err := loadConfig()
	configValue := ""
	if err == nil {
		configValue = cfg.Output
	}

	return resolveStringConfig(cmd, "output", "", configValue, "table")
}

func isJSONOutput(cmd *cobra.Command) bool {
	output := resolveOutputFormat(cmd)
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
	// Load config file to get defaults
	cfg, err := loadConfig()
	if err != nil {
		// Config file is optional for run command, just use empty config
		cfg = &CLIConfig{}
	}

	reqBody := runRequest{Prompt: args[0]}

	// Resolve parameters with proper precedence
	reqBody.Repo = resolveStringConfig(cmd, "repo", "", cfg.Repo, "")
	reqBody.Provider = resolveStringConfig(cmd, "provider", "", cfg.Provider, "")
	reqBody.Model = resolveStringConfig(cmd, "model", "", cfg.Model, "")

	// Budget resolution
	budget := resolveFloat64Config(cmd, "budget", cfg.Budget, 0)
	if budget > 0 {
		reqBody.Budget = budget
	}

	// Timeout resolution
	timeout, err := resolveDurationConfig(cmd, "timeout", cfg.Timeout, 0)
	if err != nil {
		return fmt.Errorf("resolving timeout: %w", err)
	}
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
	// Load config file to get defaults
	cfg, err := loadConfig()
	if err != nil {
		cfg = &CLIConfig{}
	}

	var params []string
	if s, _ := cmd.Flags().GetString("status"); s != "" {
		params = append(params, "status="+s)
	}

	// Use config file default for repo if not specified via flag
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
			Short: "Create an example configuration file",
			RunE:  runConfigInit,
		},
		&cobra.Command{
			Use:   "show",
			Short: "Show current effective configuration",
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
		issues = append(issues, fmt.Sprintf("config: cannot read configuration file: %v", err))
	} else {
		if cfg.Server == "" {
			issues = append(issues, "config: 'server' is not set")
		} else {
			fmt.Fprintf(os.Stderr, "config: server = %s\n", cfg.Server)
		}

		// Validate new config fields
		if cfg.Output != "" && !strings.EqualFold(cfg.Output, "json") && !strings.EqualFold(cfg.Output, "table") {
			issues = append(issues, fmt.Sprintf("config: invalid output format '%s' (must be 'json' or 'table')", cfg.Output))
		}

		if cfg.Timeout != "" {
			if _, err := time.ParseDuration(cfg.Timeout); err != nil {
				issues = append(issues, fmt.Sprintf("config: invalid timeout format '%s': %v", cfg.Timeout, err))
			}
		}

		if cfg.Budget < 0 {
			issues = append(issues, "config: budget cannot be negative")
		}

		fmt.Fprintf(os.Stderr, "config: found configuration with %d fields\n", getConfigFieldCount(cfg))
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

// getConfigFieldCount counts non-empty fields in config
func getConfigFieldCount(cfg *CLIConfig) int {
	count := 0
	if cfg.Server != "" {
		count++
	}
	if cfg.Provider != "" {
		count++
	}
	if cfg.Model != "" {
		count++
	}
	if cfg.Budget > 0 {
		count++
	}
	if cfg.Timeout != "" {
		count++
	}
	if cfg.Output != "" {
		count++
	}
	if cfg.Repo != "" {
		count++
	}
	return count
}

func runConfigInit(cmd *cobra.Command, _ []string) error {
	dir := configDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Check if config file already exists
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("config file already exists at %s", configPath)
	}

	// Create config directory if it doesn't exist
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Create example config with helpful comments
	exampleConfig := `# Alcove CLI Configuration
# All fields are optional and will override corresponding flags when set

# Bridge server URL (can also be set via ALCOVE_SERVER env var or --server flag)
server: ""

# Default provider for tasks (optional)
# Examples: "anthropic", "openai", "google"
# provider: anthropic

# Default model override (optional)
# Examples: "claude-sonnet-4-20250514", "gpt-4", "gemini-pro"
# model: claude-sonnet-4-20250514

# Default budget limit in USD (optional)
# budget: 5.00

# Default timeout for tasks (optional, accepts Go duration syntax)
# Examples: "30m", "1h", "2h30m"
# timeout: 30m

# Default output format: "table" or "json" (optional)
# output: table

# Default repository for tasks (optional)
# Examples: "myorg/myproject", "username/repo"
# repo: myorg/myproject
`

	if err := os.WriteFile(configPath, []byte(exampleConfig), 0600); err != nil {
		return fmt.Errorf("writing example config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Example configuration created at %s\n", configPath)
	fmt.Fprintln(os.Stderr, "Edit the file to set your defaults, then run 'alcove config validate' to check it.")
	return nil
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		cfg = &CLIConfig{} // Use empty config if none found
		fmt.Fprintf(os.Stderr, "Warning: no config file found, showing defaults\n\n")
	}

	if isJSONOutput(cmd) {
		// Create effective config structure showing resolved values
		effectiveConfig := map[string]interface{}{
			"server":   resolveStringConfig(cmd, "server", "ALCOVE_SERVER", cfg.Server, ""),
			"provider": resolveStringConfig(cmd, "provider", "", cfg.Provider, ""),
			"model":    resolveStringConfig(cmd, "model", "", cfg.Model, ""),
			"budget":   resolveFloat64Config(cmd, "budget", cfg.Budget, 0),
			"output":   resolveOutputFormat(cmd),
			"repo":     resolveStringConfig(cmd, "repo", "", cfg.Repo, ""),
		}

		// Add timeout separately since it needs special handling
		if timeout, err := resolveDurationConfig(cmd, "timeout", cfg.Timeout, 0); err == nil && timeout > 0 {
			effectiveConfig["timeout"] = timeout.String()
		} else {
			effectiveConfig["timeout"] = ""
		}

		return outputJSON(effectiveConfig)
	}

	fmt.Println("Current effective configuration:")
	fmt.Println("(showing resolved values after applying precedence: flag > env > config > default)")
	fmt.Println()

	// Show server with source
	server := resolveStringConfig(cmd, "server", "ALCOVE_SERVER", cfg.Server, "")
	source := getConfigSource("server", cmd, "ALCOVE_SERVER", cfg.Server)
	fmt.Printf("Server:   %s %s\n", server, source)

	// Show provider with source
	provider := resolveStringConfig(cmd, "provider", "", cfg.Provider, "")
	if provider != "" {
		source := getConfigSource("provider", cmd, "", cfg.Provider)
		fmt.Printf("Provider: %s %s\n", provider, source)
	} else {
		fmt.Printf("Provider: (not set)\n")
	}

	// Show model with source
	model := resolveStringConfig(cmd, "model", "", cfg.Model, "")
	if model != "" {
		source := getConfigSource("model", cmd, "", cfg.Model)
		fmt.Printf("Model:    %s %s\n", model, source)
	} else {
		fmt.Printf("Model:    (not set)\n")
	}

	// Show budget with source
	budget := resolveFloat64Config(cmd, "budget", cfg.Budget, 0)
	if budget > 0 {
		source := getFloat64ConfigSource("budget", cmd, cfg.Budget)
		fmt.Printf("Budget:   %.2f %s\n", budget, source)
	} else {
		fmt.Printf("Budget:   (not set)\n")
	}

	// Show timeout with source
	timeout, err := resolveDurationConfig(cmd, "timeout", cfg.Timeout, 0)
	if err == nil && timeout > 0 {
		source := getDurationConfigSource("timeout", cmd, cfg.Timeout)
		fmt.Printf("Timeout:  %s %s\n", timeout, source)
	} else {
		fmt.Printf("Timeout:  (not set)\n")
	}

	// Show output with source
	output := resolveOutputFormat(cmd)
	source = getConfigSource("output", cmd, "", cfg.Output)
	fmt.Printf("Output:   %s %s\n", output, source)

	// Show repo with source
	repo := resolveStringConfig(cmd, "repo", "", cfg.Repo, "")
	if repo != "" {
		source := getConfigSource("repo", cmd, "", cfg.Repo)
		fmt.Printf("Repo:     %s %s\n", repo, source)
	} else {
		fmt.Printf("Repo:     (not set)\n")
	}

	return nil
}

// Helper functions to determine config source for display
func getConfigSource(flagName string, cmd *cobra.Command, envVar string, configValue string) string {
	if flagValue, _ := cmd.Flags().GetString(flagName); flagValue != "" {
		return "(from --" + flagName + " flag)"
	}
	if envVar != "" && os.Getenv(envVar) != "" {
		return "(from " + envVar + " env)"
	}
	if configValue != "" {
		return "(from config file)"
	}
	return "(default)"
}

func getFloat64ConfigSource(flagName string, cmd *cobra.Command, configValue float64) string {
	if flagValue, _ := cmd.Flags().GetFloat64(flagName); flagValue > 0 {
		return "(from --" + flagName + " flag)"
	}
	if configValue > 0 {
		return "(from config file)"
	}
	return "(default)"
}

func getDurationConfigSource(flagName string, cmd *cobra.Command, configValue string) string {
	if flagValue, _ := cmd.Flags().GetDuration(flagName); flagValue > 0 {
		return "(from --" + flagName + " flag)"
	}
	if configValue != "" {
		return "(from config file)"
	}
	return "(default)"
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
