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

// CLIProfile holds per-profile CLI configuration.
type CLIProfile struct {
	Server     string `yaml:"server,omitempty"`
	Output     string `yaml:"output,omitempty"`      // "json" or "table"
	Username   string `yaml:"username,omitempty"`    // Basic Auth username
	Password   string `yaml:"password,omitempty"`    // Basic Auth password
	ProxyURL   string `yaml:"proxy_url,omitempty"`   // HTTP proxy
	NoProxy    string `yaml:"no_proxy,omitempty"`    // Comma-separated no-proxy hosts
	ActiveTeam string `yaml:"active_team,omitempty"` // Active team name
	Defaults   struct {
		Repo     string  `yaml:"repo,omitempty"`     // Default repository
		Provider string  `yaml:"provider,omitempty"` // Default LLM provider
		Model    string  `yaml:"model,omitempty"`    // Default model
		Timeout  string  `yaml:"timeout,omitempty"`  // Default timeout (e.g., "30m")
		Budget   float64 `yaml:"budget,omitempty"`   // Default budget in USD
	} `yaml:"defaults,omitempty"`
}

// CLIConfig holds the user-level CLI configuration, including named profiles.
type CLIConfig struct {
	ActiveProfile string                `yaml:"active_profile,omitempty"`
	Profiles      map[string]CLIProfile `yaml:"profiles,omitempty"`

	// Top-level fields for backward compat (the "default" profile)
	CLIProfile `yaml:",inline"`
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
		Long:          "Alcove CLI for dispatching and managing AI coding sessions via the Bridge API.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().String("server", "", "Bridge server URL (overrides config and ALCOVE_SERVER)")
	root.PersistentFlags().String("output", "", "Output format: json or table (default: table)")
	root.PersistentFlags().StringP("username", "u", "", "Username for Basic Auth (overrides ALCOVE_USERNAME)")
	root.PersistentFlags().StringP("password", "p", "", "Password for Basic Auth (overrides ALCOVE_PASSWORD)")
	root.PersistentFlags().String("proxy-url", "", "HTTP/HTTPS proxy URL (overrides environment)")
	root.PersistentFlags().String("no-proxy", "", "Comma-separated list of hosts to exclude from proxy (overrides NO_PROXY env var)")
	root.PersistentFlags().String("profile", "", "Use a named profile from config (overrides active_profile)")
	root.PersistentFlags().String("team", "", "Team name to use for this invocation (overrides active_team in profile)")

	root.AddCommand(
		newRunCmd(),
		newListCmd(),
		newLogsCmd(),
		newStatusCmd(),
		newCancelCmd(),
		newDeleteCmd(),
		newLoginCmd(),
		newConfigCmd(),
		newProfileCmd(),
		newTeamsCmd(),
		newCatalogCmd(),
		newCredentialsCmd(),
		newAgentsCmd(),
		newWorkflowsCmd(),
		newVersionCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// resolveProfile resolves the active CLIProfile based on --profile flag,
// ALCOVE_PROFILE env var, active_profile in config, or top-level (inline) fields.
func resolveProfile(cmd *cobra.Command) (*CLIProfile, error) {
	cfg, err := loadConfig()
	if err != nil {
		return &CLIProfile{}, nil // no config file — empty profile
	}

	// Determine which profile name to use
	profileName, _ := cmd.Flags().GetString("profile")
	if profileName == "" {
		profileName = os.Getenv("ALCOVE_PROFILE")
	}
	if profileName == "" {
		profileName = cfg.ActiveProfile
	}

	// Look up the named profile
	if profileName != "" && cfg.Profiles != nil {
		if profile, ok := cfg.Profiles[profileName]; ok {
			return &profile, nil
		}
		return nil, fmt.Errorf("profile %q not found in config", profileName)
	}

	// Fall back to top-level (inline) config
	return &cfg.CLIProfile, nil
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
	// 3. Active profile from config file
	profile, err := resolveProfile(cmd)
	if err != nil {
		return "", err
	}
	if profile.Server != "" {
		return strings.TrimRight(profile.Server, "/"), nil
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

	// 3. Active profile from config file
	if profile, err := resolveProfile(cmd); err == nil {
		username = profile.Username
		password = profile.Password
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

	// 3. Active profile from config file
	if config.ProxyURL == "" {
		if profile, err := resolveProfile(cmd); err == nil && profile.ProxyURL != "" {
			if err := validateProxyURL(profile.ProxyURL); err == nil {
				config.ProxyURL = profile.ProxyURL
			}
			if profile.NoProxy != "" && len(config.NoProxy) == 0 {
				config.NoProxy = parseNoProxy(profile.NoProxy)
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

// getConfigPaths returns a list of config file paths to check in order of precedence.
func getConfigPaths() []string {
	var paths []string

	// 1. XDG config home (if set)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "alcove", "config.yaml"))
	}

	// 2. Standard XDG config location
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "alcove", "config.yaml"))
		// 3. Convenience location mentioned in original issue
		paths = append(paths, filepath.Join(home, ".alcove.yaml"))
	}

	return paths
}

func loadConfig() (*CLIConfig, error) {
	var lastErr error

	for _, path := range getConfigPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue // Try next path
		}

		var cfg CLIConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing config file %s: %w", path, err)
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

func newHTTPClient(proxyConfig *ProxyConfig) *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: newHTTPTransport(proxyConfig),
	}
}

// teamInfo represents a team returned from the API.
type teamInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsPersonal bool   `json:"is_personal"`
	CreatedAt  string `json:"created_at"`
}

// teamsListResponse is the response from GET /api/v1/teams.
type teamsListResponse struct {
	Teams []teamInfo `json:"teams"`
}

// resolveTeamName determines the team name to use from --team flag or active_team config.
func resolveTeamName(cmd *cobra.Command) string {
	if teamFlag, _ := cmd.Flags().GetString("team"); teamFlag != "" {
		return teamFlag
	}
	if profile, err := resolveProfile(cmd); err == nil && profile.ActiveTeam != "" {
		return profile.ActiveTeam
	}
	return ""
}

// resolveTeamID resolves a team name to its ID by calling the teams API.
// It uses apiRequestRaw to avoid infinite recursion (apiRequest calls resolveTeamID).
func resolveTeamID(cmd *cobra.Command, teamName string) (string, error) {
	resp, err := apiRequestRaw(cmd, http.MethodGet, "/api/v1/teams", nil, "")
	if err != nil {
		return "", fmt.Errorf("fetching teams: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("fetching teams: bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result teamsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding teams response: %w", err)
	}

	for _, team := range result.Teams {
		if team.Name == teamName {
			return team.ID, nil
		}
	}
	return "", fmt.Errorf("team %q not found", teamName)
}

// apiRequestRaw performs an authenticated HTTP request with an explicit team ID header.
// This is the low-level function; apiRequest wraps it with team name resolution.
func apiRequestRaw(cmd *cobra.Command, method, path string, body interface{}, teamID string) (*http.Response, error) {
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

	// Set team header if provided
	if teamID != "" {
		req.Header.Set("X-Alcove-Team", teamID)
	}

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

// apiRequest performs an authenticated HTTP request to the Bridge API.
// It automatically resolves the team name (from --team flag or active_team config)
// to a team ID and sets the X-Alcove-Team header.
func apiRequest(cmd *cobra.Command, method, path string, body interface{}) (*http.Response, error) {
	teamName := resolveTeamName(cmd)
	var teamID string
	if teamName != "" {
		var err error
		teamID, err = resolveTeamID(cmd, teamName)
		if err != nil {
			return nil, err
		}
	}

	return apiRequestRaw(cmd, method, path, body, teamID)
}

func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// formatDurationForDisplay formats session duration for better readability in list output
func formatDurationForDisplay(duration, status, startedAt string) string {
	if duration != "" {
		// Parse the Go duration string and format it nicely
		if d, err := time.ParseDuration(duration); err == nil {
			return formatDuration(d)
		}
		// Fall back to original duration if parsing fails
		return duration
	}

	// For running sessions, calculate elapsed time
	if status == "running" {
		if startTime, err := time.Parse(time.RFC3339, startedAt); err == nil {
			elapsed := time.Since(startTime)
			return formatDuration(elapsed) + "*"
		}
	}

	return "-"
}

// formatDuration formats a time.Duration for human readability
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	} else if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm%ds", minutes, seconds)
	} else {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
}

	if f, _ := cmd.Flags().GetString("output"); f == "json" {
		return true
	}
	if os.Getenv("ALCOVE_OUTPUT") == "json" {
		return true
	}
	if profile, err := resolveProfile(cmd); err == nil && profile.Output == "json" {
		return true
	}
	return false
}

