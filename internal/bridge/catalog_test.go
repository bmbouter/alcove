package bridge

import (
	"testing"
)

func TestLoadCatalog(t *testing.T) {
	entries := LoadCatalog()
	if len(entries) == 0 {
		t.Fatal("LoadCatalog returned 0 entries")
	}
	// Check first entry has required fields
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
	if first.URL == "" {
		t.Error("first entry has empty URL")
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
