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

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
)

// BridgeLLM provides LLM capabilities for Bridge system features.
// It calls the LLM API directly (not through Gate).
type BridgeLLM struct {
	provider           string // "anthropic", "google-vertex", or "claude-oauth"
	model              string
	apiKey             string
	region             string
	project            string
	serviceAccountJSON string
	credStore          *CredentialStore // for acquiring OAuth2 tokens from stored credentials
}

// NewBridgeLLM creates a BridgeLLM client from config.
// Returns nil if no system LLM is configured (features will be disabled).
func NewBridgeLLM(cfg *Config, credStore *CredentialStore) *BridgeLLM {
	eff := ResolveEffectiveLLM(cfg)

	if !eff.Configured {
		return nil
	}

	provider := eff.Provider

	apiKey := cfg.SystemLLM.APIKey
	if provider == "claude-oauth" {
		apiKey = cfg.SystemLLM.OAuthToken
	}

	if provider == "anthropic" && apiKey == "" {
		return nil
	}
	if provider == "claude-oauth" && apiKey == "" {
		return nil
	}
	if provider == "google-vertex" && eff.ProjectID == "" {
		return nil
	}

	return &BridgeLLM{
		provider:           provider,
		apiKey:             apiKey,
		model:              eff.Model,
		region:             eff.Region,
		project:            eff.ProjectID,
		serviceAccountJSON: cfg.SystemLLM.ServiceAccountJSON,
		credStore:          credStore,
	}
}

// Available returns true if the system LLM is configured and usable.
func (l *BridgeLLM) Available() bool {
	return l != nil
}

// Complete sends a prompt to the LLM and returns the text response.
// maxTokens limits output length. Returns error if system LLM is not configured.
func (l *BridgeLLM) Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	if l == nil {
		return "", fmt.Errorf("system LLM not configured")
	}

	switch l.provider {
	case "anthropic":
		return l.completeAnthropic(ctx, systemPrompt, userPrompt, maxTokens)
	case "claude-oauth":
		return l.completeClaudeOAuth(ctx, systemPrompt, userPrompt, maxTokens)
	case "google-vertex":
		return l.completeVertex(ctx, systemPrompt, userPrompt, maxTokens)
	default:
		return "", fmt.Errorf("unsupported LLM provider: %s", l.provider)
	}
}

func (l *BridgeLLM) completeAnthropic(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	body := map[string]any{
		"model":      l.model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages":   []map[string]string{{"role": "user", "content": userPrompt}},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", l.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading LLM response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing LLM response: %w", err)
	}

	for _, c := range result.Content {
		if c.Type == "text" {
			log.Printf("system LLM call: model=%s prompt_len=%d response_len=%d", l.model, len(userPrompt), len(c.Text))
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in LLM response")
}

func (l *BridgeLLM) completeClaudeOAuth(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	body := map[string]any{
		"model":      l.model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages":   []map[string]string{{"role": "user", "content": userPrompt}},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", l.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20,claude-code-20250219")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading LLM response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing LLM response: %w", err)
	}

	for _, c := range result.Content {
		if c.Type == "text" {
			log.Printf("system LLM call (claude-oauth): model=%s prompt_len=%d response_len=%d", l.model, len(userPrompt), len(c.Text))
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in LLM response")
}

func (l *BridgeLLM) completeVertex(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error) {
	// Convert model name: claude-sonnet-4-20250514 → claude-sonnet-4@20250514
	model := l.model
	if idx := strings.LastIndex(model, "-20"); idx > 0 && !strings.Contains(model, "@") {
		model = model[:idx] + "@" + model[idx+1:]
	}

	url := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:rawPredict",
		l.region, l.project, l.region, model)

	body := map[string]any{
		"anthropic_version": "vertex-2023-10-16",
		"max_tokens":        maxTokens,
		"system":            systemPrompt,
		"messages":          []map[string]string{{"role": "user", "content": userPrompt}},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	// Get OAuth2 token: prefer inline service account JSON, then credential store, then ADC.
	var tokenResult *TokenResult
	if l.serviceAccountJSON != "" {
		creds, credErr := google.CredentialsFromJSON(ctx, []byte(l.serviceAccountJSON), "https://www.googleapis.com/auth/cloud-platform")
		if credErr != nil {
			return "", fmt.Errorf("parsing service account JSON: %w", credErr)
		}
		tok, tokErr := creds.TokenSource.Token()
		if tokErr != nil {
			return "", fmt.Errorf("getting OAuth2 token from service account: %w", tokErr)
		}
		tokenResult = &TokenResult{Token: tok.AccessToken, TokenType: "bearer"}
	}
	if tokenResult == nil {
		tokenResult, err = l.credStore.AcquireSystemToken(ctx, "google-vertex")
	}
	if tokenResult == nil {
		tokenResult, err = l.credStore.AcquireSystemToken(ctx, "_system_llm")
	}
	if tokenResult == nil {
		tokenResult, err = l.credStore.AcquireToken(ctx, "google-vertex")
	}
	if tokenResult == nil {
		// Fallback: try google default credentials from environment
		creds, credErr := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if credErr != nil {
			return "", fmt.Errorf("no Vertex credential available and no default credentials (%v)", credErr)
		}
		tok, tokErr := creds.TokenSource.Token()
		if tokErr != nil {
			return "", fmt.Errorf("getting OAuth2 token: %w", tokErr)
		}
		tokenResult = &TokenResult{Token: tok.AccessToken, TokenType: "bearer"}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenResult.Token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading LLM response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing LLM response: %w", err)
	}

	for _, c := range result.Content {
		if c.Type == "text" {
			log.Printf("system LLM call (vertex): model=%s prompt_len=%d response_len=%d", l.model, len(userPrompt), len(c.Text))
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in LLM response")
}
