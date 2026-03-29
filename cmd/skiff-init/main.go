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

// Command skiff-init is the PID 1 process inside Skiff containers.
// It reads the task from environment variables, runs Claude Code as a child process,
// streams transcript events to Ledger, and handles timeouts and cancellation.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/bmbouter/alcove/internal/hail"
	"github.com/bmbouter/alcove/internal/ledger"
)

const (
	defaultHeartbeatTimeout = 10 * time.Minute
	walBatchSize            = 50
	walFlushInterval        = 5 * time.Second
	shutdownGrace           = 10 * time.Second
)

func main() {
	log.SetPrefix("skiff-init: ")
	log.SetFlags(log.Ltime | log.Lmsgprefix)

	// --- Read task from environment variables (injected by Bridge) ---
	taskID := requireEnv("TASK_ID")
	sessionID := envOrDefault("SESSION_ID", taskID)
	prompt := requireEnv("PROMPT")
	repo := os.Getenv("REPO")
	branch := os.Getenv("BRANCH")
	provider := envOrDefault("PROVIDER", "anthropic")
	model := os.Getenv("CLAUDE_MODEL")
	budgetStr := os.Getenv("TASK_BUDGET")

	var budget float64
	if budgetStr != "" {
		budget, _ = strconv.ParseFloat(budgetStr, 64)
	}

	timeoutStr := envOrDefault("TASK_TIMEOUT", "3600")
	timeoutSecs, _ := strconv.Atoi(timeoutStr)
	if timeoutSecs <= 0 {
		timeoutSecs = 3600
	}

	heartbeatTimeout := parseDuration(os.Getenv("HEARTBEAT_TIMEOUT"), defaultHeartbeatTimeout)

	task := internal.Task{
		ID:       taskID,
		Prompt:   prompt,
		Repo:     repo,
		Branch:   branch,
		Provider: provider,
		Model:    model,
		Budget:   budget,
		Timeout:  time.Duration(timeoutSecs) * time.Second,
	}

	log.Printf("task %s received: prompt=%q repo=%s", task.ID, truncate(task.Prompt, 60), task.Repo)

	// --- Connect to NATS (Hail) for status updates and cancellation ---
	hailURL := envOrDefault("HAIL_URL", "nats://localhost:4222")
	log.Printf("connecting to Hail at %s", hailURL)
	hailClient, err := hail.Connect(hailURL)
	if err != nil {
		log.Printf("warning: could not connect to Hail: %v (continuing without status updates)", err)
		hailClient = nil
	}
	if hailClient != nil {
		defer hailClient.Close()
	}

	// --- Subscribe to cancellation ---
	var cancelCh <-chan struct{}
	if hailClient != nil {
		cancelCh, err = hailClient.SubscribeCancel(sessionID)
		if err != nil {
			log.Printf("warning: failed to subscribe to cancel topic: %v", err)
		}
	}
	if cancelCh == nil {
		// No-op cancel channel
		ch := make(chan struct{})
		cancelCh = ch
	}

	// --- Send running status ---
	if hailClient != nil {
		_ = hailClient.PublishStatus(task.ID, hail.StatusUpdate{
			TaskID:    task.ID,
			SessionID: sessionID,
			Status:    "running",
		})
	}

	// --- Create Ledger client ---
	ledgerURL := envOrDefault("LEDGER_URL", "http://localhost:8081")
	ledgerToken := os.Getenv("SESSION_TOKEN")
	lc := ledger.NewClient(ledgerURL, ledgerToken)

	// --- Set up environment ---
	setupEnv(task)

	// --- Clone repo if specified ---
	if task.Repo != "" {
		if err := cloneRepo(task.Repo, task.Branch); err != nil {
			log.Printf("warning: repo clone failed: %v", err)
		}
	}

	// --- Build context with hard timeout ---
	ctx, cancel := context.WithTimeout(context.Background(), task.Timeout)
	defer cancel()

	// --- Run Claude Code ---
	exitCode, outcome, artifacts := runClaude(ctx, task, sessionID, hailClient, lc, heartbeatTimeout, cancelCh)

	// --- Send final status ---
	if hailClient != nil {
		finalStatus := hail.StatusUpdate{
			TaskID:    task.ID,
			SessionID: sessionID,
			Status:    outcome,
			ExitCode:  &exitCode,
			Artifacts: artifacts,
		}
		_ = hailClient.PublishStatus(task.ID, finalStatus)
	}

	if err := lc.UpdateSession(sessionID, outcome, &exitCode, artifacts); err != nil {
		log.Printf("warning: failed to update final session status: %v", err)
	}

	log.Printf("task %s finished: %s (exit %d)", task.ID, outcome, exitCode)
	os.Exit(exitCode)
}

