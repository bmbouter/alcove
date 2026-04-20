package bridge

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed catalog.json
var catalogJSON []byte

type CatalogEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	SourceType  string   `json:"source_type"`
	SourceURL   string   `json:"source_url"`
	SourcePath  string   `json:"source_path,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	DocsURL     string   `json:"docs_url,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

var (
	catalogOnce    sync.Once
	catalogEntries []CatalogEntry
)

func LoadCatalog() []CatalogEntry {
	catalogOnce.Do(func() {
		json.Unmarshal(catalogJSON, &catalogEntries)
	})
	return catalogEntries
}

func ResolveCatalogSkillRepos(catalog []CatalogEntry, enabledMap map[string]bool) []SkillRepo {
	var repos []SkillRepo
	for _, entry := range catalog {
		if enabledMap[entry.ID] {
			enabled := true
			repos = append(repos, SkillRepo{
				URL:     entry.SourceURL,
				Ref:     entry.Ref,
				Name:    entry.Name,
				Enabled: &enabled,
			})
		}
	}
	return repos
}

// --- Catalog Item types and store ---

// CatalogItem represents an individual item discovered within a catalog source.
type CatalogItem struct {
	ID          string                 `json:"id"`
	SourceID    string                 `json:"source_id"`
	Slug        string                 `json:"slug"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ItemType    string                 `json:"item_type"`
	Definition  map[string]interface{} `json:"definition,omitempty"`
	SourceFile  string                 `json:"source_file"`
	SyncedAt    time.Time              `json:"synced_at"`
	Enabled     bool                   `json:"enabled"` // populated per-team
}

// CatalogSourceSummary provides a summary of a catalog source with item counts for a team.
type CatalogSourceSummary struct {
	SourceID     string `json:"source_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Category     string `json:"category"`
	TotalItems   int    `json:"total_items"`
	EnabledItems int    `json:"enabled_items"`
}

// ItemToggle specifies a slug and its desired enabled state.
type ItemToggle struct {
	Slug    string `json:"slug"`
	Enabled bool   `json:"enabled"`
}

// CatalogItemStore manages catalog items in PostgreSQL.
type CatalogItemStore struct {
	db *pgxpool.Pool
}

// NewCatalogItemStore creates a CatalogItemStore with the given database pool.
func NewCatalogItemStore(db *pgxpool.Pool) *CatalogItemStore {
	return &CatalogItemStore{db: db}
}

// UpsertCatalogItems bulk upserts items for a source, removing items no longer present.
func (s *CatalogItemStore) UpsertCatalogItems(ctx context.Context, sourceID string, items []CatalogItem) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()
	seenSlugs := make(map[string]bool)

	for _, item := range items {
		seenSlugs[item.Slug] = true
		var defJSON []byte
		if item.Definition != nil {
			defJSON, err = json.Marshal(item.Definition)
			if err != nil {
				return fmt.Errorf("marshaling definition for %s: %w", item.Slug, err)
			}
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO catalog_items (source_id, slug, name, description, item_type, definition, source_file, synced_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (source_id, slug) DO UPDATE SET
				name = EXCLUDED.name,
				description = EXCLUDED.description,
				item_type = EXCLUDED.item_type,
				definition = EXCLUDED.definition,
				source_file = EXCLUDED.source_file,
				synced_at = EXCLUDED.synced_at
		`, sourceID, item.Slug, item.Name, item.Description, item.ItemType, defJSON, item.SourceFile, now)
		if err != nil {
			return fmt.Errorf("upserting catalog item %s/%s: %w", sourceID, item.Slug, err)
		}
	}

	// Remove items that are no longer in the source.
	if len(seenSlugs) > 0 {
		slugList := make([]string, 0, len(seenSlugs))
		for slug := range seenSlugs {
			slugList = append(slugList, slug)
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM catalog_items WHERE source_id = $1 AND slug != ALL($2)
		`, sourceID, slugList)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM catalog_items WHERE source_id = $1`, sourceID)
	}
	if err != nil {
		return fmt.Errorf("cleaning stale catalog items for %s: %w", sourceID, err)
	}

	return tx.Commit(ctx)
}

// ListCatalogItems returns all items for a given source.
func (s *CatalogItemStore) ListCatalogItems(ctx context.Context, sourceID string) ([]CatalogItem, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, source_id, slug, name, description, item_type, definition, source_file, synced_at
		FROM catalog_items
		WHERE source_id = $1
		ORDER BY name ASC
	`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("querying catalog items: %w", err)
	}
	defer rows.Close()

	var items []CatalogItem
	for rows.Next() {
		var item CatalogItem
		var defJSON []byte
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Slug, &item.Name, &item.Description,
			&item.ItemType, &defJSON, &item.SourceFile, &item.SyncedAt); err != nil {
			return nil, fmt.Errorf("scanning catalog item: %w", err)
		}
		if defJSON != nil {
			json.Unmarshal(defJSON, &item.Definition)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating catalog items: %w", err)
	}
	if items == nil {
		items = []CatalogItem{}
	}
	return items, nil
}