// ---------- run ----------

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "Submit a new session to the Bridge",
		Long:  "Dispatch a coding session. By default returns the session ID immediately. Use --watch for live streaming.",
		Args:  cobra.ExactArgs(1),
		RunE:  runRun,
	}
	cmd.Flags().String("repo", "", "Target repository (e.g., org/repo)")
	cmd.Flags().String("provider", "", "LLM provider name")
	cmd.Flags().String("model", "", "Model override (e.g., claude-sonnet-4-20250514)")
	cmd.Flags().Float64("budget", 0, "Budget limit in USD (e.g., 5.00)")
	cmd.Flags().Duration("timeout", 0, "Session timeout (e.g., 30m, 1h)")
	cmd.Flags().Bool("watch", false, "Stream transcript via SSE after dispatch")
	cmd.Flags().Bool("debug", false, "Keep containers after exit for log inspection")
	cmd.Flags().Bool("direct-outbound", false, "Allow direct outbound network connections (bypasses Gate proxy)")
	return cmd
}

type runRequest struct {
	Prompt         string  `json:"prompt"`
	Repo           string  `json:"repo,omitempty"`
	Provider       string  `json:"provider,omitempty"`
	Timeout        int     `json:"timeout,omitempty"`
	Model          string  `json:"model,omitempty"`
	Budget         float64 `json:"budget_usd,omitempty"`
	Debug          bool    `json:"debug,omitempty"`
	DirectOutbound bool    `json:"direct_outbound,omitempty"`
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
	reqBody.DirectOutbound, _ = cmd.Flags().GetBool("direct-outbound")

	// Fall back to active profile defaults
	if profile, err := resolveProfile(cmd); err == nil {
		if reqBody.Repo == "" {
			reqBody.Repo = profile.Defaults.Repo
		}
		if reqBody.Provider == "" {
			reqBody.Provider = profile.Defaults.Provider
		}
		if reqBody.Model == "" {
			reqBody.Model = profile.Defaults.Model
		}
		if reqBody.Budget == 0 && profile.Defaults.Budget > 0 {
			reqBody.Budget = profile.Defaults.Budget
		}
		if reqBody.Timeout == 0 && profile.Defaults.Timeout != "" {
			if d, err := time.ParseDuration(profile.Defaults.Timeout); err == nil {
				reqBody.Timeout = int(d.Seconds())
			}
		}
	}

	resp, err := apiRequest(cmd, http.MethodPost, "/api/v1/sessions", reqBody)
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

		// Format duration for better readability
		duration := formatDurationForDisplay(s.Duration, s.Status, s.StartedAt)

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Status, s.Repo, s.Provider, duration, prompt)
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
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Prompt         string `json:"prompt"`
	Repo           string `json:"repo,omitempty"`
	Provider       string `json:"provider"`
	Status         string `json:"status"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at,omitempty"`
	Duration       string `json:"duration,omitempty"`
	ExitCode       *int   `json:"exit_code,omitempty"`
	DirectOutbound bool   `json:"direct_outbound,omitempty"`
	Artifacts      []struct {
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
	if result.DirectOutbound {
		fmt.Fprintf(os.Stdout, "Network:    direct outbound\n")
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

	// Determine which profile to save the server URL on
	loginProfileName, _ := cmd.Flags().GetString("profile")
	if loginProfileName == "" {
		loginProfileName = os.Getenv("ALCOVE_PROFILE")
	}
	if loginProfileName == "" {
		loginProfileName = cfg.ActiveProfile
	}

	if loginProfileName != "" && cfg.Profiles != nil {
		if profile, ok := cfg.Profiles[loginProfileName]; ok {
			profile.Server = bridgeURL
			cfg.Profiles[loginProfileName] = profile
		} else {
			cfg.Server = bridgeURL
		}
	} else {
		cfg.Server = bridgeURL
	}
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
		Use:   "init",
		Short: "Create an example configuration file",
		Long:  "Create an example configuration file with all available options and documentation.",
		RunE:  runConfigInit,
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the current configuration",
		RunE:  runConfigValidate,
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show current effective configuration",
		Long:  "Display current effective configuration showing values from all sources (flags, environment, config file).",
		RunE:  runConfigShow,
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long:  "Set a configuration value. When a named profile is active (via --profile flag, ALCOVE_PROFILE env, or active_profile in config), the value is set on that profile. Otherwise it is set on the top-level config.",
		Args:  cobra.ExactArgs(2),
		RunE:  runConfigSet,
	})
	return cmd
}

