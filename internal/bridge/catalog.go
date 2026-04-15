package bridge

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed catalog.json
var catalogJSON []byte

type CatalogEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Ref         string   `json:"ref,omitempty"`
	Path        string   `json:"path,omitempty"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source,omitempty"`
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
				URL:     entry.URL,
				Ref:     entry.Ref,
				Name:    entry.Name,
				Enabled: &enabled,
			})
		}
	}
	return repos
}
