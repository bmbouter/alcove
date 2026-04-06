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

// Command gate runs the Gate authorization proxy sidecar.
//
// Gate is deployed as a sidecar container alongside each Skiff pod.
// It intercepts all outbound HTTP/HTTPS traffic, enforces scope-based
// access control, injects real credentials (replacing session tokens),
// and logs all requests for audit.
//
// Configuration is via environment variables:
//
//	GATE_SESSION_ID    — the session ID for this task
//	GATE_SCOPE         — JSON-encoded Scope (allowed services/repos/operations)
//	GATE_CREDENTIALS   — JSON map of service name → real credential
//	GATE_SESSION_TOKEN — opaque token that the Skiff container presents
//	GATE_LLM_TOKEN         — bearer token or API key (falls back to GATE_LLM_API_KEY)
//	GATE_LLM_PROVIDER      — "anthropic" or "google-vertex"
//	GATE_LLM_TOKEN_TYPE    — "api_key" or "bearer" (default: "api_key")
//	GATE_TOKEN_REFRESH_URL — Bridge endpoint for token refresh
//	GATE_TOKEN_REFRESH_SECRET — session-scoped secret for refresh auth
//	GATE_VERTEX_REGION     — Vertex AI region (default: "us-east5")
//	GATE_VERTEX_PROJECT    — Vertex AI project ID
//	GATE_LEDGER_URL        — URL to send proxy logs to
//	GATE_GITLAB_HOST       — self-hosted GitLab hostname (default: gitlab.com)
//	GATE_TOOL_CONFIGS      — JSON map of tool name → proxy config (api_host, auth_header, auth_format)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bmbouter/alcove/internal/gate"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("gate: configuration error: %v", err)
	}

	proxy := gate.NewProxy(cfg)

	if cfg.LedgerURL != "" {
		log.Printf("gate: proxy log target: %s/api/v1/sessions/%s/proxy-log", cfg.LedgerURL, cfg.SessionID)
	} else {
		log.Printf("gate: WARNING — GATE_LEDGER_URL is empty, proxy logs will NOT be sent")
	}

	// Start periodic log flushing to Ledger (every 30 seconds)
	proxy.StartLogFlusher(30 * time.Second)

	server := &http.Server{
		Addr:         ":8443",
		Handler:      proxy.Handler(),
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	// Graceful shutdown on SIGTERM/SIGINT
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		log.Printf("gate: starting proxy on :8443 (session=%s, provider=%s)", cfg.SessionID, cfg.LLMProvider)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("gate: server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("gate: shutting down...")

	// Stop the log flusher (triggers final flush)
	proxy.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("gate: shutdown error: %v", err)
	}

	log.Println("gate: stopped")
}

// loadConfig reads Gate configuration from environment variables.
func loadConfig() (gate.Config, error) {
	sessionID := os.Getenv("GATE_SESSION_ID")
	if sessionID == "" {
		return gate.Config{}, fmt.Errorf("GATE_SESSION_ID is required")
	}

	scopeJSON := os.Getenv("GATE_SCOPE")
	if scopeJSON == "" {
		return gate.Config{}, fmt.Errorf("GATE_SCOPE is required")
	}

	scope, err := gate.ParseScope(scopeJSON)
	if err != nil {
		return gate.Config{}, fmt.Errorf("invalid GATE_SCOPE: %w", err)
	}

	credJSON := os.Getenv("GATE_CREDENTIALS")
	credentials := make(map[string]string)
	if credJSON != "" {
		if err := json.Unmarshal([]byte(credJSON), &credentials); err != nil {
			return gate.Config{}, fmt.Errorf("invalid GATE_CREDENTIALS: %w", err)
		}
	}

	sessionToken := os.Getenv("GATE_SESSION_TOKEN")
	llmToken := os.Getenv("GATE_LLM_TOKEN")
	if llmToken == "" {
		llmToken = os.Getenv("GATE_LLM_API_KEY") // legacy fallback
	}
	if llmToken == "" {
		log.Println("WARNING: GATE_LLM_TOKEN is not set — LLM API proxying will fail. Ensure Bridge is configured with provider credentials.")
	}
	llmTokenType := os.Getenv("GATE_LLM_TOKEN_TYPE")
	if llmTokenType == "" {
		llmTokenType = "api_key"
	}
	tokenRefreshURL := os.Getenv("GATE_TOKEN_REFRESH_URL")
	tokenRefreshSecret := os.Getenv("GATE_TOKEN_REFRESH_SECRET")
	llmProvider := os.Getenv("GATE_LLM_PROVIDER")
	if llmProvider == "" {
		llmProvider = "anthropic"
	}

	vertexRegion := os.Getenv("GATE_VERTEX_REGION")
	if vertexRegion == "" {
		vertexRegion = "us-east5"
	}

	vertexProject := os.Getenv("GATE_VERTEX_PROJECT")

	ledgerURL := os.Getenv("GATE_LEDGER_URL")

	gitlabHost := os.Getenv("GATE_GITLAB_HOST")

	var toolConfigs map[string]gate.ToolConfig
	if tcJSON := os.Getenv("GATE_TOOL_CONFIGS"); tcJSON != "" {
		if err := json.Unmarshal([]byte(tcJSON), &toolConfigs); err != nil {
			log.Printf("warning: invalid GATE_TOOL_CONFIGS: %v", err)
			toolConfigs = nil
		}
	}
	if toolConfigs == nil {
		toolConfigs = make(map[string]gate.ToolConfig)
	}

	return gate.Config{
		SessionID:          sessionID,
		Scope:              scope,
		Credentials:        credentials,
		ToolConfigs:        toolConfigs,
		SessionToken:       sessionToken,
		LLMToken:           llmToken,
		LLMProvider:        llmProvider,
		LLMTokenType:       llmTokenType,
		TokenRefreshURL:    tokenRefreshURL,
		TokenRefreshSecret: tokenRefreshSecret,
		VertexRegion:       vertexRegion,
		VertexProject:      vertexProject,
		LedgerURL:          ledgerURL,
		GitLabHost:         gitlabHost,
	}, nil
}