func setProfileField(profile *CLIProfile, key, value string) error {
	switch key {
	case "server":
		profile.Server = value
	case "output":
		profile.Output = value
	case "username":
		profile.Username = value
	case "password":
		profile.Password = value
	case "proxy_url":
		profile.ProxyURL = value
	case "no_proxy":
		profile.NoProxy = value
	case "active_team":
		profile.ActiveTeam = value
	case "defaults.repo":
		profile.Defaults.Repo = value
	case "defaults.provider":
		profile.Defaults.Provider = value
	case "defaults.model":
		profile.Defaults.Model = value
	case "defaults.timeout":
		profile.Defaults.Timeout = value
	case "defaults.budget":
		if b, err := strconv.ParseFloat(value, 64); err == nil {
			profile.Defaults.Budget = b
		} else {
			return fmt.Errorf("invalid budget value: %s", value)
		}
	default:
		return fmt.Errorf("unknown config key: %s\nValid keys: server, output, username, password, proxy_url, no_proxy, active_team, defaults.repo, defaults.provider, defaults.model, defaults.timeout, defaults.budget", key)
	}
	return nil
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &CLIConfig{}
	}
	key, value := args[0], args[1]

	// Determine which profile to set the value on
	profileName, _ := cmd.Flags().GetString("profile")
	if profileName == "" {
		profileName = os.Getenv("ALCOVE_PROFILE")
	}
	if profileName == "" {
		profileName = cfg.ActiveProfile
	}

	if profileName != "" {
		// Set on named profile
		if cfg.Profiles == nil {
			cfg.Profiles = make(map[string]CLIProfile)
		}
		profile, ok := cfg.Profiles[profileName]
		if !ok {
			return fmt.Errorf("profile %q not found in config; use 'alcove profile add %s' to create it", profileName, profileName)
		}
		if err := setProfileField(&profile, key, value); err != nil {
			return err
		}
		cfg.Profiles[profileName] = profile
	} else {
		// Set on top-level (inline) config
		if err := setProfileField(&cfg.CLIProfile, key, value); err != nil {
			return err
		}
	}
	return saveConfig(cfg)
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