// runClaude executes Claude Code and streams its output. It returns the exit code,
// outcome string, and any artifacts.
func runClaude(
	ctx context.Context,
	task internal.Task,
	sessionID string,
	hailClient *hail.Client,
	lc *ledger.Client,
	heartbeatTimeout time.Duration,
	cancelCh <-chan struct{},
) (int, string, []internal.Artifact) {

	// Build command arguments
	args := []string{
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
		"--bare",
		"--session-id", task.ID,
	}
	if task.Model != "" {
		args = append(args, "--model", task.Model)
	}
	if task.Budget > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(task.Budget, 'f', 2, 64))
	}
	args = append(args, task.Prompt)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("error creating stdout pipe: %v", err)
		return 1, "error", nil
	}

	if err := cmd.Start(); err != nil {
		log.Printf("error starting claude: %v", err)
		return 1, "error", nil
	}

	// WAL file for local transcript persistence
	walPath := fmt.Sprintf("/tmp/alcove-transcript-%s.jsonl", task.ID)
	walFile, err := os.Create(walPath)
	if err != nil {
		log.Printf("warning: could not create WAL file %s: %v", walPath, err)
	}
	defer func() {
		if walFile != nil {
			walFile.Close()
		}
	}()

	// Read stdout line-by-line (NDJSON)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB line buffer

	var (
		batch            []json.RawMessage
		artifacts        []internal.Artifact
		lastEvent        = time.Now()
		ticker           = time.NewTicker(walFlushInterval)
		doneCh           = make(chan struct{})
		outcome          = "completed"
		sawSuccessResult bool
	)
	defer ticker.Stop()

	// Monitor heartbeat timeout and cancellation in a goroutine
	go func() {
		for {
			select {
			case <-doneCh:
				return
			case <-cancelCh:
				log.Println("cancellation received, sending SIGTERM to claude")
				outcome = "cancelled"
				_ = cmd.Process.Signal(syscall.SIGTERM)
				time.Sleep(shutdownGrace)
				_ = cmd.Process.Kill()
				return
			case <-ticker.C:
				if time.Since(lastEvent) > heartbeatTimeout {
					log.Printf("heartbeat timeout (%v without output), sending SIGTERM", heartbeatTimeout)
					outcome = "timeout"
					_ = cmd.Process.Signal(syscall.SIGTERM)
					time.Sleep(shutdownGrace)
					_ = cmd.Process.Kill()
					return
				}
			}
		}
	}()

	// Process output lines
	for scanner.Scan() {
		line := scanner.Bytes()
		lastEvent = time.Now()

		// Write to WAL
		if walFile != nil {
			_, _ = walFile.Write(line)
			_, _ = walFile.Write([]byte("\n"))
		}

		// Store the raw JSON line directly to preserve all fields
		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)

		// Publish to NATS for real-time SSE streaming
		if hailClient != nil {
			_ = hailClient.PublishTranscript(sessionID, lineCopy)
		}

		// Check for result events to determine success
		var rawMap map[string]any
		if json.Unmarshal(lineCopy, &rawMap) == nil {
			if rawMap["type"] == "result" {
				if isErr, ok := rawMap["is_error"].(bool); ok && !isErr {
					sawSuccessResult = true
				}
			}
		} else {
			continue // skip malformed lines
		}

		batch = append(batch, json.RawMessage(lineCopy))

		// Flush batch when it reaches the batch size
		if len(batch) >= walBatchSize {
			flushBatch(lc, sessionID, &batch)
		}
	}

	close(doneCh)

	// Flush remaining events
	if len(batch) > 0 {
		flushBatch(lc, sessionID, &batch)
	}

	// Wait for process to exit
	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	if ctx.Err() != nil {
		outcome = "timeout"
	} else if sawSuccessResult {
		outcome = "completed"
		exitCode = 0 // Override exit code for successful results
	} else if outcome == "completed" {
		outcome = "error"
	}

	return exitCode, outcome, artifacts
}

