package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeDirectory(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		if len(result.LanguageCounts) != 0 {
			t.Errorf("expected no languages, got %v", result.LanguageCounts)
		}
		if len(result.CISystems) != 0 {
			t.Errorf("expected no CI systems, got %v", result.CISystems)
		}
		if result.HasAlcoveConfig {
			t.Errorf("expected no .alcove config")
		}
		if result.HasClaudeMD {
			t.Errorf("expected no CLAUDE.md")
		}
		if result.FileCount != 0 {
			t.Errorf("expected 0 files, got %d", result.FileCount)
		}
	})

	t.Run("Go project", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create some Go files
		createFile(t, tmpDir, "main.go", "package main")
		createFile(t, tmpDir, "handler.go", "package main")
		createFile(t, tmpDir, "util.py", "print('hello')")

		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		if result.LanguageCounts["Go"] != 2 {
			t.Errorf("expected 2 Go files, got %d", result.LanguageCounts["Go"])
		}
		if result.LanguageCounts["Python"] != 1 {
			t.Errorf("expected 1 Python file, got %d", result.LanguageCounts["Python"])
		}
		if result.FileCount != 3 {
			t.Errorf("expected 3 files, got %d", result.FileCount)
		}
	})

	t.Run("GitHub Actions CI", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create GitHub workflows directory and file
		createFile(t, tmpDir, ".github/workflows/ci.yml", "name: CI")

		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		found := false
		for _, system := range result.CISystems {
			if system == "github-actions" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected github-actions in CI systems, got %v", result.CISystems)
		}
	})

	t.Run("GitLab CI", func(t *testing.T) {
		tmpDir := t.TempDir()

		createFile(t, tmpDir, ".gitlab-ci.yml", "stages: [test]")

		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		found := false
		for _, system := range result.CISystems {
			if system == "gitlab-ci" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected gitlab-ci in CI systems, got %v", result.CISystems)
		}
	})

	t.Run("Jenkins CI", func(t *testing.T) {
		tmpDir := t.TempDir()

		createFile(t, tmpDir, "Jenkinsfile", "pipeline { agent any }")

		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		found := false
		for _, system := range result.CISystems {
			if system == "jenkins" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected jenkins in CI systems, got %v", result.CISystems)
		}
	})

	t.Run("Alcove config detection", func(t *testing.T) {
		tmpDir := t.TempDir()

		createFile(t, tmpDir, ".alcove/agents/test.yml", "name: test")

		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		if !result.HasAlcoveConfig {
			t.Errorf("expected .alcove config to be detected")
		}
	})

	t.Run("CLAUDE.md detection", func(t *testing.T) {
		tmpDir := t.TempDir()

		createFile(t, tmpDir, "CLAUDE.md", "# Project instructions")

		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		if !result.HasClaudeMD {
			t.Errorf("expected CLAUDE.md to be detected")
		}
	})

	t.Run("CLAUDE.md case insensitive", func(t *testing.T) {
		tmpDir := t.TempDir()

		createFile(t, tmpDir, "claude.md", "# Project instructions")

		result, err := analyzeDirectory(tmpDir, 1000)
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		if !result.HasClaudeMD {
			t.Errorf("expected claude.md to be detected (case insensitive)")
		}
	})

	t.Run("file count cap", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create more files than the cap
		for i := 0; i < 15; i++ {
			createFile(t, tmpDir, fmt.Sprintf("file%d.go", i), "package main")
		}

		result, err := analyzeDirectory(tmpDir, 10) // Cap at 10 files for language detection
		if err != nil {
			t.Fatalf("analyzeDirectory failed: %v", err)
		}

		// Should still count all files for FileCount
		if result.FileCount != 15 {
			t.Errorf("expected 15 files total, got %d", result.FileCount)
		}

		// But language counting should be capped at 10
		if result.LanguageCounts["Go"] != 10 {
			t.Errorf("expected language counting capped at 10, got %d", result.LanguageCounts["Go"])
		}
	})
}

func TestCalculateLanguagePercentages(t *testing.T) {
	t.Run("mixed languages", func(t *testing.T) {
		counts := map[string]int{
			"Go":     72,
			"Python": 28,
		}

		languages := calculateLanguagePercentages(counts)
		if len(languages) != 2 {
			t.Fatalf("expected 2 languages, got %d", len(languages))
		}

		// Should be sorted by percentage (descending)
		if languages[0].Name != "Go" || languages[0].Percentage != 72 {
			t.Errorf("expected Go: 72%%, got %s: %d%%", languages[0].Name, languages[0].Percentage)
		}
		if languages[1].Name != "Python" || languages[1].Percentage != 28 {
			t.Errorf("expected Python: 28%%, got %s: %d%%", languages[1].Name, languages[1].Percentage)
		}
	})

	t.Run("no languages", func(t *testing.T) {
		counts := map[string]int{}
		languages := calculateLanguagePercentages(counts)
		if len(languages) != 0 {
			t.Errorf("expected no languages, got %v", languages)
		}
	})

	t.Run("rounding behavior", func(t *testing.T) {
		counts := map[string]int{
			"Go":     99,
			"Python": 1,
		}

		languages := calculateLanguagePercentages(counts)
		if len(languages) != 2 {
			t.Fatalf("expected 2 languages, got %d", len(languages))
		}

		if languages[0].Name != "Go" || languages[0].Percentage != 99 {
			t.Errorf("expected Go: 99%%, got %s: %d%%", languages[0].Name, languages[0].Percentage)
		}
		if languages[1].Name != "Python" || languages[1].Percentage != 1 {
			t.Errorf("expected Python: 1%%, got %s: %d%%", languages[1].Name, languages[1].Percentage)
		}
	})
}

