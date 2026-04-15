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
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// catalogEntry represents a catalog entry from GET /api/v1/teams/{team}/catalog.
type catalogEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	SourceType  string   `json:"source_type"`
	Enabled     bool     `json:"enabled"`
	Tags        []string `json:"tags"`
}

// catalogEntriesResponse is the response from GET /api/v1/teams/{team}/catalog.
type catalogEntriesResponse struct {
	Entries []catalogEntry `json:"entries"`
}

// catalogItem represents a single item within a source from GET /api/v1/teams/{team}/catalog/{source}.
type catalogItem struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// catalogItemsResponse is the response from GET /api/v1/teams/{team}/catalog/{source}.
type catalogItemsResponse struct {
	Items []catalogItem `json:"items"`
}

// sourceTypeLabel maps source_type to a short display label.
func sourceTypeLabel(sourceType string) string {
	switch sourceType {
	case "claude-plugins-official":
		return "plugin"
	case "agency-agents":
		return "agent"
	case "mcp-server":
		return "mcp"
	case "plugin-bundle":
		return "bundle"
	default:
		return sourceType
	}
}

// catalogAgent represents an enabled agent from GET /api/v1/teams/{team}/agents.
type catalogAgent struct {
	Source string `json:"source"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
}

// catalogAgentsResponse is the response from GET /api/v1/teams/{team}/agents.
type catalogAgentsResponse struct {
	Agents []catalogAgent `json:"agents"`
}

func newCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Manage the service catalog",
	}
	cmd.AddCommand(
		newCatalogListCmd(),
		newCatalogSearchCmd(),
		newCatalogItemsCmd(),
		newCatalogEnableCmd(),
		newCatalogDisableCmd(),
		newCatalogAgentsCmd(),
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

func runCatalogList(cmd *cobra.Command, _ []string) error {
	entries, err := fetchCatalogEntries(cmd)
	if err != nil {
		return err
	}

	categoryFilter, _ := cmd.Flags().GetString("category")
	var filtered []catalogEntry
	for _, e := range entries {
		if categoryFilter != "" && !strings.EqualFold(e.Category, categoryFilter) {
			continue
		}
		filtered = append(filtered, e)
	}

	return printCatalogEntries(cmd, filtered)
}

// fetchCatalogEntries retrieves all catalog entries from the API.
func fetchCatalogEntries(cmd *cobra.Command) ([]catalogEntry, error) {
	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return nil, fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return nil, err
	}

	path := fmt.Sprintf("/api/v1/teams/%s/catalog", teamID)
	resp, err := apiRequestRaw(cmd, http.MethodGet, path, nil, teamID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result catalogEntriesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding catalog response: %w", err)
	}

	return result.Entries, nil
}

// printCatalogEntries renders catalog entries as a table or JSON.
func printCatalogEntries(cmd *cobra.Command, entries []catalogEntry) error {
	if isJSONOutput(cmd) {
		return outputJSON(entries)
	}

	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "No catalog entries found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCATEGORY\tTYPE\tNAME\tENABLED\tDESCRIPTION")
	for _, e := range entries {
		enabled := "no"
		if e.Enabled {
			enabled = "yes"
		}
		desc := e.Description
		if len(desc) > 50 {
			desc = desc[:47] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID, e.Category, sourceTypeLabel(e.SourceType), e.Name, enabled, desc)
	}
	return w.Flush()
}

// ---------- catalog search ----------

func newCatalogSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <query>",
		Short: "Search catalog entries by name, description, or tags",
		Args:  cobra.ExactArgs(1),
		RunE:  runCatalogSearch,
	}
}

func runCatalogSearch(cmd *cobra.Command, args []string) error {
	query := strings.ToLower(args[0])

	entries, err := fetchCatalogEntries(cmd)
	if err != nil {
		return err
	}

	var matched []catalogEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name), query) ||
			strings.Contains(strings.ToLower(e.Description), query) {
			matched = append(matched, e)
			continue
		}
		for _, tag := range e.Tags {
			if strings.Contains(strings.ToLower(tag), query) {
				matched = append(matched, e)
				break
			}
		}
	}

	return printCatalogEntries(cmd, matched)
}

// ---------- catalog items ----------

func newCatalogItemsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "items <source>",
		Short: "List items within a catalog source",
		Args:  cobra.ExactArgs(1),
		RunE:  runCatalogItems,
	}
	cmd.Flags().String("search", "", "Filter items by name or slug")
	return cmd
}

func runCatalogItems(cmd *cobra.Command, args []string) error {
	source := args[0]

	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/v1/teams/%s/catalog/%s", teamID, source)
	resp, err := apiRequestRaw(cmd, http.MethodGet, path, nil, teamID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result catalogItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding items response: %w", err)
	}

	// Apply search filter if provided
	search, _ := cmd.Flags().GetString("search")
	var filtered []catalogItem
	for _, item := range result.Items {
		if search != "" {
			lowerSearch := strings.ToLower(search)
			if !strings.Contains(strings.ToLower(item.Slug), lowerSearch) &&
				!strings.Contains(strings.ToLower(item.Name), lowerSearch) {
				continue
			}
		}
		filtered = append(filtered, item)
	}

	if isJSONOutput(cmd) {
		return outputJSON(filtered)
	}

	if len(filtered) == 0 {
		fmt.Fprintln(os.Stderr, "No items found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tNAME\tTYPE\tENABLED")
	for _, item := range filtered {
		enabled := "no"
		if item.Enabled {
			enabled = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", item.Slug, item.Name, item.Type, enabled)
	}
	return w.Flush()
}

// ---------- catalog enable ----------

func newCatalogEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <source>[/<item>]",
		Short: "Enable a catalog source or individual item for the active team",
		Long: `Enable a catalog source or individual item.

When the argument contains "/", it enables a single item within a source.
When no "/", it enables ALL items in the source (bulk enable).

Examples:
  alcove catalog enable agency-agents/marketing-writer   # enable single item
  alcove catalog enable agency-agents                     # enable all items in source`,
		Args: cobra.ExactArgs(1),
		RunE: runCatalogEnable,
	}
}

