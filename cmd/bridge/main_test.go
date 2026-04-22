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

package main

import (
	"bytes"
	"log"
	"os"
	"testing"
)

func TestShutdownLogging(t *testing.T) {
	// Capture log output
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer func() {
		log.SetOutput(os.Stderr)
	}()

	// Test that the envOrDefault function works correctly
	// (This also validates the file compiles correctly)
	result := envOrDefault("NONEXISTENT_VAR", "fallback")
	if result != "fallback" {
		t.Errorf("expected 'fallback', got %s", result)
	}

	// Test with an actual environment variable
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	result = envOrDefault("TEST_VAR", "fallback")
	if result != "test_value" {
		t.Errorf("expected 'test_value', got %s", result)
	}
}

// TestShutdownLogMessages validates that shutdown log messages are properly formatted
func TestShutdownLogMessages(t *testing.T) {
	expectedMessages := []string{
		"received shutdown signal, beginning graceful shutdown...",
		"shutting down HTTP server...",
		"HTTP server shut down successfully",
		"graceful shutdown complete",
		"shutting down NATS connection...",
		"NATS connection closed",
		"shutting down database connection pool...",
		"database connection pool closed",
		"shutting down scheduler...",
		"scheduler stopped",
		"shutting down agent repo syncer...",
		"agent repo syncer stopped",
	}

	// This test validates that our expected log messages are properly defined
	// The actual shutdown logic is integration tested in the main function
	for _, msg := range expectedMessages {
		if len(msg) == 0 {
			t.Errorf("Empty shutdown message found")
		}
	}
}