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
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrphanedWorkflowCleanup tests the scenario where repo URLs change
// and orphaned workflows need to be cleaned up even after agents are gone.
func TestOrphanedWorkflowCleanup(t *testing.T) {
	ctx := context.Background()

	// Set up test database
	db := setupTestDB(t)
	defer teardownTestDB(t, db)

	// Create test team and user
	teamID := createTestTeam(t, db, "test-team", false)
	username := "testuser"
	createTestUser(t, db, username, teamID)

	// Create stores
	defStore := NewAgentDefStore(db)
	workflowStore := NewWorkflowStore(db)
	profileStore := NewProfileStore(db)
	policyRuleStore := NewPolicyRuleStore(db)
	repoGroupStore := NewRepoGroupStore(db)
	settingsStore := NewSettingsStore(db)
	scheduler := &MockScheduler{}
	dispatcher := &MockDispatcher{}

	syncer := NewAgentRepoSyncer(db, settingsStore, scheduler, defStore, dispatcher, profileStore, policyRuleStore, workflowStore, repoGroupStore)

	// Step 1: Configure initial repo and simulate sync creating resources
	oldRepoURL := "https://github.com/old-org/repo"
	newRepoURL := "https://github.com/new-org/repo"

	// Add initial repo configuration
	repos := []SkillRepo{{URL: oldRepoURL, Enabled: true}}
	reposJSON, _ := json.Marshal(repos)
	_, err := db.Exec(ctx, `
		INSERT INTO team_settings (team_id, key, value)
		VALUES ($1, 'agent_repos', $2)
		ON CONFLICT (team_id, key) DO UPDATE SET value = EXCLUDED.value
	`, teamID, reposJSON)
	require.NoError(t, err)

	// Simulate resources that would be created by sync
	// Create an agent definition
	agentDef := &AgentDefinition{
		ID:         uuid.New().String(),
		Name:       "test-agent",
		SourceRepo: oldRepoURL,
		SourceFile: "test-agent.yml",
		SourceKey:  username + "::" + oldRepoURL + "::test-agent.yml",
		TeamID:     teamID,
		Prompt:     "Test agent prompt",
	}
	require.NoError(t, defStore.UpsertAgentDefinition(ctx, agentDef))

	// Create a workflow 
	workflowDef := &WorkflowDefinition{
		Name:       "test-workflow",
		SourceRepo: oldRepoURL,
		SourceFile: "test-workflow.yml",
		TeamID:     teamID,
		Workflow:   []WorkflowStep{{ID: "step1", Agent: "test-agent"}},
	}
	sourceKey := username + "::" + oldRepoURL + "::test-workflow.yml"
	require.NoError(t, workflowStore.UpsertWorkflow(ctx, workflowDef, sourceKey, "raw yaml", ""))

	// Create a security profile
	profile := &SecurityProfile{
		ID:         uuid.New().String(),
		Name:       "test-profile",
		Source:     "yaml",
		SourceRepo: oldRepoURL,
		SourceKey:  username + "::" + oldRepoURL + "::security-profiles/test-profile.yml",
		TeamID:     teamID,
		Tools:      make(map[string]ProfileToolConfig),
	}
	require.NoError(t, profileStore.UpsertYAMLProfile(ctx, profile))

	// Verify resources exist
	workflows, err := workflowStore.ListWorkflowsByRepo(ctx, oldRepoURL, teamID)
	require.NoError(t, err)
	assert.Len(t, workflows, 1)

	profiles, err := profileStore.ListProfiles(ctx, teamID)
	require.NoError(t, err)
	assert.Len(t, profiles, 1)

	agents, err := defStore.ListAgentDefinitionsByRepo(ctx, oldRepoURL, teamID)
	require.NoError(t, err)
	assert.Len(t, agents, 1)

	// Step 2: Change repo URL configuration to simulate org rename
	repos = []SkillRepo{{URL: newRepoURL, Enabled: true}}
	reposJSON, _ = json.Marshal(repos)
	_, err = db.Exec(ctx, `
		UPDATE team_settings SET value = $1
		WHERE team_id = $2 AND key = 'agent_repos'
	`, reposJSON, teamID)
	require.NoError(t, err)

	// Step 3: Simulate first sync cycle - agents get cleaned up
	// This simulates what would happen when SyncAll runs and doesn't find
	// the old repo URL anymore
	require.NoError(t, defStore.DeleteAgentDefinitionsByRepo(ctx, oldRepoURL, teamID))

	// Verify agents are gone but orphaned resources remain
	agents, err = defStore.ListAgentDefinitionsByRepo(ctx, oldRepoURL, teamID)
	require.NoError(t, err)
	assert.Len(t, agents, 0, "agents should be cleaned up")

	workflows, err = workflowStore.ListWorkflowsByRepo(ctx, oldRepoURL, teamID)
	require.NoError(t, err)
	assert.Len(t, workflows, 1, "workflows should still exist (orphaned)")

	profiles, err = profileStore.ListProfiles(ctx, teamID)
	require.NoError(t, err)
	assert.Len(t, profiles, 1, "profiles should still exist (orphaned)")

	// Step 4: Run enhanced cleanup logic (simulating next sync cycle)
	// This is the key test - the enhanced logic should find orphaned workflows
	// even though there are no agent definitions left to discover the source repo
	require.NoError(t, syncer.SyncAll(ctx))

	// Step 5: Verify all orphaned resources are now cleaned up
	workflows, err = workflowStore.ListWorkflowsByRepo(ctx, oldRepoURL, teamID)
	require.NoError(t, err)
	assert.Len(t, workflows, 0, "orphaned workflows should be cleaned up")

	profiles, err = profileStore.ListProfiles(ctx, teamID)
	require.NoError(t, err)
	assert.Len(t, profiles, 0, "orphaned profiles should be cleaned up")

	// Verify no resources exist for the old repo URL
	agentRepos, err := defStore.ListDistinctSourceRepos(ctx, teamID)
	require.NoError(t, err)
	assert.NotContains(t, agentRepos, oldRepoURL)

	workflowRepos, err := workflowStore.ListDistinctSourceRepos(ctx, teamID)
	require.NoError(t, err)
	assert.NotContains(t, workflowRepos, oldRepoURL)

	profileRepos, err := profileStore.ListDistinctSourceRepos(ctx, teamID)
	require.NoError(t, err)
	assert.NotContains(t, profileRepos, oldRepoURL)
}

