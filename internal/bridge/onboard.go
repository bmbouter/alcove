package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AnalyzeRequest represents the request to analyze a repository.
type AnalyzeRequest struct {
	RepoURL string `json:"repo_url"`
	Ref     string `json:"ref,omitempty"`
}

// Language represents a programming language with its percentage.
type Language struct {
	Name       string `json:"name"`
	Percentage int    `json:"percentage"`
}

// AnalyzeResponse represents the analysis results for a repository.
type AnalyzeResponse struct {
	Languages             []Language `json:"languages"`
	CISystems            []string   `json:"ci_systems"`
	HasAlcoveConfig      bool       `json:"has_alcove_config"`
	HasClaudeMD          bool       `json:"has_claude_md"`
	RecommendedTemplates []string   `json:"recommended_templates"`
	FileCount            int        `json:"file_count"`
}

// AnalyzeResult represents internal analysis results.
type AnalyzeResult struct {
	LanguageCounts       map[string]int
	CISystems           []string
	HasAlcoveConfig     bool
	HasClaudeMD         bool
	FileCount           int
}

// languageExtensions maps file extensions to programming languages.
var languageExtensions = map[string]string{
	".go":    "Go",
	".py":    "Python",
	".pyw":   "Python",
	".ts":    "TypeScript",
	".tsx":   "TypeScript",
	".js":    "JavaScript",
	".jsx":   "JavaScript",
	".mjs":   "JavaScript",
	".java":  "Java",
	".jar":   "Java",
	".rs":    "Rust",
	".rb":    "Ruby",
	".cs":    "C#",
	".cpp":   "C++",
	".cc":    "C++",
	".cxx":   "C++",
	".c":     "C",
	".h":     "C",
	".hpp":   "C++",
	".php":   "PHP",
	".kt":    "Kotlin",
	".scala": "Scala",
	".sh":    "Shell",
	".bash":  "Shell",
	".zsh":   "Shell",
	".fish":  "Shell",
	".r":     "R",
	".R":     "R",
	".swift": "Swift",
	".dart":  "Dart",
	".lua":   "Lua",
	".pl":    "Perl",
	".pm":    "Perl",
	".elm":   "Elm",
	".clj":   "Clojure",
	".cljs":  "Clojure",
	".ex":    "Elixir",
	".exs":   "Elixir",
	".erl":   "Erlang",
	".hrl":   "Erlang",
	".ml":    "OCaml",
	".mli":   "OCaml",
	".hs":    "Haskell",
	".lhs":   "Haskell",
	".vim":   "Vim script",
	".sql":   "SQL",
	".tf":    "HCL",
	".hcl":   "HCL",
}

// ciIndicators maps paths and files to CI systems.
var ciIndicators = map[string]string{
	".github/workflows":  "github-actions",
	".gitlab-ci.yml":     "gitlab-ci",
	".gitlab-ci.yaml":    "gitlab-ci",
	"Jenkinsfile":        "jenkins",
	".circleci":          "circleci",
	".travis.yml":        "travis-ci",
	".appveyor.yml":      "appveyor",
	"azure-pipelines.yml": "azure-pipelines",
	"buildkite.yml":      "buildkite",
}

// handleOnboardAnalyze handles the POST /api/v1/onboard/analyze endpoint.
func (a *API) handleOnboardAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.RepoURL == "" {
		respondError(w, http.StatusBadRequest, "repo_url is required")
		return
	}

	// SSRF mitigation: only allow HTTPS URLs
	if !strings.HasPrefix(req.RepoURL, "https://") {
		respondError(w, http.StatusBadRequest, "only HTTPS URLs are supported")
		return
	}

	// 30s timeout matching handleAgentRepoValidate
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := a.analyzeRepository(ctx, req.RepoURL, req.Ref)
	if err != nil {
		log.Printf("error: analyzing repository %s: %v", req.RepoURL, err)
		respondError(w, http.StatusBadRequest, "repository not found or not accessible — only public repositories are supported for analysis")
		return
	}

	// Convert internal result to response format
	response := &AnalyzeResponse{
		Languages:             calculateLanguagePercentages(result.LanguageCounts),
		CISystems:            result.CISystems,
		HasAlcoveConfig:      result.HasAlcoveConfig,
		HasClaudeMD:          result.HasClaudeMD,
		RecommendedTemplates: recommendTemplates(result),
		FileCount:            result.FileCount,
	}

	respondJSON(w, http.StatusOK, response)
}

