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

// Package hail provides a NATS client wrapper for Alcove messaging.
package hail

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/bmbouter/alcove/internal"
)

// Client wraps a NATS connection for Alcove-specific messaging patterns.
type Client struct {
	nc   *nats.Conn
	subs []*nats.Subscription
	mu   sync.Mutex
}

// Connect establishes a connection to the NATS server at the given URL.
func Connect(url string) (*Client, error) {
	nc, err := nats.Connect(url,
		nats.Name("skiff-init"),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("hail: connect to %s: %w", url, err)
	}
	return &Client{nc: nc}, nil
}

// PublishStatus sends a status update for the given task.
func (c *Client) PublishStatus(taskID string, status StatusUpdate) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("hail: marshal status: %w", err)
	}
	subject := fmt.Sprintf("tasks.%s.status", taskID)
	if err := c.nc.Publish(subject, data); err != nil {
		return fmt.Errorf("hail: publish %s: %w", subject, err)
	}
	return c.nc.Flush()
}

// PublishTranscript sends a transcript event for the given session.
// No Flush() — transcript events are high-frequency.
func (c *Client) PublishTranscript(sessionID string, event []byte) error {
	subject := fmt.Sprintf("tasks.%s.transcript", sessionID)
	return c.nc.Publish(subject, event)
}

// SubscribeCancel subscribes to the cancellation topic for a specific session.
// A message on the returned channel signals that the task should be cancelled.
// The sessionID must match the subject Bridge publishes to (tasks.<sessionID>.cancel).
func (c *Client) SubscribeCancel(sessionID string) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)
	subject := fmt.Sprintf("tasks.%s.cancel", sessionID)
	sub, err := c.nc.Subscribe(subject, func(msg *nats.Msg) {
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return nil, fmt.Errorf("hail: subscribe %s: %w", subject, err)
	}
	c.mu.Lock()
	c.subs = append(c.subs, sub)
	c.mu.Unlock()
	return ch, nil
}

// Close drains all subscriptions and closes the NATS connection.
func (c *Client) Close() {
	c.mu.Lock()
	subs := c.subs
	c.subs = nil
	c.mu.Unlock()

	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}
	if c.nc != nil {
		c.nc.Close()
	}
}

// StatusUpdate carries a task status change over NATS.
type StatusUpdate struct {
	TaskID    string              `json:"task_id"`
	SessionID string             `json:"session_id"`
	Status    string              `json:"status"` // accepted, running, completed, timeout, cancelled, error
	ExitCode  *int                `json:"exit_code,omitempty"`
	Artifacts []internal.Artifact `json:"artifacts,omitempty"`
	Message   string              `json:"message,omitempty"`
	Outputs   map[string]string   `json:"outputs,omitempty"`
}
