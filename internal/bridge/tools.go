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
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ToolDefinition represents an MCP tool in the registry.
type ToolDefinition struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	DisplayName string          `json:"display_name"`
	ToolType    string          `json:"tool_type"`              // "builtin" or "custom"
	MCPCommand  string          `json:"mcp_command,omitempty"`
	MCPArgs     json.RawMessage `json:"mcp_args,omitempty"`
	APIHost     string          `json:"api_host,omitempty"`
	AuthHeader  string          `json:"auth_header,omitempty"`
	AuthFormat  string          `json:"auth_format,omitempty"`
	Operations  json.RawMessage `json:"operations"`             // [{name, description, risk}]
	TeamID      string          `json:"team_id,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ToolOperation defines a single operation a tool supports.
type ToolOperation struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"` // "read", "write", "danger"
}

// ToolStore manages the MCP tool registry.
type ToolStore struct {
	db *pgxpool.Pool
}

// NewToolStore creates a ToolStore with the given database pool.
func NewToolStore(db *pgxpool.Pool) *ToolStore {
	return &ToolStore{db: db}
}

// CreateTool inserts a new custom tool into the registry.
func (ts *ToolStore) CreateTool(ctx context.Context, tool *ToolDefinition, teamID string) error {
	if tool.ID == "" {
		tool.ID = uuid.New().String()
	}
	tool.CreatedAt = time.Now().UTC()
	tool.TeamID = teamID
	tool.ToolType = "custom"

	if tool.MCPArgs == nil {
		tool.MCPArgs = json.RawMessage(`[]`)
	}
	if tool.Operations == nil {
		tool.Operations = json.RawMessage(`[]`)
	}
	if tool.AuthHeader == "" {
		tool.AuthHeader = "Authorization"
	}
	if tool.AuthFormat == "" {
		tool.AuthFormat = "bearer"
	}

	_, err := ts.db.Exec(ctx,
		`INSERT INTO mcp_tools (id, name, display_name, tool_type, mcp_command, mcp_args, api_host, auth_header, auth_format, operations, team_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		tool.ID, tool.Name, tool.DisplayName, tool.ToolType,
		tool.MCPCommand, string(tool.MCPArgs), tool.APIHost,
		tool.AuthHeader, tool.AuthFormat, string(tool.Operations),
		tool.TeamID, tool.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting tool: %w", err)
	}
	return nil
}

// ListTools returns ALL builtin tools plus the given owner's custom tools.
func (ts *ToolStore) ListTools(ctx context.Context, teamID string) ([]ToolDefinition, error) {
	var query string
	var args []any
	if teamID != "" {
		query = `SELECT id, name, display_name, tool_type, mcp_command, mcp_args, api_host, auth_header, auth_format, operations, team_id, created_at
			FROM mcp_tools
			WHERE tool_type = 'builtin' OR team_id = $1
			ORDER BY tool_type ASC, name ASC`
		args = []any{teamID}
	} else {
		query = `SELECT id, name, display_name, tool_type, mcp_command, mcp_args, api_host, auth_header, auth_format, operations, team_id, created_at
			FROM mcp_tools
			WHERE tool_type = 'builtin'
			ORDER BY tool_type ASC, name ASC`
	}

	rows, err := ts.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying tools: %w", err)
	}
	defer rows.Close()

	var tools []ToolDefinition
	for rows.Next() {
		var t ToolDefinition
		var mcpCommand, apiHost, authHeader, authFormat, teamID *string
		var mcpArgs, operations string

		if err := rows.Scan(&t.ID, &t.Name, &t.DisplayName, &t.ToolType,
			&mcpCommand, &mcpArgs, &apiHost, &authHeader, &authFormat,
			&operations, &teamID, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning tool: %w", err)
		}

		if teamID != nil {
			t.TeamID = *teamID
		}
		if mcpCommand != nil {
			t.MCPCommand = *mcpCommand
		}
		t.MCPArgs = json.RawMessage(mcpArgs)
		if apiHost != nil {
			t.APIHost = *apiHost
		}
		if authHeader != nil {
			t.AuthHeader = *authHeader
		}
		if authFormat != nil {
			t.AuthFormat = *authFormat
		}
		t.Operations = json.RawMessage(operations)

		tools = append(tools, t)
	}

	if tools == nil {
		tools = []ToolDefinition{}
	}

	return tools, rows.Err()
}

// GetTool looks up a tool by name. Returns builtin tools regardless of owner;
// returns custom tools only if the owner matches.
func (ts *ToolStore) GetTool(ctx context.Context, name, teamID string) (*ToolDefinition, error) {
	var query string
	var args []any
	if teamID != "" {
		query = `SELECT id, name, display_name, tool_type, mcp_command, mcp_args, api_host, auth_header, auth_format, operations, team_id, created_at
			FROM mcp_tools
			WHERE name = $1 AND (tool_type = 'builtin' OR team_id = $2)
			LIMIT 1`
		args = []any{name, teamID}
	} else {
		query = `SELECT id, name, display_name, tool_type, mcp_command, mcp_args, api_host, auth_header, auth_format, operations, team_id, created_at
			FROM mcp_tools
			WHERE name = $1 AND tool_type = 'builtin'
			LIMIT 1`
		args = []any{name}
	}

	var t ToolDefinition
	var mcpCommand, apiHost, authHeader, authFormat, scannedTeamID *string
	var mcpArgs, operations string

	err := ts.db.QueryRow(ctx, query, args...).Scan(
		&t.ID, &t.Name, &t.DisplayName, &t.ToolType,
		&mcpCommand, &mcpArgs, &apiHost, &authHeader, &authFormat,
		&operations, &scannedTeamID, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("tool %q not found: %w", name, err)
	}

	if scannedTeamID != nil {
		t.TeamID = *scannedTeamID
	}
	if mcpCommand != nil {
		t.MCPCommand = *mcpCommand
	}
	t.MCPArgs = json.RawMessage(mcpArgs)
	if apiHost != nil {
		t.APIHost = *apiHost
	}
	if authHeader != nil {
		t.AuthHeader = *authHeader
	}
	if authFormat != nil {
		t.AuthFormat = *authFormat
	}
	t.Operations = json.RawMessage(operations)

	return &t, nil
}

// UpdateTool updates an existing custom tool. Builtin tools cannot be updated.
func (ts *ToolStore) UpdateTool(ctx context.Context, tool *ToolDefinition, teamID string) error {
	if tool.MCPArgs == nil {
		tool.MCPArgs = json.RawMessage(`[]`)
	}
	if tool.Operations == nil {
		tool.Operations = json.RawMessage(`[]`)
	}

	result, err := ts.db.Exec(ctx,
		`UPDATE mcp_tools
		SET display_name = $1, mcp_command = $2, mcp_args = $3, api_host = $4,
		    auth_header = $5, auth_format = $6, operations = $7
		WHERE name = $8 AND tool_type = 'custom' AND team_id = $9`,
		tool.DisplayName, tool.MCPCommand, string(tool.MCPArgs), tool.APIHost,
		tool.AuthHeader, tool.AuthFormat, string(tool.Operations),
		tool.Name, teamID)
	if err != nil {
		return fmt.Errorf("updating tool: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("tool %q not found or is a builtin tool", tool.Name)
	}
	return nil
}

// DeleteTool removes a custom tool from the registry. Builtin tools cannot be deleted.
func (ts *ToolStore) DeleteTool(ctx context.Context, name, teamID string) error {
	result, err := ts.db.Exec(ctx,
		`DELETE FROM mcp_tools WHERE name = $1 AND tool_type = 'custom' AND team_id = $2`,
		name, teamID)
	if err != nil {
		return fmt.Errorf("deleting tool: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("tool %q not found or is a builtin tool", name)
	}
	return nil
}

// SeedBuiltinTools creates or updates the GitHub and GitLab builtin tools.
// Uses INSERT ... ON CONFLICT DO UPDATE for idempotent seeding.
func (ts *ToolStore) SeedBuiltinTools(ctx context.Context) error {
	githubOps, err := json.Marshal([]ToolOperation{
		{Name: "clone", Description: "Clone repository", Risk: "read"},
		{Name: "read_prs", Description: "Read pull requests", Risk: "read"},
		{Name: "read_issues", Description: "Read issues", Risk: "read"},
		{Name: "read_contents", Description: "Read repository contents", Risk: "read"},
		{Name: "read_actions", Description: "Read CI/CD pipelines", Risk: "read"},
		{Name: "push_branch", Description: "Push to non-default branch", Risk: "write"},
		{Name: "create_pr_draft", Description: "Create draft pull request", Risk: "write"},
		{Name: "create_pr", Description: "Create pull request", Risk: "write"},
		{Name: "create_comment", Description: "Comment on issues/PRs", Risk: "write"},
		{Name: "merge_pr", Description: "Merge pull request", Risk: "danger"},
		{Name: "push_main", Description: "Push to default branch", Risk: "danger"},
		{Name: "delete_branch", Description: "Delete branch", Risk: "danger"},
	})
	if err != nil {
		return fmt.Errorf("marshaling github operations: %w", err)
	}

	gitlabOps, err := json.Marshal([]ToolOperation{
		{Name: "clone", Description: "Clone repository", Risk: "read"},
		{Name: "read_mrs", Description: "Read merge requests", Risk: "read"},
		{Name: "read_issues", Description: "Read issues", Risk: "read"},
		{Name: "read_contents", Description: "Read repository contents", Risk: "read"},
		{Name: "read_pipelines", Description: "Read CI/CD pipelines", Risk: "read"},
		{Name: "push_branch", Description: "Push to non-default branch", Risk: "write"},
		{Name: "create_mr_draft", Description: "Create draft merge request", Risk: "write"},
		{Name: "create_mr", Description: "Create merge request", Risk: "write"},
		{Name: "create_comment", Description: "Comment on issues/MRs", Risk: "write"},
		{Name: "merge_mr", Description: "Merge merge request", Risk: "danger"},
		{Name: "push_main", Description: "Push to default branch", Risk: "danger"},
		{Name: "delete_branch", Description: "Delete branch", Risk: "danger"},
	})
	if err != nil {
		return fmt.Errorf("marshaling gitlab operations: %w", err)
	}

	jiraOps, err := json.Marshal([]ToolOperation{
		{Name: "read_issues", Description: "Read issues", Risk: "read"},
		{Name: "search_issues", Description: "Search issues with JQL", Risk: "read"},
		{Name: "read_comments", Description: "Read issue comments", Risk: "read"},
		{Name: "read_projects", Description: "Read projects", Risk: "read"},
		{Name: "read_boards", Description: "Read agile boards", Risk: "read"},
		{Name: "read_sprints", Description: "Read sprints", Risk: "read"},
		{Name: "read_metadata", Description: "Read issue types, priorities, statuses, fields", Risk: "read"},
		{Name: "create_issue", Description: "Create new issue", Risk: "write"},
		{Name: "update_issue", Description: "Update existing issue", Risk: "write"},
		{Name: "add_comment", Description: "Add comment to issue", Risk: "write"},
		{Name: "transition_issue", Description: "Transition issue status", Risk: "write"},
		{Name: "delete_issue", Description: "Delete issue", Risk: "danger"},
	})
	if err != nil {
		return fmt.Errorf("marshaling jira operations: %w", err)
	}

	builtins := []struct {
		name        string
		displayName string
		mcpCommand  string
		mcpArgs     string
		apiHost     string
		authHeader  string
		authFormat  string
		operations  string
	}{
		{
			name:        "github",
			displayName: "GitHub",
			mcpCommand:  "github-mcp-server",
			mcpArgs:     `[]`,
			apiHost:     "api.github.com",
			authHeader:  "Authorization",
			authFormat:  "bearer",
			operations:  string(githubOps),
		},
		{
			name:        "gitlab",
			displayName: "GitLab",
			mcpCommand:  "npx -y @gitlab-org/gitlab-mcp-server",
			mcpArgs:     `[]`,
			apiHost:     "gitlab.com",
			authHeader:  "PRIVATE-TOKEN",
			authFormat:  "header",
			operations:  string(gitlabOps),
		},
		{
			name:        "jira",
			displayName: "Jira",
			mcpCommand:  "",
			mcpArgs:     `[]`,
			apiHost:     "",
			authHeader:  "Authorization",
			authFormat:  "basic",
			operations:  string(jiraOps),
		},
	}

	for _, b := range builtins {
		id := uuid.New().String()
		_, err := ts.db.Exec(ctx,
			`INSERT INTO mcp_tools (id, name, display_name, tool_type, mcp_command, mcp_args, api_host, auth_header, auth_format, operations, team_id, created_at)
			VALUES ($1, $2, $3, 'builtin', $4, $5, $6, $7, $8, $9, NULL, NOW())
			ON CONFLICT (name) WHERE team_id IS NULL DO UPDATE SET
				display_name = EXCLUDED.display_name,
				mcp_command = EXCLUDED.mcp_command,
				mcp_args = EXCLUDED.mcp_args,
				api_host = EXCLUDED.api_host,
				auth_header = EXCLUDED.auth_header,
				auth_format = EXCLUDED.auth_format,
				operations = EXCLUDED.operations`,
			id, b.name, b.displayName, b.mcpCommand, b.mcpArgs,
			b.apiHost, b.authHeader, b.authFormat, b.operations)
		if err != nil {
			return fmt.Errorf("seeding builtin tool %q: %w", b.name, err)
		}
	}

	return nil
}
