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
	Server string `yaml:"server"`
}

func main() {
	root := &cobra.Command{
		Use:   "alcove",
		Short: "Alcove — sandboxed AI coding agents",
		Long:  "Alcove CLI for dispatching and managing AI coding tasks via the Bridge API.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().String("server", "", "Bridge server URL (overrides config and ALCOVE_SERVER)")
	root.PersistentFlags().String("output", "", "Output format: json or table (default: table)")

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

	token, err := loadToken()
	if err == nil && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return newHTTPClient().Do(req)
}

func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func isJSONOutput(cmd *cobra.Command) bool {
	o, _ := cmd.Flags().GetString("output")
	return strings.EqualFold(o, "json")
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
	ID         string  `json:"id"`
	Prompt     string  `json:"prompt"`
	Repo       string  `json:"repo,omitempty"`
	Provider   string  `json:"provider"`
	Status     string  `json:"status"`
	StartedAt  string  `json:"started_at"`
	Duration   string  `json:"duration,omitempty"`
	ExitCode   *int    `json:"exit_code,omitempty"`
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
	ID         string   `json:"id"`
	TaskID     string   `json:"task_id"`
	Prompt     string   `json:"prompt"`
	Repo       string   `json:"repo,omitempty"`
	Provider   string   `json:"provider"`
	Status     string   `json:"status"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at,omitempty"`
	Duration   string   `json:"duration,omitempty"`
	ExitCode   *int     `json:"exit_code,omitempty"`
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
	password, err := reader.ReadString('\n')
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
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the current configuration",
		RunE:  runConfigValidate,
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

	req, err := http.NewRequest(http.MethodGet, server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	token, err := loadToken()
	if err == nil && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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
