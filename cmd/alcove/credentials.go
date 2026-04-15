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

	"github.com/spf13/cobra"
)

func newCredentialsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "credentials",
		Short:   "Manage team credentials and secrets",
		Aliases: []string{"creds"},
	}
	cmd.AddCommand(
		newCredentialsListCmd(),
		newCredentialsCreateCmd(),
		newCredentialsDeleteCmd(),
	)
	return cmd
}

// ---------- credentials list ----------

func newCredentialsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List credentials for the active team",
		RunE:  runCredentialsList,
	}
}

func runCredentialsList(cmd *cobra.Command, _ []string) error {
	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	resp, err := apiRequestRaw(cmd, http.MethodGet, "/api/v1/credentials", nil, teamID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Credentials []credentialInfo `json:"credentials"`
		Count       int             `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	if len(result.Credentials) == 0 {
		fmt.Fprintln(os.Stderr, "No credentials found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tTYPE\tCREATED")
	for _, c := range result.Credentials {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, c.Provider, c.AuthType, c.CreatedAt)
	}
	return w.Flush()
}

// credentialInfo represents a credential returned from the API.
type credentialInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	AuthType  string `json:"auth_type"`
	ProjectID string `json:"project_id,omitempty"`
	Region    string `json:"region,omitempty"`
	APIHost   string `json:"api_host,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	TeamID    string `json:"team_id,omitempty"`
}

// ---------- credentials create ----------

func newCredentialsCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new credential for the active team",
		RunE:  runCredentialsCreate,
	}
	cmd.Flags().String("name", "", "Credential name (required)")
	cmd.Flags().String("provider", "generic", "Provider name (e.g., anthropic, google-vertex, github, gitlab)")
	cmd.Flags().String("auth-type", "secret", "Auth type (e.g., api_key, service_account, secret)")
	cmd.Flags().String("secret", "", "Shorthand: sets provider=generic, auth-type=secret, and uses value as credential")
	cmd.Flags().String("credential", "", "Credential value (e.g., API key, service account JSON)")
	cmd.Flags().String("project-id", "", "GCP project ID (Vertex AI only)")
	cmd.Flags().String("region", "", "GCP region (Vertex AI only)")
	cmd.Flags().String("api-host", "", "Custom API host (e.g., self-hosted GitLab URL)")
	return cmd
}

func runCredentialsCreate(cmd *cobra.Command, _ []string) error {
	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	name, _ := cmd.Flags().GetString("name")
	provider, _ := cmd.Flags().GetString("provider")
	authType, _ := cmd.Flags().GetString("auth-type")
	secret, _ := cmd.Flags().GetString("secret")
	credential, _ := cmd.Flags().GetString("credential")
	projectID, _ := cmd.Flags().GetString("project-id")
	region, _ := cmd.Flags().GetString("region")
	apiHost, _ := cmd.Flags().GetString("api-host")

	// --secret shorthand
	if secret != "" {
		provider = "generic"
		authType = "secret"
		credential = secret
	}

	if name == "" || credential == "" {
		return fmt.Errorf("--name and either --secret or --credential are required")
	}

	payload := map[string]string{
		"name":       name,
		"provider":   provider,
		"auth_type":  authType,
		"credential": credential,
	}
	if projectID != "" {
		payload["project_id"] = projectID
	}
	if region != "" {
		payload["region"] = region
	}
	if apiHost != "" {
		payload["api_host"] = apiHost
	}

	resp, err := apiRequestRaw(cmd, http.MethodPost, "/api/v1/credentials", payload, teamID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result credentialInfo
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Credential created: %s (id: %s)\n", result.Name, result.ID)
	return nil
}

// ---------- credentials delete ----------

func newCredentialsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a credential by ID",
		Args:  cobra.ExactArgs(1),
		RunE:  runCredentialsDelete,
	}
}

func runCredentialsDelete(cmd *cobra.Command, args []string) error {
	credID := args[0]

	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/v1/credentials/%s", credID)

	resp, err := apiRequestRaw(cmd, http.MethodDelete, path, nil, teamID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	if isJSONOutput(cmd) {
		return outputJSON(map[string]string{"id": credID, "status": "deleted"})
	}

	fmt.Fprintf(os.Stderr, "Credential %s deleted\n", credID)
	return nil
}
