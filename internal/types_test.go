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

package internal

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExecutableSpec_JSON(t *testing.T) {
	spec := ExecutableSpec{
		URL:  "https://example.com/agent",
		Args: []string{"--verbose", "--config", "/tmp/config.yaml"},
		Env:  map[string]string{"DEBUG": "1", "LOG_LEVEL": "info"},
	}

	// Test JSON marshaling
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	// Test JSON unmarshaling
	var decoded ExecutableSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Verify fields
	if decoded.URL != spec.URL {
		t.Errorf("URL mismatch: got %s, want %s", decoded.URL, spec.URL)
	}
	if len(decoded.Args) != len(spec.Args) {
		t.Errorf("Args length mismatch: got %d, want %d", len(decoded.Args), len(spec.Args))
	}
	if len(decoded.Env) != len(spec.Env) {
		t.Errorf("Env length mismatch: got %d, want %d", len(decoded.Env), len(spec.Env))
	}
}

func TestExecutableSpec_YAML(t *testing.T) {
	spec := ExecutableSpec{
		URL:  "file:///usr/local/bin/agent",
		Args: []string{"--mode", "prod"},
		Env:  map[string]string{"API_KEY": "test"},
	}

	// Test YAML marshaling
	data, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatalf("YAML marshal failed: %v", err)
	}

	// Test YAML unmarshaling
	var decoded ExecutableSpec
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("YAML unmarshal failed: %v", err)
	}

	// Verify fields
	if decoded.URL != spec.URL {
		t.Errorf("URL mismatch: got %s, want %s", decoded.URL, spec.URL)
	}
	if len(decoded.Args) != len(spec.Args) {
		t.Errorf("Args length mismatch: got %d, want %d", len(decoded.Args), len(spec.Args))
	}
	if len(decoded.Env) != len(spec.Env) {
		t.Errorf("Env length mismatch: got %d, want %d", len(decoded.Env), len(spec.Env))
	}
}

func TestExecutableSpec_MinimalFields(t *testing.T) {
	spec := ExecutableSpec{
		URL: "https://example.com/minimal-agent",
		// Args and Env are optional (omitempty)
	}

	// Test JSON with minimal fields
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	var decoded ExecutableSpec
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if decoded.URL != spec.URL {
		t.Errorf("URL mismatch: got %s, want %s", decoded.URL, spec.URL)
	}
	if decoded.Args != nil {
		t.Errorf("Args should be nil for omitempty, got %v", decoded.Args)
	}
	if decoded.Env != nil {
		t.Errorf("Env should be nil for omitempty, got %v", decoded.Env)
	}
}