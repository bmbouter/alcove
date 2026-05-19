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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// agentDefinition represents an agent definition from GET /api/v1/agent-definitions.
type agentDefinition struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Repos        []agentRepoSpec `json:"repos,omitempty"`
	DevContainer json.RawMessage `json:"dev_container,omitempty"`
	SourceRepo   string          `json:"source_repo"`
	SourceFile   string          `json:"source_file"`
	SyncError    string          `json:"sync_error,omitempty"`
	SyncWarning  string          `json:"sync_warning,omitempty"`
	RepoDisabled bool            `json:"repo_disabled"`
	LastSynced   string          `json:"last_synced,omitempty"`
}

// agentRepoSpec represents a repo entry in an agent definition.
type agentRepoSpec struct {
	URL string `json:"url"`
	Ref string `json:"ref,omitempty"`
}

// agentDefinitionsResponse is the response from GET /api/v1/agent-definitions.
type agentDefinitionsResponse struct {
	AgentDefinitions []agentDefinition `json:"agent_definitions"`
	Count            int               `json:"count"`
}

// agentRepo represents an agent repo from GET /api/v1/user/settings/agent-repos.
type agentRepo struct {
	URL     string `json:"url"`
	Ref     string `json:"ref,omitempty"`
	Name    string `json:"name,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// agentReposResponse is the response from GET /api/v1/user/settings/agent-repos.
type agentReposResponse struct {
	Repos []agentRepo `json:"repos"`
}

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage agent definitions and repos",
	}
	cmd.AddCommand(
		newAgentsListCmd(),
		newAgentsCheckCredentialsCmd(),
		newAgentsSyncCmd(),
		newAgentsReposCmd(),
		newAgentsRunCmd(),
	)
	return cmd
}

// ---------- agents list ----------

func newAgentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List synced agent definitions",
		RunE:  runAgentsList,
	}
}

func runAgentsList(cmd *cobra.Command, _ []string) error {
	resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/agent-definitions", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return formatAPIError("listing agent definitions", resp.StatusCode, body)
	}

	var result agentDefinitionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	if len(result.AgentDefinitions) == 0 {
		fmt.Fprintln(os.Stderr, "No agent definitions found.")
		return nil
	}

	// Check if any agents have warnings
	hasWarnings := false
	warningCount := 0
	for _, d := range result.AgentDefinitions {
		if d.SyncWarning != "" {
			hasWarnings = true
			warningCount++
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)

	// Add WARNING column only if there are warnings
	if hasWarnings {
		fmt.Fprintln(w, "NAME\tDESCRIPTION\tDEV_CONTAINER\tLAST_SYNCED\tWARNING\tSOURCE")
	} else {
		fmt.Fprintln(w, "NAME\tDESCRIPTION\tDEV_CONTAINER\tLAST_SYNCED\tSOURCE")
	}

	for _, d := range result.AgentDefinitions {
		desc := d.Description
		if len(desc) > 40 {
			desc = desc[:37] + "..."
		}

		devContainer := "no"
		if len(d.DevContainer) > 0 && string(d.DevContainer) != "null" {
			devContainer = "yes"
		}

		lastSynced := d.LastSynced
		if t, err := time.Parse(time.RFC3339Nano, d.LastSynced); err == nil {
			lastSynced = t.Local().Format("2006-01-02 15:04")
		}

		warning := ""
		if d.SyncWarning != "" {
			warning = "⚠ " + d.SyncWarning
			// Truncate long warning text
			if len(warning) > 50 {
				warning = warning[:47] + "..."
			}
		}

		if hasWarnings {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				d.Name, desc, devContainer, lastSynced, warning, d.SourceRepo)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				d.Name, desc, devContainer, lastSynced, d.SourceRepo)
		}
	}

	if err := w.Flush(); err != nil {
		return err
	}

	// Print warning summary footer if there are warnings
	if hasWarnings {
		fmt.Fprintf(os.Stderr, "\n⚠ %d agent(s) have unmet credential requirements. Run `alcove agents check-credentials` for details.\n", warningCount)
	}

	return nil
}

// ---------- agents check-credentials ----------

func newAgentsCheckCredentialsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check-credentials",
		Short: "Check credential requirements for agents",
		RunE:  runAgentsCheckCredentials,
	}
}

func runAgentsCheckCredentials(cmd *cobra.Command, _ []string) error {
	resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/agent-definitions", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return formatAPIError("checking agent credentials", resp.StatusCode, body)
	}

	var result agentDefinitionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	// Filter agents with credential warnings
	var agentsWithWarnings []agentDefinition
	for _, d := range result.AgentDefinitions {
		if d.SyncWarning != "" {
			agentsWithWarnings = append(agentsWithWarnings, d)
		}
	}

	if len(agentsWithWarnings) == 0 {
		fmt.Println("All agents have their credential requirements satisfied.")
		return nil
	}

	fmt.Printf("Found %d agent(s) with unmet credential requirements:\n\n", len(agentsWithWarnings))

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tSOURCE_FILE\tWARNING")
	for _, d := range agentsWithWarnings {
		fmt.Fprintf(w, "%s\t%s\t%s\n", d.Name, d.SourceFile, d.SyncWarning)
	}
	err = w.Flush()
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "\nTo resolve these issues:")
	fmt.Fprintln(os.Stderr, "1. Run `alcove credentials list` to see available credentials")
	fmt.Fprintln(os.Stderr, "2. Run `alcove credentials create` to add missing credentials")
	fmt.Fprintln(os.Stderr, "3. Run `alcove agents sync` to refresh agent definitions")

	return nil
}

// ---------- agents sync ----------

func newAgentsSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Trigger agent definition sync",
		RunE:  runAgentsSync,
	}
}

func runAgentsSync(cmd *cobra.Command, _ []string) error {
	resp, err := apiRequest(cmd, http.MethodPost, "/api/v1/agent-definitions/sync", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return formatAPIError("syncing agent definitions", resp.StatusCode, body)
	}

	if isJSONOutput(cmd) {
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Synced successfully at %s\n", time.Now().Local().Format("2006-01-02 15:04:05"))
	return nil
}

// ---------- agents repos ----------

func newAgentsReposCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repos",
		Short: "List configured agent repos",
		RunE:  runAgentsRepos,
	}
	cmd.Flags().Bool("json", false, "Output JSON instead of table format")
	cmd.AddCommand(
		newAgentsReposAddCmd(),
		newAgentsReposRemoveCmd(),
	)
	return cmd
}

func runAgentsRepos(cmd *cobra.Command, _ []string) error {
	repos, err := fetchAgentRepos(cmd)
	if err != nil {
		return err
	}

	// Check for --json flag or global --output json
	jsonFlag, _ := cmd.Flags().GetBool("json")
	if jsonFlag || isJSONOutput(cmd) {
		return outputJSON(repos)
	}

	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "No agent repos configured.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tURL\tREF")
	for _, r := range repos {
		ref := r.Ref
		if ref == "" {
			ref = "main"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, r.URL, ref)
	}
	return w.Flush()
}

func fetchAgentRepos(cmd *cobra.Command) ([]agentRepo, error) {
	resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/user/settings/agent-repos", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, formatAPIError("fetching agent repos", resp.StatusCode, body)
	}

	var result agentReposResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return result.Repos, nil
}

func putAgentRepos(cmd *cobra.Command, repos []agentRepo) error {
	reqBody := map[string]interface{}{"repos": repos}
	resp, err := apiRequest(cmd, http.MethodPut, "/api/v1/user/settings/agent-repos", reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return formatAPIError("updating agent repos", resp.StatusCode, body)
	}

	return nil
}

// ---------- agents repos add ----------

func newAgentsReposAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add an agent repo",
		RunE:  runAgentsReposAdd,
	}
	cmd.Flags().String("url", "", "Repository URL (required)")
	cmd.Flags().String("ref", "", "Branch, tag, or commit (default: main)")
	cmd.Flags().String("name", "", "Display name for the repo")
	return cmd
}

func runAgentsReposAdd(cmd *cobra.Command, _ []string) error {
	repoURL, _ := cmd.Flags().GetString("url")
	if repoURL == "" {
		return fmt.Errorf("--url is required")
	}

	ref, _ := cmd.Flags().GetString("ref")
	name, _ := cmd.Flags().GetString("name")

	repos, err := fetchAgentRepos(cmd)
	if err != nil {
		return err
	}

	// Check for duplicate URL.
	for _, r := range repos {
		if r.URL == repoURL {
			return fmt.Errorf("agent repo with URL %q already exists", repoURL)
		}
	}

	newRepo := agentRepo{
		URL:  repoURL,
		Ref:  ref,
		Name: name,
	}
	repos = append(repos, newRepo)

	if err := putAgentRepos(cmd, repos); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		return outputJSON(newRepo)
	}

	fmt.Fprintf(os.Stderr, "Agent repo added: %s\n", repoURL)
	return nil
}

// ---------- agents repos remove ----------

func newAgentsReposRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove an agent repo",
		RunE:  runAgentsReposRemove,
	}
	cmd.Flags().String("url", "", "Repository URL to remove")
	cmd.Flags().String("name", "", "Name of the repo to remove")
	return cmd
}

func runAgentsReposRemove(cmd *cobra.Command, _ []string) error {
	repoURL, _ := cmd.Flags().GetString("url")
	repoName, _ := cmd.Flags().GetString("name")

	if repoURL == "" && repoName == "" {
		return fmt.Errorf("either --url or --name is required")
	}

	repos, err := fetchAgentRepos(cmd)
	if err != nil {
		return err
	}

	var filtered []agentRepo
	found := false
	for _, r := range repos {
		if (repoURL != "" && r.URL == repoURL) || (repoName != "" && r.Name == repoName) {
			found = true
			continue
		}
		filtered = append(filtered, r)
	}

	if !found {
		if repoURL != "" {
			return fmt.Errorf("agent repo with URL %q not found", repoURL)
		}
		return fmt.Errorf("agent repo with name %q not found", repoName)
	}

	if filtered == nil {
		filtered = []agentRepo{}
	}

	if err := putAgentRepos(cmd, filtered); err != nil {
		return err
	}

	if isJSONOutput(cmd) {
		return outputJSON(map[string]string{"status": "removed"})
	}

	identifier := repoURL
	if identifier == "" {
		identifier = repoName
	}
	fmt.Fprintf(os.Stderr, "Agent repo removed: %s\n", identifier)
	return nil
}

// ---------- agents run ----------

func newAgentsRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Run an agent definition by name",
		Args:  cobra.ExactArgs(1),
		RunE:  runAgentsRun,
	}
	cmd.Flags().Bool("watch", false, "Stream transcript via SSE after dispatch")
	return cmd
}

func runAgentsRun(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Fetch agent definitions to find the one matching by name.
	resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/agent-definitions", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return formatAPIError("fetching agent definitions for run", resp.StatusCode, body)
	}

	var result agentDefinitionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	var matchedDef *agentDefinition
	for i, d := range result.AgentDefinitions {
		if d.Name == name {
			matchedDef = &result.AgentDefinitions[i]
			break
		}
	}

	if matchedDef == nil {
		return fmt.Errorf("agent definition %q not found", name)
	}

	// Dispatch the agent definition.
	runPath := fmt.Sprintf("/api/v1/agent-definitions/%s/run", matchedDef.ID)
	runResp, err := apiRequest(cmd, http.MethodPost, runPath, nil)
	if err != nil {
		return err
	}
	defer runResp.Body.Close()

	if runResp.StatusCode != http.StatusCreated && runResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(runResp.Body)
		return formatAPIError("running agent", runResp.StatusCode, body)
	}

	var runResult runResponse
	if err := json.NewDecoder(runResp.Body).Decode(&runResult); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(runResult)
	}

	// Handle both old (ID) and new (SessionID) response formats
	sessionID := runResult.SessionID
	if sessionID == "" {
		sessionID = runResult.ID
	}

	// Display warnings first
	if len(runResult.Warnings) > 0 {
		for _, warning := range runResult.Warnings {
			fmt.Fprintf(os.Stderr, "⚠ Warning: %s\n", warning)
		}
		fmt.Fprintln(os.Stderr)
	}

	fmt.Fprintf(os.Stderr, "Session dispatched: %s\n", sessionID)

	watch, _ := cmd.Flags().GetBool("watch")
	if watch {
		return streamSSE(cmd, sessionID, "/api/v1/sessions/"+sessionID+"/transcript")
	}

	fmt.Println(sessionID)
	return nil
}
