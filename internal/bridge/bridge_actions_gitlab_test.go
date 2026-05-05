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
	"strings"
	"testing"
)

func TestBridgeActionCreateMRsInputValidation(t *testing.T) {
	tests := []struct {
		name    string
		inputs  map[string]interface{}
		wantErr bool
		errMsg  string
	}{
		{
			name: "missing required inputs",
			inputs: map[string]interface{}{
				"projects": []string{"group/project1"},
			},
			wantErr: true,
			errMsg:  "missing required inputs: source_branch, title",
		},
		{
			name: "missing projects",
			inputs: map[string]interface{}{
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			wantErr: true,
			errMsg:  "missing required input: projects (array of GitLab project paths)",
		},
		{
			name: "empty projects array",
			inputs: map[string]interface{}{
				"projects":      []string{},
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			wantErr: true,
			errMsg:  "missing required input: projects (array of GitLab project paths)",
		},
		{
			name: "valid inputs with defaults",
			inputs: map[string]interface{}{
				"projects":      []string{"group/project1", "group/project2"},
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			wantErr: false,
		},
		{
			name: "valid inputs with all options",
			inputs: map[string]interface{}{
				"projects":      []string{"group/project1"},
				"source_branch": "feature-branch",
				"target_branch": "develop",
				"title":         "Test MR",
				"description":   "Test description",
				"draft":         true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			credStore := &CredentialStore{}

			result, err := bridgeActionCreateMRs(ctx, tt.inputs, credStore, "team1")

			if tt.wantErr {
				if err == nil && result.Status != "failed" {
					t.Errorf("expected error, got success")
				}
				if result != nil && result.Status == "failed" && !strings.Contains(result.Error, tt.errMsg) {
					t.Errorf("expected error message containing '%s', got: %s", tt.errMsg, result.Error)
				}
			} else {
				// For valid inputs, we expect the function to reach the credential acquisition step
				// and fail there (which is expected since we don't have real credentials)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if result == nil {
					t.Error("expected result, got nil")
				}
				// The function should fail at the credential acquisition step
				if result.Status != "failed" || !strings.Contains(result.Error, "failed to acquire GitLab token") {
					t.Errorf("expected credential failure, got: %s - %s", result.Status, result.Error)
				}
			}
		})
	}
}

func TestValidBridgeActionsIncludesCreateMRs(t *testing.T) {
	// Test that create-mrs is included in validBridgeActions
	if !validBridgeActions["create-mrs"] {
		t.Error("create-mrs not found in validBridgeActions")
	}

	// Also test that create-prs is now included (fixing the pre-existing gap)
	if !validBridgeActions["create-prs"] {
		t.Error("create-prs not found in validBridgeActions")
	}
}

func TestBridgeActionRegistration(t *testing.T) {
	// Test that all new actions are properly registered
	actions := RegisterBridgeActions()

	if _, exists := actions["create-mrs"]; !exists {
		t.Error("create-mrs action not registered")
	}

	if _, exists := actions["create-prs"]; !exists {
		t.Error("create-prs action not registered")
	}
}