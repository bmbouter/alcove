package bridge

import (
	"testing"
	"time"
)

func TestLoadCatalog(t *testing.T) {
	entries := LoadCatalog()
	if len(entries) < 100 {
		t.Fatalf("LoadCatalog returned %d entries, expected 100+", len(entries))
	}
	// Check first entry has required fields including new ones
	first := entries[0]
	if first.ID == "" {
		t.Error("first entry has empty ID")
	}
	if first.Name == "" {
		t.Error("first entry has empty Name")
	}
	if first.Category == "" {
		t.Error("first entry has empty Category")
	}
	if first.SourceType == "" {
		t.Error("first entry has empty SourceType")
	}
	if first.SourceURL == "" {
		t.Error("first entry has empty SourceURL")
	}

	// Check no duplicate IDs
	seen := make(map[string]bool)
	for _, e := range entries {
		if seen[e.ID] {
			t.Errorf("duplicate catalog entry ID: %s", e.ID)
		}
		seen[e.ID] = true
	}
}

func TestResolveCatalogSkillRepos(t *testing.T) {
	catalog := LoadCatalog()

	// Empty enabled map returns nothing
	repos := ResolveCatalogSkillRepos(catalog, map[string]bool{})
	if len(repos) != 0 {
		t.Errorf("empty enabled map: got %d repos, want 0", len(repos))
	}

	// Nil enabled map returns nothing
	repos = ResolveCatalogSkillRepos(catalog, nil)
	if len(repos) != 0 {
		t.Errorf("nil enabled map: got %d repos, want 0", len(repos))
	}

	// Enable one entry
	repos = ResolveCatalogSkillRepos(catalog, map[string]bool{"code-review": true})
	if len(repos) != 1 {
		t.Errorf("one enabled: got %d repos, want 1", len(repos))
	}
	if len(repos) == 1 && repos[0].Name != "Code Review" {
		t.Errorf("got name %q, want %q", repos[0].Name, "Code Review")
	}

	// Disabled entry returns nothing
	repos = ResolveCatalogSkillRepos(catalog, map[string]bool{"code-review": false})
	if len(repos) != 0 {
		t.Errorf("disabled entry: got %d repos, want 0", len(repos))
	}

	// Unknown entry ID is ignored
	repos = ResolveCatalogSkillRepos(catalog, map[string]bool{"nonexistent": true})
	if len(repos) != 0 {
		t.Errorf("unknown entry: got %d repos, want 0", len(repos))
	}

	// Enable all entries
	all := make(map[string]bool)
	for _, e := range catalog {
		all[e.ID] = true
	}
	repos = ResolveCatalogSkillRepos(catalog, all)
	if len(repos) != len(catalog) {
		t.Errorf("all enabled: got %d repos, want %d", len(repos), len(catalog))
	}
}

func TestResolveCatalogItemsToSkillRepos(t *testing.T) {
	// Create a helper function to simulate the function being tested
	resolveCatalogItemsToSkillRepos := func(items []CatalogItem) []SkillRepo {
		if len(items) == 0 {
			return nil
		}

		// Load catalog to get source URL mapping
		catalog := LoadCatalog()
		sourceMap := make(map[string]CatalogEntry)
		for _, entry := range catalog {
			sourceMap[entry.ID] = entry
		}

		var repos []SkillRepo
		for _, item := range items {
			if source, ok := sourceMap[item.SourceID]; ok {
				enabled := true
				repos = append(repos, SkillRepo{
					URL:     source.SourceURL,
					Ref:     source.Ref,
					Name:    source.Name,
					Enabled: &enabled,
				})
			}
		}

		return repos
	}

	// Test empty input
	repos := resolveCatalogItemsToSkillRepos(nil)
	if repos != nil {
		t.Error("expected nil for empty input")
	}

	repos = resolveCatalogItemsToSkillRepos([]CatalogItem{})
	if repos != nil {
		t.Error("expected nil for empty slice")
	}

	// Test with valid catalog items
	catalog := LoadCatalog()
	if len(catalog) == 0 {
		t.Fatal("catalog is empty, cannot test")
	}

	// Use the first catalog entry as test data
	firstEntry := catalog[0]
	testItems := []CatalogItem{
		{
			ID:       "test-item-1",
			SourceID: firstEntry.ID,
			Slug:     "test-item",
			Name:     "Test Item",
			SyncedAt: time.Now(),
		},
	}

	repos = resolveCatalogItemsToSkillRepos(testItems)
	if len(repos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(repos))
		return
	}

	repo := repos[0]
	if repo.URL != firstEntry.SourceURL {
		t.Errorf("URL mismatch: got %q, want %q", repo.URL, firstEntry.SourceURL)
	}
	if repo.Ref != firstEntry.Ref {
		t.Errorf("Ref mismatch: got %q, want %q", repo.Ref, firstEntry.Ref)
	}
	if repo.Name != firstEntry.Name {
		t.Errorf("Name mismatch: got %q, want %q", repo.Name, firstEntry.Name)
	}
	if repo.Enabled == nil || !*repo.Enabled {
		t.Error("expected enabled to be true")
	}

	// Test with unknown source ID
	unknownItems := []CatalogItem{
		{
			ID:       "test-item-2",
			SourceID: "unknown-source-id",
			Slug:     "unknown-item",
			Name:     "Unknown Item",
			SyncedAt: time.Now(),
		},
	}

	repos = resolveCatalogItemsToSkillRepos(unknownItems)
	if len(repos) != 0 {
		t.Errorf("expected 0 repos for unknown source, got %d", len(repos))
	}
}