// flushBatch sends a batch of transcript events to Ledger and clears the batch.
func flushBatch(lc *ledger.Client, sessionID string, batch *[]json.RawMessage) {
	if err := lc.AppendTranscript(sessionID, *batch); err != nil {
		log.Printf("warning: failed to flush transcript batch to Ledger: %v", err)
	}
	*batch = nil
}

// setupEnv configures the environment for Claude Code execution.
func setupEnv(task internal.Task) {
	// Git configuration for non-interactive use
	setEnvIfMissing("GIT_TERMINAL_PROMPT", "0")
	setEnvIfMissing("GIT_AUTHOR_NAME", "Alcove")
	setEnvIfMissing("GIT_AUTHOR_EMAIL", "alcove@localhost")
	setEnvIfMissing("GIT_COMMITTER_NAME", "Alcove")
	setEnvIfMissing("GIT_COMMITTER_EMAIL", "alcove@localhost")

	// Set Gate credential URL for the git credential helper.
	// The credential helper script reads GATE_CREDENTIAL_URL to know where to POST.
	if gateURL := os.Getenv("ANTHROPIC_BASE_URL"); gateURL != "" {
		// ANTHROPIC_BASE_URL points to http://gate-<taskID>:8443
		// The credential helper needs the same base URL
		setEnvIfMissing("GATE_CREDENTIAL_URL", gateURL)
	}

	// Force HTTPS for git operations (SSH bypasses Gate credential helper).
	setEnvIfMissing("GIT_SSH_COMMAND", "echo 'SSH disabled — use HTTPS' && exit 1")

	// Configure MCP servers for Claude Code if specified.
	if mcpConfig := os.Getenv("ALCOVE_MCP_CONFIG"); mcpConfig != "" {
		configureMCPServers(mcpConfig)
	}

	// Apply task-specific env vars
	for k, v := range task.Env {
		os.Setenv(k, v)
	}
}

// configureMCPServers writes MCP server configuration for Claude Code.
// The config is a JSON object mapping server names to their configurations.
// Claude Code reads MCP servers from ~/.claude.json.
func configureMCPServers(configJSON string) {
	// Parse the MCP config
	var mcpServers map[string]any
	if err := json.Unmarshal([]byte(configJSON), &mcpServers); err != nil {
		log.Printf("warning: invalid ALCOVE_MCP_CONFIG: %v", err)
		return
	}

	// Build the Claude Code config structure
	claudeConfig := map[string]any{
		"mcpServers": mcpServers,
	}

	// Determine home directory
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = "/home/skiff"
	}

	// Write to ~/.claude.json
	configPath := filepath.Join(homeDir, ".claude.json")
	data, err := json.MarshalIndent(claudeConfig, "", "  ")
	if err != nil {
		log.Printf("warning: failed to marshal MCP config: %v", err)
		return
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		log.Printf("warning: failed to write MCP config to %s: %v", configPath, err)
		return
	}

	log.Printf("configured %d MCP server(s) at %s", len(mcpServers), configPath)

	// Also write settings to auto-approve MCP servers
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settingsPath), 0755)
	settings := map[string]any{
		"enableAllProjectMcpServers": true,
	}
	settingsData, _ := json.MarshalIndent(settings, "", "  ")
	os.WriteFile(settingsPath, settingsData, 0644)
}

// cloneRepo performs a shallow clone of the given repo.
func cloneRepo(repo, branch string) error {
	args := []string{"clone", "--depth=1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repo, "/workspace")

	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone %s: %w", repo, err)
	}

	return os.Chdir("/workspace")
}

// requireEnv returns the value of an environment variable or exits fatally.
func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

// envOrDefault returns the environment variable value or a default.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// setEnvIfMissing sets an environment variable only if it is not already set.
func setEnvIfMissing(key, value string) {
	if os.Getenv(key) == "" {
		os.Setenv(key, value)
	}
}

// parseDuration parses a duration string, returning the default on failure.
func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// truncate shortens a string to n characters.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func init() {
	// As PID 1, we need to reap zombie children.
	sigCh := make(chan os.Signal, 16)
	signal.Notify(sigCh, syscall.SIGCHLD)
	go func() {
		for range sigCh {
			for {
				var status syscall.WaitStatus
				pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
				if pid <= 0 || err != nil {
					break
				}
			}
		}
	}()

	// Forward termination signals to allow graceful shutdown.
	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-termCh
		log.Printf("received %v, shutting down", sig)
		os.Exit(128 + int(sig.(syscall.Signal)))
	}()
}
