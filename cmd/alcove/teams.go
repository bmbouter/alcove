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

func newTeamsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "Manage teams",
	}
	cmd.AddCommand(
		newTeamsListCmd(),
		newTeamsCreateCmd(),
		newTeamsUseCmd(),
		newTeamsAddMemberCmd(),
		newTeamsRemoveMemberCmd(),
		newTeamsDeleteCmd(),
	)
	return cmd
}

// ---------- teams list ----------

func newTeamsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all teams",
		RunE:  runTeamsList,
	}
}

func runTeamsList(cmd *cobra.Command, _ []string) error {
	resp, err := apiRequestRaw(cmd, http.MethodGet, "/api/v1/teams", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result teamsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	if len(result.Teams) == 0 {
		fmt.Fprintln(os.Stderr, "No teams found.")
		return nil
	}

	activeTeam := resolveTeamName(cmd)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "  NAME\tPERSONAL\tCREATED")
	for _, t := range result.Teams {
		marker := " "
		if t.Name == activeTeam {
			marker = "*"
		}
		personal := "no"
		if t.IsPersonal {
			personal = "yes"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\n", marker, t.Name, personal, t.CreatedAt)
	}
	return w.Flush()
}

// ---------- teams create ----------

func newTeamsCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new team",
		Args:  cobra.ExactArgs(1),
		RunE:  runTeamsCreate,
	}
}

func runTeamsCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	reqBody := map[string]string{"name": name}

	resp, err := apiRequestRaw(cmd, http.MethodPost, "/api/v1/teams", reqBody, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result teamInfo
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Team created: %s\n", result.Name)
	return nil
}

// ---------- teams use ----------

func newTeamsUseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use [name]",
		Short: "Set the active team for the current profile",
		Long:  "Set the active team. Use --personal to switch to your personal team, or provide a team name.",
		RunE:  runTeamsUse,
	}
	cmd.Flags().Bool("personal", false, "Switch to your personal team")
	return cmd
}

func runTeamsUse(cmd *cobra.Command, args []string) error {
	personal, _ := cmd.Flags().GetBool("personal")

	var teamName string

	if personal {
		if len(args) > 0 {
			return fmt.Errorf("cannot specify both --personal and a team name")
		}
		// Find the personal team
		resp, err := apiRequestRaw(cmd, http.MethodGet, "/api/v1/teams", nil, "")
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
		}

		var result teamsListResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}

		for _, t := range result.Teams {
			if t.IsPersonal {
				teamName = t.Name
				break
			}
		}
		if teamName == "" {
			return fmt.Errorf("no personal team found")
		}
	} else {
		if len(args) != 1 {
			return fmt.Errorf("provide a team name or use --personal")
		}
		teamName = args[0]

		// Validate the team exists by resolving its ID
		if _, err := resolveTeamID(cmd, teamName); err != nil {
			return err
		}
	}

	// Save to active_team in current profile config
	cfg, _ := loadConfig()
	if cfg == nil {
		cfg = &CLIConfig{}
	}

	// Determine which profile to update
	profileName, _ := cmd.Flags().GetString("profile")
	if profileName == "" {
		profileName = os.Getenv("ALCOVE_PROFILE")
	}
	if profileName == "" {
		profileName = cfg.ActiveProfile
	}

	if profileName != "" && cfg.Profiles != nil {
		if profile, ok := cfg.Profiles[profileName]; ok {
			profile.ActiveTeam = teamName
			cfg.Profiles[profileName] = profile
		} else {
			return fmt.Errorf("profile %q not found in config", profileName)
		}
	} else {
		cfg.CLIProfile.ActiveTeam = teamName
	}

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Active team set to: %s\n", teamName)
	return nil
}

// ---------- teams add-member ----------

func newTeamsAddMemberCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-member <team> <username>",
		Short: "Add a member to a team",
		Args:  cobra.ExactArgs(2),
		RunE:  runTeamsAddMember,
	}
}

func runTeamsAddMember(cmd *cobra.Command, args []string) error {
	teamName := args[0]
	username := args[1]

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	reqBody := map[string]string{"username": username}
	path := fmt.Sprintf("/api/v1/teams/%s/members", teamID)

	resp, err := apiRequestRaw(cmd, http.MethodPost, path, reqBody, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	if isJSONOutput(cmd) {
		return outputJSON(map[string]string{"team": teamName, "username": username, "status": "added"})
	}

	fmt.Fprintf(os.Stderr, "Added %s to team %s\n", username, teamName)
	return nil
}

// ---------- teams remove-member ----------

func newTeamsRemoveMemberCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-member <team> <username>",
		Short: "Remove a member from a team",
		Args:  cobra.ExactArgs(2),
		RunE:  runTeamsRemoveMember,
	}
}

func runTeamsRemoveMember(cmd *cobra.Command, args []string) error {
	teamName := args[0]
	username := args[1]

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/v1/teams/%s/members/%s", teamID, username)

	resp, err := apiRequestRaw(cmd, http.MethodDelete, path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	if isJSONOutput(cmd) {
		return outputJSON(map[string]string{"team": teamName, "username": username, "status": "removed"})
	}

	fmt.Fprintf(os.Stderr, "Removed %s from team %s\n", username, teamName)
	return nil
}

// ---------- teams delete ----------

func newTeamsDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <team>",
		Short: "Delete a team",
		Args:  cobra.ExactArgs(1),
		RunE:  runTeamsDelete,
	}
	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runTeamsDelete(cmd *cobra.Command, args []string) error {
	teamName := args[0]
	yes, _ := cmd.Flags().GetBool("yes")

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	if !yes {
		fmt.Fprintf(os.Stderr, "Delete team %q? This cannot be undone. [y/N] ", teamName)
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" && answer != "yes" {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return nil
		}
	}

	path := fmt.Sprintf("/api/v1/teams/%s", teamID)

	resp, err := apiRequestRaw(cmd, http.MethodDelete, path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	if isJSONOutput(cmd) {
		return outputJSON(map[string]string{"team": teamName, "status": "deleted"})
	}

	fmt.Fprintf(os.Stderr, "Team %s deleted\n", teamName)
	return nil
}