func runCatalogEnable(cmd *cobra.Command, args []string) error {
	return setCatalogEnabled(cmd, args[0], true)
}

// ---------- catalog disable ----------

func newCatalogDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <source>[/<item>]",
		Short: "Disable a catalog source or individual item for the active team",
		Long: `Disable a catalog source or individual item.

When the argument contains "/", it disables a single item within a source.
When no "/", it disables ALL items in the source (bulk disable).

Examples:
  alcove catalog disable agency-agents/marketing-writer   # disable single item
  alcove catalog disable agency-agents                     # disable all items in source`,
		Args: cobra.ExactArgs(1),
		RunE: runCatalogDisable,
	}
}

func runCatalogDisable(cmd *cobra.Command, args []string) error {
	return setCatalogEnabled(cmd, args[0], false)
}

// setCatalogEnabled enables or disables a catalog entry for the active team.
// If the argument contains "/", it toggles a single item (PUT /api/v1/teams/{team}/catalog/{source}/{item}).
// Otherwise, it toggles all items in the source (PUT /api/v1/teams/{team}/catalog/{source}).
func setCatalogEnabled(cmd *cobra.Command, arg string, enabled bool) error {
	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	reqBody := map[string]bool{"enabled": enabled}

	var path string
	var displayName string
	if source, item, ok := strings.Cut(arg, "/"); ok {
		// Single item: PUT /api/v1/teams/{team}/catalog/{source}/{item}
		path = fmt.Sprintf("/api/v1/teams/%s/catalog/%s/%s", teamID, source, item)
		displayName = arg
	} else {
		// Bulk source: PUT /api/v1/teams/{team}/catalog/{source}
		path = fmt.Sprintf("/api/v1/teams/%s/catalog/%s", teamID, arg)
		displayName = arg
	}

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
		return outputJSON(map[string]string{"id": displayName, "team": teamName, "status": action})
	}

	fmt.Fprintf(os.Stderr, "Catalog entry %s %s for team %s\n", displayName, action, teamName)
	return nil
}

// ---------- catalog agents ----------

func newCatalogAgentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List all enabled agents across all sources",
		RunE:  runCatalogAgents,
	}
}

func runCatalogAgents(cmd *cobra.Command, _ []string) error {
	teamName := resolveTeamName(cmd)
	if teamName == "" {
		return fmt.Errorf("no active team; use 'alcove teams use <name>' or --team to set one")
	}

	teamID, err := resolveTeamID(cmd, teamName)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/api/v1/teams/%s/agents", teamID)
	resp, err := apiRequestRaw(cmd, http.MethodGet, path, nil, teamID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result catalogAgentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding agents response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result.Agents)
	}

	if len(result.Agents) == 0 {
		fmt.Fprintln(os.Stderr, "No enabled agents found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE/SLUG\tNAME")
	for _, a := range result.Agents {
		sourceSlug := fmt.Sprintf("%s/%s", a.Source, a.Slug)
		fmt.Fprintf(w, "%s\t%s\n", sourceSlug, a.Name)
	}
	return w.Flush()
}
