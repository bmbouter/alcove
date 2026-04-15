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

// catalogEntry represents a single entry from the global catalog.
type catalogEntry struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// catalogListResponse is the response from GET /api/v1/catalog.
type catalogListResponse struct {
	Entries []catalogEntry `json:"entries"`
}

// teamCatalogEntry represents a catalog entry enabled for a team.
type teamCatalogEntry struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// teamCatalogResponse is the response from GET /api/v1/teams/{teamId}/catalog.
type teamCatalogResponse struct {
	Entries []teamCatalogEntry `json:"entries"`
}

func newCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Manage the service catalog",
	}
	cmd.AddCommand(
		newCatalogListCmd(),
		newCatalogEnableCmd(),
		newCatalogDisableCmd(),
	)
	return cmd
}

// ---------- catalog list ----------

func newCatalogListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List catalog entries",
		RunE:  runCatalogList,
	}
	cmd.Flags().String("category", "", "Filter by category")
	return cmd
}

// catalogDisplayEntry combines catalog metadata with team-specific enabled state.
type catalogDisplayEntry struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
}

func runCatalogList(cmd *cobra.Command, _ []string) error {
	// Fetch the global catalog
	resp, err := apiRequestRaw(cmd, http.MethodGet, "/api/v1/catalog", nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var catalog catalogListResponse
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return fmt.Errorf("decoding catalog response: %w", err)
	}

	// Build a map of enabled states from the team catalog (if a team is active)
	enabledMap := make(map[string]bool)
	teamName := resolveTeamName(cmd)
	if teamName != "" {
		teamID, err := resolveTeamID(cmd, teamName)
		if err == nil {
			path := fmt.Sprintf("/api/v1/teams/%s/catalog", teamID)
			teamResp, err := apiRequestRaw(cmd, http.MethodGet, path, nil, teamID)
			if err == nil {
				defer teamResp.Body.Close()
				if teamResp.StatusCode == http.StatusOK {
					var teamCatalog teamCatalogResponse
					if err := json.NewDecoder(teamResp.Body).Decode(&teamCatalog); err == nil {
						for _, entry := range teamCatalog.Entries {
							enabledMap[entry.ID] = entry.Enabled
						}
					}
				}
			}
		}
	}

	// Merge catalog entries with enabled state
	categoryFilter, _ := cmd.Flags().GetString("category")
	var display []catalogDisplayEntry
	for _, entry := range catalog.Entries {
		if categoryFilter != "" && entry.Category != categoryFilter {
			continue
		}
		display = append(display, catalogDisplayEntry{
			ID:          entry.ID,
			Category:    entry.Category,
			Name:        entry.Name,
			Enabled:     enabledMap[entry.ID],
			Description: entry.Description,
		})
	}

	if isJSONOutput(cmd) {
		return outputJSON(display)
	}

	if len(display) == 0 {
		fmt.Fprintln(os.Stderr, "No catalog entries found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCATEGORY\tNAME\tENABLED\tDESCRIPTION")
	for _, d := range display {
		enabled := "no"
		if d.Enabled {
			enabled = "yes"
		}
		desc := d.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Category, d.Name, enabled, desc)
	}
	return w.Flush()
}

// ---------- catalog enable ----------

func newCatalogEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a catalog entry for the active team",
		Args:  cobra.ExactArgs(1),
		RunE:  runCatalogEnable,
	}
}

func runCatalogEnable(cmd *cobra.Command, args []string) error {
	return setCatalogEnabled(cmd, args[0], true)
}

// ---------- catalog disable ----------

func newCatalogDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a catalog entry for the active team",
		Args:  cobra.ExactArgs(1),
		RunE:  runCatalogDisable,
	}
}

func runCatalogDisable(cmd *cobra.Command, args []string) error {
	return setCatalogEnabled(cmd, args[0], false)
}

// setCatalogEnabled enables or disables a catalog entry for the active team.
func setCatalogEnabled(cmd *cobra.Command, entryID string, enabled bool) error {
	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	reqBody := map[string]bool{"enabled": enabled}
	path := fmt.Sprintf("/api/v1/teams/%s/catalog/%s", teamID, entryID)

	resp, err := apiRequestRaw(cmd, http.MethodPut, path, reqBody, teamID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	action := "enabled"
	if !enabled {
		action = "disabled"
	}

	if isJSONOutput(cmd) {
		return outputJSON(map[string]string{"id": entryID, "team": teamName, "status": action})
	}

	fmt.Fprintf(os.Stderr, "Catalog entry %s %s for team %s\n", entryID, action, teamName)
	return nil
}