// MockScheduler implements Scheduler interface for testing
type MockScheduler struct{}

func (m *MockScheduler) Start(ctx context.Context) error { return nil }
func (m *MockScheduler) Stop() error                     { return nil }

// MockDispatcher implements Dispatcher interface for testing  
type MockDispatcher struct{}

func (m *MockDispatcher) Start(ctx context.Context) error { return nil }
func (m *MockDispatcher) Stop() error                     { return nil }

// Helper functions for test setup
func setupTestDB(t *testing.T) *pgxpool.Pool {
	// This would connect to a test database
	// For this test to actually run, you'd need test database setup
	// This is a placeholder - in real testing you'd use testcontainers or similar
	t.Skip("Integration test requires test database setup")
	return nil
}

func teardownTestDB(t *testing.T, db *pgxpool.Pool) {
	if db != nil {
		db.Close()
	}
}

func createTestTeam(t *testing.T, db *pgxpool.Pool, name string, isPersonal bool) string {
	teamID := uuid.New().String()
	_, err := db.Exec(context.Background(),
		`INSERT INTO teams (id, name, is_personal) VALUES ($1, $2, $3)`,
		teamID, name, isPersonal)
	require.NoError(t, err)
	return teamID
}

func createTestUser(t *testing.T, db *pgxpool.Pool, username, teamID string) {
	_, err := db.Exec(context.Background(),
		`INSERT INTO team_members (team_id, username) VALUES ($1, $2)`,
		teamID, username)
	require.NoError(t, err)
}