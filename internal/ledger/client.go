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

// Package ledger provides an HTTP client for writing session data to the
// Ledger service (PostgreSQL-backed session storage).
package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bmbouter/alcove/internal"
)

// Client communicates with the Ledger HTTP API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string // session-scoped append-only token
}

// NewClient creates a Ledger client pointing at the given base URL.
// The token is used for authentication with the Ledger API.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		token: token,
	}
}

// CreateSession registers a new session record in Ledger.
func (c *Client) CreateSession(session internal.Session) error {
	return c.post("/api/v1/sessions", session)
}

// AppendTranscript appends transcript events to an existing session.
// Events are added in append-only fashion; existing events are never modified.
func (c *Client) AppendTranscript(sessionID string, events []json.RawMessage) error {
	path := fmt.Sprintf("/api/v1/sessions/%s/transcript", sessionID)
	payload := struct {
		Events []json.RawMessage `json:"events"`
	}{Events: events}
	return c.post(path, payload)
}

// UpdateSession updates the final status of a session (status, exit code, artifacts).
// Only the status, exit_code, and artifacts fields are mutable; all other fields
// are immutable after creation.
func (c *Client) UpdateSession(sessionID string, status string, exitCode *int, artifacts []internal.Artifact) error {
	path := fmt.Sprintf("/api/v1/sessions/%s/status", sessionID)
	payload := struct {
		Status    string              `json:"status"`
		ExitCode  *int                `json:"exit_code,omitempty"`
		Artifacts []internal.Artifact `json:"artifacts,omitempty"`
	}{
		Status:    status,
		ExitCode:  exitCode,
		Artifacts: artifacts,
	}
	return c.post(path, payload)
}

// post sends a JSON POST request to the Ledger API.
func (c *Client) post(path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("ledger: marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("ledger: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ledger: %s %s: %w", http.MethodPost, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("ledger: %s %s: HTTP %d", http.MethodPost, path, resp.StatusCode)
	}
	return nil
}
