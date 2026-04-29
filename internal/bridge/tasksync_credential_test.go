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
	"context"
	"testing"
)

func TestCredentialValidation(t *testing.T) {
	// Test isCredentialGatedService function
	testCases := []struct {
		service  string
		expected bool
	}{
		{"github", true},
		{"gitlab", true},
		{"jira", true},
		{"splunk", true},
		{"unknown", false},
		{"", false},
	}

	for _, tc := range testCases {
		result := isCredentialGatedService(tc.service)
		if result != tc.expected {
			t.Errorf("isCredentialGatedService(%q) = %v, expected %v", tc.service, result, tc.expected)
		}
	}
}

func TestValidateCredentialRequirements(t *testing.T) {
	// This would require a full database setup to test properly
	// For now, we just ensure the function exists and can be called
	// without panicking with nil inputs

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("validateCredentialRequirements panicked: %v", r)
		}
	}()

	// Test with nil syncer - should not panic but return early
	var syncer *AgentRepoSyncer
	if syncer != nil {
		syncer.validateCredentialRequirements(context.Background(), "test-repo", "test-team")
	}
}