// analyzeRepository clones a repository and analyzes its structure.
func (a *API) analyzeRepository(ctx context.Context, repoURL, ref string) (*AnalyzeResult, error) {
	// Create temp directory
	dir, err := os.MkdirTemp("", "alcove-analyze-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// Clone repository (pattern from tasksync.go:900-916)
	args := []string{"clone", "--depth=1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, repoURL, dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("cloning: %s", string(out))
	}

	// Analyze the cloned directory
	return analyzeDirectory(dir, 1000)
}

// analyzeDirectory analyzes a directory and returns analysis results.
func analyzeDirectory(dir string, maxFiles int) (*AnalyzeResult, error) {
	result := &AnalyzeResult{
		LanguageCounts: make(map[string]int),
		CISystems:     []string{},
	}

	filesProcessed := 0
	ciSystems := make(map[string]bool)

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip files that can't be read
		}

		// Skip hidden directories except for CI-specific ones
		relPath, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			// Check for CI systems
			for indicator, system := range ciIndicators {
				if strings.HasSuffix(relPath, indicator) {
					ciSystems[system] = true
					break
				}
			}

			// Check for .alcove directory
			if strings.HasSuffix(relPath, ".alcove") {
				result.HasAlcoveConfig = true
			}

			// Skip .git and other hidden directories except specific CI ones
			if strings.HasPrefix(d.Name(), ".") &&
			   !strings.HasPrefix(d.Name(), ".github") &&
			   !strings.HasPrefix(d.Name(), ".gitlab") &&
			   !strings.HasPrefix(d.Name(), ".circleci") {
				return filepath.SkipDir
			}

			return nil
		}

		// Check for CLAUDE.md
		if strings.ToLower(d.Name()) == "claude.md" {
			result.HasClaudeMD = true
		}

		// Check for CI files
		for indicator, system := range ciIndicators {
			if d.Name() == indicator || strings.HasSuffix(relPath, indicator) {
				ciSystems[system] = true
				break
			}
		}

		// Count file extensions for language detection
		if filesProcessed < maxFiles {
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if language, ok := languageExtensions[ext]; ok {
				result.LanguageCounts[language]++
			}
			filesProcessed++
		}

		result.FileCount++
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walking directory: %w", err)
	}

	// Convert CI systems map to slice
	for system := range ciSystems {
		result.CISystems = append(result.CISystems, system)
	}

	return result, nil
}

// calculateLanguagePercentages converts language counts to percentages.
func calculateLanguagePercentages(languageCounts map[string]int) []Language {
	total := 0
	for _, count := range languageCounts {
		total += count
	}

	if total == 0 {
		return []Language{}
	}

	languages := make([]Language, 0, len(languageCounts))
	for lang, count := range languageCounts {
		percentage := (count * 100) / total
		if percentage > 0 { // Only include languages with >0% after rounding
			languages = append(languages, Language{
				Name:       lang,
				Percentage: percentage,
			})
		}
	}

	// Sort by percentage (descending)
	for i := 0; i < len(languages)-1; i++ {
		for j := i + 1; j < len(languages); j++ {
			if languages[j].Percentage > languages[i].Percentage {
				languages[i], languages[j] = languages[j], languages[i]
			}
		}
	}

	return languages
}

// recommendTemplates returns recommended templates based on analysis results.
func recommendTemplates(result *AnalyzeResult) []string {
	templates := []string{"code-review"} // Always recommend code review

	// Check for Go projects
	hasGo := result.LanguageCounts["Go"] > 0

	// Check for test-friendly languages
	hasTestableLanguage := hasGo ||
		result.LanguageCounts["Python"] > 0 ||
		result.LanguageCounts["Java"] > 0 ||
		result.LanguageCounts["JavaScript"] > 0 ||
		result.LanguageCounts["TypeScript"] > 0

	// Recommend test coverage for languages that commonly have test frameworks
	if hasTestableLanguage {
		templates = append(templates, "test-coverage")
	}

	// Recommend dependency audit for projects with package managers
	// This is heuristic-based since we don't deeply analyze package files
	hasDependencyFiles := false
	// We would need to check for package.json, requirements.txt, pom.xml, etc.
	// For now, recommend for common languages that have dependency management
	if result.LanguageCounts["JavaScript"] > 0 ||
		result.LanguageCounts["TypeScript"] > 0 ||
		result.LanguageCounts["Python"] > 0 ||
		result.LanguageCounts["Java"] > 0 ||
		hasGo {
		hasDependencyFiles = true
	}

	if hasDependencyFiles {
		templates = append(templates, "dependency-audit")
	}

	return templates
}