func TestRecommendTemplates(t *testing.T) {
	t.Run("Go project", func(t *testing.T) {
		result := &AnalyzeResult{
			LanguageCounts: map[string]int{"Go": 10},
		}

		templates := recommendTemplates(result)

		expectedTemplates := []string{"code-review", "test-coverage", "dependency-audit"}
		if len(templates) != len(expectedTemplates) {
			t.Fatalf("expected %d templates, got %d: %v", len(expectedTemplates), len(templates), templates)
		}

		for i, expected := range expectedTemplates {
			if templates[i] != expected {
				t.Errorf("expected template %d to be %s, got %s", i, expected, templates[i])
			}
		}
	})

	t.Run("JavaScript project", func(t *testing.T) {
		result := &AnalyzeResult{
			LanguageCounts: map[string]int{"JavaScript": 10},
		}

		templates := recommendTemplates(result)

		expectedTemplates := []string{"code-review", "test-coverage", "dependency-audit"}
		if len(templates) != len(expectedTemplates) {
			t.Fatalf("expected %d templates, got %d: %v", len(expectedTemplates), len(templates), templates)
		}
	})

	t.Run("unknown language", func(t *testing.T) {
		result := &AnalyzeResult{
			LanguageCounts: map[string]int{"COBOL": 10},
		}

		templates := recommendTemplates(result)

		if len(templates) != 1 || templates[0] != "code-review" {
			t.Errorf("expected only code-review template, got %v", templates)
		}
	})

	t.Run("no languages", func(t *testing.T) {
		result := &AnalyzeResult{
			LanguageCounts: map[string]int{},
		}

		templates := recommendTemplates(result)

		if len(templates) != 1 || templates[0] != "code-review" {
			t.Errorf("expected only code-review template, got %v", templates)
		}
	})
}

func TestHandleOnboardAnalyze(t *testing.T) {
	// Create a minimal API instance for testing
	api := &API{}

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/onboard/analyze", nil)
		w := httptest.NewRecorder()

		api.handleOnboardAnalyze(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/onboard/analyze", strings.NewReader("invalid json"))
		w := httptest.NewRecorder()

		api.handleOnboardAnalyze(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
		}
	})

	t.Run("missing repo_url", func(t *testing.T) {
		body := `{"ref": "main"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/onboard/analyze", strings.NewReader(body))
		w := httptest.NewRecorder()

		api.handleOnboardAnalyze(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
		}
	})

	t.Run("non-HTTPS URL", func(t *testing.T) {
		body := `{"repo_url": "git://github.com/org/repo"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/onboard/analyze", strings.NewReader(body))
		w := httptest.NewRecorder()

		api.handleOnboardAnalyze(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
		}

		var response map[string]interface{}
		json.NewDecoder(w.Body).Decode(&response)
		if !strings.Contains(response["error"].(string), "HTTPS") {
			t.Errorf("expected error about HTTPS, got: %s", response["error"])
		}
	})

	t.Run("file:// URL rejected", func(t *testing.T) {
		body := `{"repo_url": "file:///etc/passwd"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/onboard/analyze", strings.NewReader(body))
		w := httptest.NewRecorder()

		api.handleOnboardAnalyze(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
		}
	})

	// Note: We can't easily test successful cloning without network access
	// The integration tests would handle that case
}

func TestAnalyzeDirectorySkipsHiddenDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files in hidden directories that should be skipped
	createFile(t, tmpDir, ".git/objects/abc123", "git object")
	createFile(t, tmpDir, ".hidden/file.go", "package main")

	// Create files in CI directories that should be included
	createFile(t, tmpDir, ".github/workflows/ci.yml", "name: CI")
	createFile(t, tmpDir, ".gitlab-ci.yml", "stages: [test]")

	// Create normal files
	createFile(t, tmpDir, "main.go", "package main")

	result, err := analyzeDirectory(tmpDir, 1000)
	if err != nil {
		t.Fatalf("analyzeDirectory failed: %v", err)
	}

	// Should only count the main.go file for language detection
	if result.LanguageCounts["Go"] != 1 {
		t.Errorf("expected 1 Go file, got %d (hidden .go file should be skipped)", result.LanguageCounts["Go"])
	}

	// Should detect GitHub Actions from .github/workflows/
	found := false
	for _, system := range result.CISystems {
		if system == "github-actions" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected github-actions to be detected from .github/workflows/")
	}

	// Should detect GitLab CI from .gitlab-ci.yml
	found = false
	for _, system := range result.CISystems {
		if system == "gitlab-ci" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected gitlab-ci to be detected from .gitlab-ci.yml")
	}
}

// Helper function to create files in test directories
func createFile(t *testing.T, baseDir, relativePath, content string) {
	fullPath := filepath.Join(baseDir, relativePath)
	dir := filepath.Dir(fullPath)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create dir %s: %v", dir, err)
	}

	// Create file
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create file %s: %v", fullPath, err)
	}
}