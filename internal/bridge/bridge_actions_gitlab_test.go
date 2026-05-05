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

func TestDetectSCM_GitLab(t *testing.T) {
	inputs := map[string]interface{}{
		"project": "group/project",
		"tag":     "v1.0.0",
	}

	scm := detectSCM(inputs)
	if scm != "gitlab" {
		t.Errorf("Expected 'gitlab', got '%s'", scm)
	}
}

func TestDetectSCM_GitHub(t *testing.T) {
	inputs := map[string]interface{}{
		"repo": "owner/repo",
		"tag":  "v1.0.0",
	}

	scm := detectSCM(inputs)
	if scm != "github" {
		t.Errorf("Expected 'github', got '%s'", scm)
	}
}

func TestDetectSCM_Ambiguous(t *testing.T) {
	inputs := map[string]interface{}{
		"tag": "v1.0.0",
	}

	scm := detectSCM(inputs)
	if scm != "" {
		t.Errorf("Expected empty string, got '%s'", scm)
	}
}

func TestGitLabAwaitReleaseInputValidation(t *testing.T) {
	// Test missing project
	inputs := map[string]interface{}{
		"tag": "v1.0.0",
	}
	project := getStringInput(inputs, "project")
	tag := getStringInput(inputs, "tag")

	if project != "" {
		t.Errorf("Expected empty project, got '%s'", project)
	}
	if tag != "v1.0.0" {
		t.Errorf("Expected tag 'v1.0.0', got '%s'", tag)
	}

	// Test missing tag
	inputs = map[string]interface{}{
		"project": "group/project",
	}
	project = getStringInput(inputs, "project")
	tag = getStringInput(inputs, "tag")

	if project != "group/project" {
		t.Errorf("Expected project 'group/project', got '%s'", project)
	}
	if tag != "" {
		t.Errorf("Expected empty tag, got '%s'", tag)
	}
}

func TestUnifiedAwaitReleaseRegistration(t *testing.T) {
	actions := RegisterBridgeActions()

	// Check that the unified await-release action is registered
	if handler, exists := actions["await-release"]; !exists || handler == nil {
		t.Error("await-release action not registered")
	}

	// Check that the GitLab-specific alias is registered
	if handler, exists := actions["await-gl-release"]; !exists || handler == nil {
		t.Error("await-gl-release action not registered")
	}
}

func TestAwaitReleaseSchemaUpdated(t *testing.T) {
	schemas := ListBridgeActionSchemas()

	var awaitReleaseSchema *BridgeActionSchema
	var awaitGLReleaseSchema *BridgeActionSchema

	for _, schema := range schemas {
		if schema.Name == "await-release" {
			awaitReleaseSchema = &schema
		}
		if schema.Name == "await-gl-release" {
			awaitGLReleaseSchema = &schema
		}
	}

	// Check that await-release schema includes both GitHub and GitLab inputs
	if awaitReleaseSchema == nil {
		t.Error("await-release schema not found")
	} else {
		if _, hasRepo := awaitReleaseSchema.Inputs["repo"]; !hasRepo {
			t.Error("await-release schema missing 'repo' input")
		}
		if _, hasProject := awaitReleaseSchema.Inputs["project"]; !hasProject {
			t.Error("await-release schema missing 'project' input")
		}
	}

	// Check that await-gl-release schema exists
	if awaitGLReleaseSchema == nil {
		t.Error("await-gl-release schema not found")
	} else {
		if _, hasProject := awaitGLReleaseSchema.Inputs["project"]; !hasProject {
			t.Error("await-gl-release schema missing 'project' input")
		}
		if _, hasTag := awaitGLReleaseSchema.Inputs["tag"]; !hasTag {
			t.Error("await-gl-release schema missing 'tag' input")
		}
	}
}

func TestBridgeActionUnifiedAwaitRelease_DetectionLogic(t *testing.T) {
	// Test that the router correctly identifies SCM type
	// This tests the logic without needing credential store mocks

	// GitLab inputs (project key should route to GitLab)
	inputs := map[string]interface{}{
		"project": "group/project",
		"tag":     "v1.0.0",
	}
	scm := detectSCM(inputs)
	if scm != "gitlab" {
		t.Errorf("Expected GitLab detection for project input, got %s", scm)
	}

	// GitHub inputs (repo key should route to GitHub)
	inputs = map[string]interface{}{
		"repo": "owner/repo",
		"tag":  "v1.0.0",
	}
	scm = detectSCM(inputs)
	if scm != "github" {
		t.Errorf("Expected GitHub detection for repo input, got %s", scm)
	}

	// Ambiguous inputs (neither repo nor project)
	inputs = map[string]interface{}{
		"tag": "v1.0.0",
	}
	scm = detectSCM(inputs)
	if scm != "" {
		t.Errorf("Expected empty SCM detection for ambiguous inputs, got %s", scm)
	}
}