func runConfigInit(cmd *cobra.Command, _ []string) error {
	// Determine the best config path to create
	configPath := filepath.Join(configDir(), "config.yaml")

	// Check if a config file already exists
	existing := false
	for _, path := range getConfigPaths() {
		if _, err := os.Stat(path); err == nil {
			existing = true
			fmt.Fprintf(os.Stderr, "Configuration file already exists at: %s\n", path)
			fmt.Fprintf(os.Stderr, "Use 'alcove config show' to view current configuration.\n")
			break
		}
	}

	if existing {
		return fmt.Errorf("configuration file already exists")
	}

	// Create example configuration with extensive documentation
	exampleConfig := `# Alcove CLI Configuration File
# This file contains default values for Alcove CLI commands.
# You can override any setting with command-line flags or environment variables.
#
# Precedence order (highest to lowest):
# 1. Command-line flags (--server, --provider, etc.)
# 2. Environment variables (ALCOVE_SERVER, ALCOVE_OUTPUT, etc.)
# 3. This configuration file
# 4. Built-in defaults

# Bridge server URL (required)
# This can also be set via ALCOVE_SERVER environment variable or --server flag
# Example: https://alcove.example.com
server: ""

# Output format: "table" or "json" (optional, default: table)
# This can also be set via ALCOVE_OUTPUT environment variable or --output flag
output: "table"

# Basic Authentication (optional)
# If using Basic Auth instead of token-based authentication
# These can also be set via ALCOVE_USERNAME/ALCOVE_PASSWORD environment variables
# or --username/--password flags
# username: ""
# password: ""

# HTTP Proxy Configuration (optional)
# These can also be set via standard proxy environment variables
# (HTTP_PROXY, HTTPS_PROXY, NO_PROXY) or --proxy-url/--no-proxy flags
# proxy_url: "http://proxy.example.com:8080"
# no_proxy: "localhost,127.0.0.1,.example.com"

# Default values for common command options (optional)
defaults:
  # Default repository for 'run' command (optional)
  # Example: "myorg/myproject"
  # repo: ""

  # Default LLM provider for 'run' command (optional)
  # Example: "anthropic", "openai"
  # provider: ""

  # Default model for 'run' command (optional)
  # Example: "claude-sonnet-4-20250514", "gpt-4"
  # model: ""

  # Default timeout for 'run' command (optional)
  # Accepts Go duration syntax: 30m, 1h, 2h30m, etc.
  # timeout: "30m"

  # Default budget limit in USD for 'run' command (optional)
  # Example: 5.00, 10.50
  # budget: 0.0
`

	// Ensure config directory exists
	if err := os.MkdirAll(configDir(), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Write example config
	if err := os.WriteFile(configPath, []byte(exampleConfig), 0600); err != nil {
		return fmt.Errorf("writing example config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Example configuration file created at: %s\n", configPath)
	fmt.Fprintf(os.Stderr, "\nEdit the file to customize your settings, then run:\n")
	fmt.Fprintf(os.Stderr, "  alcove config validate  # Check configuration\n")
	fmt.Fprintf(os.Stderr, "  alcove config show      # View effective settings\n")

	return nil
}

func runConfigShow(cmd *cobra.Command, _ []string) error {
	// Show effective configuration from all sources
	fmt.Fprintln(os.Stderr, "Effective configuration:")
	fmt.Fprintln(os.Stderr, "")

	// Active profile
	profileName, _ := cmd.Flags().GetString("profile")
	if profileName == "" {
		profileName = os.Getenv("ALCOVE_PROFILE")
	}
	if profileName == "" {
		if cfg, err := loadConfig(); err == nil {
			profileName = cfg.ActiveProfile
		}
	}
	if profileName != "" {
		fmt.Fprintf(os.Stderr, "Profile:      %s\n", profileName)
	} else {
		fmt.Fprintf(os.Stderr, "Profile:      <default>\n")
	}

	// Server resolution
	server, serverErr := resolveServer(cmd)
	if serverErr != nil {
		fmt.Fprintf(os.Stderr, "Server:       <not configured> (%v)\n", serverErr)
	} else {
		fmt.Fprintf(os.Stderr, "Server:       %s\n", server)
	}

	// Output format
	output := "table" // default
	if f, _ := cmd.Flags().GetString("output"); f != "" {
		fmt.Fprintf(os.Stderr, "Output:       %s (from --output flag)\n", f)
	} else if env := os.Getenv("ALCOVE_OUTPUT"); env != "" {
		fmt.Fprintf(os.Stderr, "Output:       %s (from ALCOVE_OUTPUT env)\n", env)
	} else if profile, err := resolveProfile(cmd); err == nil && profile.Output != "" {
		fmt.Fprintf(os.Stderr, "Output:       %s (from config file)\n", profile.Output)
	} else {
		fmt.Fprintf(os.Stderr, "Output:       %s (default)\n", output)
	}

	// Basic Auth
	username, _ := resolveBasicAuth(cmd)
	if username != "" {
		fmt.Fprintf(os.Stderr, "Auth:         Basic Auth (username: %s)\n", username)
	} else if token, err := loadToken(); err == nil && token != "" {
		fmt.Fprintf(os.Stderr, "Auth:         Bearer token (%d chars)\n", len(token))
	} else {
		fmt.Fprintf(os.Stderr, "Auth:         <not configured>\n")
	}

	// Proxy configuration
	if proxyConfig, err := resolveProxyConfig(cmd); err == nil {
		if proxyConfig.ProxyURL != "" {
			fmt.Fprintf(os.Stderr, "Proxy:        %s\n", proxyConfig.ProxyURL)
			if len(proxyConfig.NoProxy) > 0 {
				fmt.Fprintf(os.Stderr, "No Proxy:     %s\n", strings.Join(proxyConfig.NoProxy, ", "))
			}
		} else {
			fmt.Fprintf(os.Stderr, "Proxy:        <none>\n")
		}
	}

	// Defaults from active profile
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Default values for 'run' command:")

	if profile, err := resolveProfile(cmd); err == nil {
		if profile.Defaults.Repo != "" {
			fmt.Fprintf(os.Stderr, "  Repository: %s\n", profile.Defaults.Repo)
		} else {
			fmt.Fprintf(os.Stderr, "  Repository: <none>\n")
		}

		if profile.Defaults.Provider != "" {
			fmt.Fprintf(os.Stderr, "  Provider:   %s\n", profile.Defaults.Provider)
		} else {
			fmt.Fprintf(os.Stderr, "  Provider:   <none>\n")
		}

		if profile.Defaults.Model != "" {
			fmt.Fprintf(os.Stderr, "  Model:      %s\n", profile.Defaults.Model)
		} else {
			fmt.Fprintf(os.Stderr, "  Model:      <none>\n")
		}

		if profile.Defaults.Timeout != "" {
			fmt.Fprintf(os.Stderr, "  Timeout:    %s\n", profile.Defaults.Timeout)
		} else {
			fmt.Fprintf(os.Stderr, "  Timeout:    <none>\n")
		}

		if profile.Defaults.Budget > 0 {
			fmt.Fprintf(os.Stderr, "  Budget:     $%.2f\n", profile.Defaults.Budget)
		} else {
			fmt.Fprintf(os.Stderr, "  Budget:     <none>\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "  <no config file found>\n")
	}

	// Show config file locations
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Configuration file search order:")
	for i, path := range getConfigPaths() {
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(os.Stderr, "  %d. %s (found)\n", i+1, path)
		} else {
			fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, path)
		}
	}

	return nil
}

// ---------- profile ----------

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage named profiles for multiple Alcove installations",
	}
	cmd.AddCommand(
		newProfileListCmd(),
		newProfileUseCmd(),
		newProfileAddCmd(),
		newProfileRemoveCmd(),
	)
	return cmd
}

func newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all profiles",
		RunE:  runProfileList,
	}
}

func runProfileList(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("no config file found; use 'alcove profile add <name> --server <url>' to create a profile")
	}

	if len(cfg.Profiles) == 0 {
		fmt.Fprintln(os.Stderr, "No profiles configured. Use 'alcove profile add <name> --server <url>' to create one.")
		return nil
	}

	if isJSONOutput(cmd) {
		result := make([]map[string]interface{}, 0, len(cfg.Profiles))
		for name, profile := range cfg.Profiles {
			entry := map[string]interface{}{
				"name":   name,
				"server": profile.Server,
				"active": name == cfg.ActiveProfile,
			}
			result = append(result, entry)
		}
		return outputJSON(result)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for name, profile := range cfg.Profiles {
		marker := " "
		if name == cfg.ActiveProfile {
			marker = "*"
		}
		fmt.Fprintf(w, "%s %s\t%s\n", marker, name, profile.Server)
	}
	return w.Flush()
}

func newProfileUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the active profile",
		Args:  cobra.ExactArgs(1),
		RunE:  runProfileUse,
	}
}

func runProfileUse(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("no config file found")
	}

	if cfg.Profiles == nil {
		return fmt.Errorf("no profiles configured; use 'alcove profile add %s --server <url>' to create one", name)
	}

	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found in config", name)
	}

	cfg.ActiveProfile = name
	return saveConfig(cfg)
}

func newProfileAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a new profile",
		Args:  cobra.ExactArgs(1),
		RunE:  runProfileAdd,
	}
	cmd.Flags().String("server", "", "Bridge server URL (required)")
	cmd.Flags().String("username", "", "Username for Basic Auth")
	cmd.Flags().String("password", "", "Password for Basic Auth")
	cmd.Flags().String("proxy-url", "", "HTTP/HTTPS proxy URL")
	cmd.Flags().String("no-proxy", "", "Comma-separated no-proxy hosts")
	return cmd
}

func runProfileAdd(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &CLIConfig{}
	}

	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]CLIProfile)
	}

	if _, ok := cfg.Profiles[name]; ok {
		return fmt.Errorf("profile %q already exists; use 'alcove config set' to modify it", name)
	}

	profile := CLIProfile{}
	profile.Server, _ = cmd.Flags().GetString("server")
	// Use the profile-add-specific flags, not the global ones
	if u, _ := cmd.Flags().GetString("username"); u != "" {
		profile.Username = u
	}
	if p, _ := cmd.Flags().GetString("password"); p != "" {
		profile.Password = p
	}
	if pu, _ := cmd.Flags().GetString("proxy-url"); pu != "" {
		profile.ProxyURL = pu
	}
	if np, _ := cmd.Flags().GetString("no-proxy"); np != "" {
		profile.NoProxy = np
	}

	cfg.Profiles[name] = profile
	return saveConfig(cfg)
}

func newProfileRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a profile",
		Args:  cobra.ExactArgs(1),
		RunE:  runProfileRemove,
	}
}

func runProfileRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("no config file found")
	}

	if cfg.Profiles == nil {
		return fmt.Errorf("no profiles configured")
	}

	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found in config", name)
	}

	delete(cfg.Profiles, name)

	// Clear active_profile if it was the removed profile
	if cfg.ActiveProfile == name {
		cfg.ActiveProfile = ""
	}

	return saveConfig(cfg)
}

// ---------- version ----------

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print client and server versions",
		RunE:  runVersion,
	}
}

func runVersion(cmd *cobra.Command, _ []string) error {
	if isJSONOutput(cmd) {
		result := map[string]string{"client": Version}

		// Try to get server version
		if serverVersion, err := getServerVersion(cmd); err == nil {
			result["server"] = serverVersion
		}

		return outputJSON(result)
	}

	fmt.Printf("Client: %s\n", Version)

	// Try to get server version
	serverVersion, err := getServerVersion(cmd)
	if err != nil {
		fmt.Printf("Server: (could not reach server: %v)\n", err)
	} else {
		fmt.Printf("Server: %s\n", serverVersion)
	}

	return nil
}

func getServerVersion(cmd *cobra.Command) (string, error) {
	resp, err := apiRequest(cmd, "GET", "/api/v1/health", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("health endpoint returned %d", resp.StatusCode)
	}

	var healthResp struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&healthResp); err != nil {
		return "", fmt.Errorf("failed to decode health response: %w", err)
	}

	return healthResp.Version, nil
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

	// Set team header if applicable
	teamName := resolveTeamName(cmd)
	if teamName != "" {
		teamID, err := resolveTeamID(cmd, teamName)
		if err != nil {
			return err
		}
		req.Header.Set("X-Alcove-Team", teamID)
	}

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
