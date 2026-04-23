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
//	GATE_SPLUNK_HOST       — Splunk API hostname for custom instances
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

	// Start periodic log flushing to Ledger (every 5 seconds).
	// Short interval ensures logs are captured even for fast-completing tasks.
	proxy.StartLogFlusher(5 * time.Second)

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

	// Flush remaining proxy logs synchronously before stopping the server.
	// This ensures logs are delivered even for short-lived tasks.
	entries := proxy.FlushLogs()
	if len(entries) > 0 {
		log.Printf("gate: final flush of %d proxy log entries", len(entries))
		proxy.SendLogs(entries)
	}

	// Stop the periodic log flusher goroutine.
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
	splunkHost := os.Getenv("GATE_SPLUNK_HOST")

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

	// Apply GATE_SPLUNK_HOST override for custom Splunk instances.
	if splunkHost != "" {
		if tc, ok := toolConfigs["splunk"]; ok {
			tc.APIHost = splunkHost
			toolConfigs["splunk"] = tc
		} else {
			toolConfigs["splunk"] = gate.ToolConfig{
				APIHost:    splunkHost,
				AuthHeader: "Authorization",
				AuthFormat: "bearer",
			}
		}
	}

	// MITM TLS interception: base64-encoded PEM CA cert and key.
	// When set, Gate performs MITM re-encrypt proxy for service domains,
	// enabling credential injection and scope enforcement on CONNECT tunnels.
	var caCertPEM, caKeyPEM []byte
	if certB64 := os.Getenv("GATE_CA_CERT_PEM"); certB64 != "" {
		decoded, err := gate.DecodePEMFromBase64(certB64)
		if err != nil {
			return gate.Config{}, fmt.Errorf("invalid GATE_CA_CERT_PEM: %w", err)
		}
		caCertPEM = decoded
	}
	if keyB64 := os.Getenv("GATE_CA_KEY_PEM"); keyB64 != "" {
		decoded, err := gate.DecodePEMFromBase64(keyB64)
		if err != nil {
			return gate.Config{}, fmt.Errorf("invalid GATE_CA_KEY_PEM: %w", err)
		}
		caKeyPEM = decoded
	}

	if len(caCertPEM) > 0 && len(caKeyPEM) > 0 {
		log.Printf("gate: MITM mode enabled for service domains")
	}

	enforcementMode := os.Getenv("GATE_ENFORCEMENT_MODE") // "monitor" or "" (enforce)
	if enforcementMode == "monitor" {
		log.Printf("gate: enforcement mode = monitor (log-only, all requests allowed)")
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
		CACertPEM:          caCertPEM,
		CAKeyPEM:           caKeyPEM,
		EnforcementMode:    enforcementMode,
	}, nil
}