// GetCatalogItem returns a single catalog item by source and slug.
func (s *CatalogItemStore) GetCatalogItem(ctx context.Context, sourceID, slug string) (*CatalogItem, error) {
	var item CatalogItem
	var defJSON []byte
	err := s.db.QueryRow(ctx, `
		SELECT id, source_id, slug, name, description, item_type, definition, source_file, synced_at
		FROM catalog_items
		WHERE source_id = $1 AND slug = $2
	`, sourceID, slug).Scan(&item.ID, &item.SourceID, &item.Slug, &item.Name, &item.Description,
		&item.ItemType, &defJSON, &item.SourceFile, &item.SyncedAt)
	if err != nil {
		return nil, fmt.Errorf("catalog item %s/%s not found: %w", sourceID, slug, err)
	}
	if defJSON != nil {
		json.Unmarshal(defJSON, &item.Definition)
	}
	return &item, nil
}

// ListSourcesWithCounts returns catalog sources enriched with total/enabled item counts for a team.
func (s *CatalogItemStore) ListSourcesWithCounts(ctx context.Context, teamID string) ([]CatalogSourceSummary, error) {
	catalog := LoadCatalog()

	// Get total item counts per source.
	totalRows, err := s.db.Query(ctx, `
		SELECT source_id, COUNT(*) FROM catalog_items GROUP BY source_id
	`)
	if err != nil {
		return nil, fmt.Errorf("querying total items: %w", err)
	}
	defer totalRows.Close()

	totalCounts := make(map[string]int)
	for totalRows.Next() {
		var sourceID string
		var count int
		if err := totalRows.Scan(&sourceID, &count); err != nil {
			return nil, fmt.Errorf("scanning total count: %w", err)
		}
		totalCounts[sourceID] = count
	}
	if err := totalRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating total counts: %w", err)
	}

	// Get enabled item counts per source for this team.
	enabledRows, err := s.db.Query(ctx, `
		SELECT source_id, COUNT(*) FROM team_catalog_items
		WHERE team_id = $1 AND enabled = true
		GROUP BY source_id
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying enabled items: %w", err)
	}
	defer enabledRows.Close()

	enabledCounts := make(map[string]int)
	for enabledRows.Next() {
		var sourceID string
		var count int
		if err := enabledRows.Scan(&sourceID, &count); err != nil {
			return nil, fmt.Errorf("scanning enabled count: %w", err)
		}
		enabledCounts[sourceID] = count
	}
	if err := enabledRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating enabled counts: %w", err)
	}

	var summaries []CatalogSourceSummary
	for _, entry := range catalog {
		summaries = append(summaries, CatalogSourceSummary{
			SourceID:     entry.ID,
			Name:         entry.Name,
			Description:  entry.Description,
			Category:     entry.Category,
			TotalItems:   totalCounts[entry.ID],
			EnabledItems: enabledCounts[entry.ID],
		})
	}
	return summaries, nil
}

// ListTeamEnabledItems returns all enabled items for a team across all sources.
func (s *CatalogItemStore) ListTeamEnabledItems(ctx context.Context, teamID string) ([]CatalogItem, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ci.id, ci.source_id, ci.slug, ci.name, ci.description, ci.item_type,
		       ci.definition, ci.source_file, ci.synced_at
		FROM catalog_items ci
		JOIN team_catalog_items tci ON ci.source_id = tci.source_id AND ci.slug = tci.item_slug
		WHERE tci.team_id = $1 AND tci.enabled = true
		ORDER BY ci.source_id, ci.name
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying team enabled items: %w", err)
	}
	defer rows.Close()

	var items []CatalogItem
	for rows.Next() {
		var item CatalogItem
		var defJSON []byte
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Slug, &item.Name, &item.Description,
			&item.ItemType, &defJSON, &item.SourceFile, &item.SyncedAt); err != nil {
			return nil, fmt.Errorf("scanning enabled item: %w", err)
		}
		if defJSON != nil {
			json.Unmarshal(defJSON, &item.Definition)
		}
		item.Enabled = true
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating enabled items: %w", err)
	}
	if items == nil {
		items = []CatalogItem{}
	}
	return items, nil
}

// SetItemEnabled toggles a single item's enabled state for a team.
func (s *CatalogItemStore) SetItemEnabled(ctx context.Context, teamID, sourceID, itemSlug string, enabled bool) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO team_catalog_items (team_id, source_id, item_slug, enabled, enabled_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (team_id, source_id, item_slug) DO UPDATE SET enabled = $4, enabled_at = NOW()
	`, teamID, sourceID, itemSlug, enabled)
	if err != nil {
		return fmt.Errorf("setting item enabled: %w", err)
	}
	return nil
}

// BulkSetItemsEnabled toggles multiple items' enabled state for a team within a source.
func (s *CatalogItemStore) BulkSetItemsEnabled(ctx context.Context, teamID, sourceID string, items []ItemToggle) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, item := range items {
		_, err = tx.Exec(ctx, `
			INSERT INTO team_catalog_items (team_id, source_id, item_slug, enabled, enabled_at)
			VALUES ($1, $2, $3, $4, NOW())
			ON CONFLICT (team_id, source_id, item_slug) DO UPDATE SET enabled = $4, enabled_at = NOW()
		`, teamID, sourceID, item.Slug, item.Enabled)
		if err != nil {
			return fmt.Errorf("setting item %s enabled: %w", item.Slug, err)
		}
	}

	return tx.Commit(ctx)
}

// ListEnabledAgents returns enabled items with item_type="agent" for a team.
func (s *CatalogItemStore) ListEnabledAgents(ctx context.Context, teamID string) ([]CatalogItem, error) {
	rows, err := s.db.Query(ctx, `
		SELECT ci.id, ci.source_id, ci.slug, ci.name, ci.description, ci.item_type,
		       ci.definition, ci.source_file, ci.synced_at
		FROM catalog_items ci
		JOIN team_catalog_items tci ON ci.source_id = tci.source_id AND ci.slug = tci.item_slug
		WHERE tci.team_id = $1 AND tci.enabled = true AND ci.item_type = 'agent'
		ORDER BY ci.source_id, ci.name
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying enabled agents: %w", err)
	}
	defer rows.Close()

	var items []CatalogItem
	for rows.Next() {
		var item CatalogItem
		var defJSON []byte
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Slug, &item.Name, &item.Description,
			&item.ItemType, &defJSON, &item.SourceFile, &item.SyncedAt); err != nil {
			return nil, fmt.Errorf("scanning enabled agent: %w", err)
		}
		if defJSON != nil {
			json.Unmarshal(defJSON, &item.Definition)
		}
		item.Enabled = true
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating enabled agents: %w", err)
	}
	if items == nil {
		items = []CatalogItem{}
	}
	return items, nil
}

// ListItemsForTeam returns all items for a source with their enabled state for a team.
// Supports optional search filtering on name/description.
func (s *CatalogItemStore) ListItemsForTeam(ctx context.Context, sourceID, teamID, search string) ([]CatalogItem, error) {
	query := `
		SELECT ci.id, ci.source_id, ci.slug, ci.name, ci.description, ci.item_type,
		       ci.definition, ci.source_file, ci.synced_at,
		       COALESCE(tci.enabled, false) AS enabled
		FROM catalog_items ci
		LEFT JOIN team_catalog_items tci ON ci.source_id = tci.source_id AND ci.slug = tci.item_slug AND tci.team_id = $2
		WHERE ci.source_id = $1
	`
	args := []interface{}{sourceID, teamID}

	if search != "" {
		query += ` AND (LOWER(ci.name) LIKE $3 OR LOWER(ci.description) LIKE $3)`
		args = append(args, "%"+strings.ToLower(search)+"%")
	}

	query += ` ORDER BY ci.name ASC`

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying items for team: %w", err)
	}
	defer rows.Close()

	var items []CatalogItem
	for rows.Next() {
		var item CatalogItem
		var defJSON []byte
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Slug, &item.Name, &item.Description,
			&item.ItemType, &defJSON, &item.SourceFile, &item.SyncedAt, &item.Enabled); err != nil {
			return nil, fmt.Errorf("scanning item for team: %w", err)
		}
		if defJSON != nil {
			json.Unmarshal(defJSON, &item.Definition)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating items for team: %w", err)
	}
	if items == nil {
		items = []CatalogItem{}
	}
	return items, nil
}

// MigrateSourceEnablementToItems migrates a team's source-level catalog enablement
// to item-level enablement. If a source is enabled in the old team_settings catalog
// key but has no rows in team_catalog_items, all discovered items for that source
// are auto-enabled for the team.
func (s *CatalogItemStore) MigrateSourceEnablementToItems(ctx context.Context, teamID string, enabledMap map[string]bool) error {
	for sourceID, enabled := range enabledMap {
		if !enabled {
			continue
		}

		// Check if this team already has item-level rows for this source.
		var count int
		err := s.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM team_catalog_items WHERE team_id = $1 AND source_id = $2
		`, teamID, sourceID).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking existing item enablement for %s: %w", sourceID, err)
		}
		if count > 0 {
			continue // Already migrated.
		}

		// Enable all items from this source for this team.
		_, err = s.db.Exec(ctx, `
			INSERT INTO team_catalog_items (team_id, source_id, item_slug, enabled, enabled_at)
			SELECT $1, source_id, slug, true, NOW()
			FROM catalog_items
			WHERE source_id = $2
			ON CONFLICT DO NOTHING
		`, teamID, sourceID)
		if err != nil {
			return fmt.Errorf("migrating source %s enablement to items: %w", sourceID, err)
		}
	}
	return nil
}
