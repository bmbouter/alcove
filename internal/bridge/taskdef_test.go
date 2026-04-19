package bridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bmbouter/alcove/internal"
)

func TestResolvePluginBundles(t *testing.T) {
	// Test bundle expansion
	plugins := []PluginSpec{
		{Name: "sdlc-go", Source: "bundle"},
		{Name: "my-custom", Source: "https://github.com/org/plugin.git"},
	}
	resolved := ResolvePluginBundles(plugins)

	// sdlc-go expands to 3 plugins + 1 custom = 4 total
	if len(resolved) != 4 {
		t.Fatalf("expected 4 plugins, got %d: %v", len(resolved), resolved)
	}
	// First 3 should be from the bundle
	if resolved[0].Name != "code-review" {
		t.Errorf("expected code-review first, got %s", resolved[0].Name)
	}
	// Last should be custom
	if resolved[3].Name != "my-custom" {
		t.Errorf("expected my-custom last, got %s", resolved[3].Name)
	}
}

func TestResolvePluginBundles_Dedup(t *testing.T) {
	// Test deduplication when bundle plugin overlaps with explicit plugin
	plugins := []PluginSpec{
		{Name: "code-review", Source: "claude-plugins-official"},
		{Name: "sdlc-go", Source: "bundle"}, // also contains code-review
	}
	resolved := ResolvePluginBundles(plugins)

	// code-review should only appear once
	count := 0
	for _, p := range resolved {
		if p.Name == "code-review" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 code-review, got %d", count)
	}
}

func TestResolvePluginBundles_UnknownBundle(t *testing.T) {
	plugins := []PluginSpec{
		{Name: "nonexistent-bundle", Source: "bundle"},
	}
	resolved := ResolvePluginBundles(plugins)
	if len(resolved) != 0 {
		t.Errorf("expected 0 plugins for unknown bundle, got %d", len(resolved))
	}
}

func TestParseAgentDefinitionWithPlugins(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "Do something"
plugins:
  - name: code-review
    source: claude-plugins-official
  - name: my-plugin
    source: https://github.com/org/plugin.git
    ref: v1.0
  - name: marketplace-plugin
`
	def, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Plugins) != 3 {
		t.Fatalf("expected 3 plugins, got %d", len(def.Plugins))
	}
	if def.Plugins[0].Name != "code-review" {
		t.Errorf("plugin 0 name: got %q, want %q", def.Plugins[0].Name, "code-review")
	}
	if def.Plugins[0].Source != "claude-plugins-official" {
		t.Errorf("plugin 0 source: got %q, want %q", def.Plugins[0].Source, "claude-plugins-official")
	}
	if def.Plugins[1].Ref != "v1.0" {
		t.Errorf("plugin 1 ref: got %q, want %q", def.Plugins[1].Ref, "v1.0")
	}
	if def.Plugins[2].Source != "" {
		t.Errorf("plugin 2 source should be empty for marketplace, got %q", def.Plugins[2].Source)
	}
}

func TestParseAgentDefinitionWithCredentials(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "Do something"
credentials:
  SPLUNK_TOKEN: splunk
  JIRA_TOKEN: jira
  VERTEX_SA_JSON: google-vertex
`
	def, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Credentials) != 3 {
		t.Fatalf("expected 3 credentials, got %d", len(def.Credentials))
	}
	if def.Credentials["SPLUNK_TOKEN"] != "splunk" {
		t.Errorf("SPLUNK_TOKEN: got %q, want %q", def.Credentials["SPLUNK_TOKEN"], "splunk")
	}
	if def.Credentials["JIRA_TOKEN"] != "jira" {
		t.Errorf("JIRA_TOKEN: got %q, want %q", def.Credentials["JIRA_TOKEN"], "jira")
	}
	if def.Credentials["VERTEX_SA_JSON"] != "google-vertex" {
		t.Errorf("VERTEX_SA_JSON: got %q, want %q", def.Credentials["VERTEX_SA_JSON"], "google-vertex")
	}
}

func TestParseAgentDefinitionExecutableWithCredentials(t *testing.T) {
	yamlData := `
name: Splunk Log Analyzer
executable:
  url: https://github.com/pulp/pulp-service/releases/download/v1/agent-splunk
  args: ["--model", "claude-opus-4-6"]
credentials:
  SPLUNK_TOKEN: splunk
  JIRA_TOKEN: jira
`
	def, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def.Executable == nil {
		t.Fatal("expected executable to be set")
	}
	if len(def.Credentials) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(def.Credentials))
	}
	if def.Credentials["SPLUNK_TOKEN"] != "splunk" {
		t.Errorf("SPLUNK_TOKEN: got %q, want %q", def.Credentials["SPLUNK_TOKEN"], "splunk")
	}
}

func TestParseTaskDefinitionWithDirectOutbound(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "test"
direct_outbound: true
`
	td, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !td.DirectOutbound {
		t.Error("expected DirectOutbound=true")
	}
}

func TestParseTaskDefinitionWithDirectOutboundFalse(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "test"
`
	td, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if td.DirectOutbound {
		t.Error("expected DirectOutbound=false by default")
	}
}

func TestParseTaskDefinitionWithDevContainer(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "test"
dev_container:
  image: "quay.io/myorg/devenv:latest"
`
	td, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if td.DevContainer == nil {
		t.Fatal("expected DevContainer to be set")
	}
	if td.DevContainer.Image != "quay.io/myorg/devenv:latest" {
		t.Errorf("DevContainer.Image: got %q, want %q", td.DevContainer.Image, "quay.io/myorg/devenv:latest")
	}
	if td.DevContainer.NetworkAccess != "internal" {
		t.Errorf("DevContainer.NetworkAccess: got %q, want %q (default)", td.DevContainer.NetworkAccess, "internal")
	}
}

func TestParseTaskDefinitionWithDevContainerNetworkAccess(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		wantAccess    string
		wantErr       string
	}{
		{
			name: "default to internal",
			yaml: `
name: Test Agent
prompt: "test"
dev_container:
  image: "golang:1.25"
`,
			wantAccess: "internal",
		},
		{
			name: "explicit internal",
			yaml: `
name: Test Agent
prompt: "test"
dev_container:
  image: "golang:1.25"
  network_access: internal
`,
			wantAccess: "internal",
		},
		{
			name: "external",
			yaml: `
name: Test Agent
prompt: "test"
dev_container:
  image: "golang:1.25"
  network_access: external
`,
			wantAccess: "external",
		},
		{
			name: "invalid value",
			yaml: `
name: Test Agent
prompt: "test"
dev_container:
  image: "golang:1.25"
  network_access: public
`,
			wantErr: `dev_container.network_access must be "internal" or "external", got "public"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			td, err := ParseTaskDefinition([]byte(tt.yaml))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if err.Error() != tt.wantErr {
					t.Errorf("error = %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if td.DevContainer.NetworkAccess != tt.wantAccess {
				t.Errorf("NetworkAccess = %q, want %q", td.DevContainer.NetworkAccess, tt.wantAccess)
			}
		})
	}
}

func TestParseTaskDefinitionWithDevContainerEmptyImage(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "test"
dev_container:
  image: ""
`
	_, err := ParseTaskDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for dev_container with empty image")
	}
	if err.Error() != "dev_container block present but image is empty" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseTaskDefinitionWithDevContainerNoImage(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "test"
dev_container: {}
`
	_, err := ParseTaskDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for dev_container with no image")
	}
	if err.Error() != "dev_container block present but image is empty" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseTaskDefinitionWithoutDevContainer(t *testing.T) {
	yamlData := `
name: Test Agent
prompt: "test"
`
	td, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if td.DevContainer != nil {
		t.Error("expected DevContainer to be nil when omitted")
	}
}

func TestToTaskRequestIncludesDevContainer(t *testing.T) {
	def := &TaskDefinition{
		Name:   "Test Agent",
		Prompt: "Do something",
		DevContainer: &DevContainerSpec{
			Image: "quay.io/myorg/devenv:latest",
		},
	}

	req := def.ToTaskRequest()
	if req.DevContainer == nil {
		t.Fatal("expected DevContainer to be set in task request")
	}
	if req.DevContainer.Image != "quay.io/myorg/devenv:latest" {
		t.Errorf("DevContainer.Image: got %q, want %q", req.DevContainer.Image, "quay.io/myorg/devenv:latest")
	}
}

func TestToTaskRequestIncludesDirectOutbound(t *testing.T) {
	def := &TaskDefinition{
		Name:           "Test Agent",
		Prompt:         "Do something",
		DirectOutbound: true,
	}

	req := def.ToTaskRequest()
	if !req.DirectOutbound {
		t.Error("expected DirectOutbound=true in task request")
	}
}

func TestToTaskRequestIncludesCredentials(t *testing.T) {
	def := &TaskDefinition{
		Name:   "Test Agent",
		Prompt: "Do something",
		Credentials: map[string]string{
			"API_TOKEN": "my-service",
			"DB_PASS":   "database",
		},
	}

	req := def.ToTaskRequest()
	if len(req.Credentials) != 2 {
		t.Fatalf("expected 2 credentials in request, got %d", len(req.Credentials))
	}
	if req.Credentials["API_TOKEN"] != "my-service" {
		t.Errorf("API_TOKEN: got %q, want %q", req.Credentials["API_TOKEN"], "my-service")
	}
	if req.Credentials["DB_PASS"] != "database" {
		t.Errorf("DB_PASS: got %q, want %q", req.Credentials["DB_PASS"], "database")
	}
}

// TestToTaskRequest_AllFields verifies that ToTaskRequest maps every relevant
// TaskDefinition field to the TaskRequest, with special attention to fields
// (DirectOutbound, CIGate) that were previously dropped.
func TestToTaskRequest_AllFields(t *testing.T) {
	def := &TaskDefinition{
		Name:        "Full Agent",
		Description: "An agent with every field set",
		Prompt:      "Implement the thing",
		Executable:  &internal.ExecutableSpec{URL: "https://example.com/bin", Args: []string{"--fast"}, Env: map[string]string{"K": "V"}},
		Repos:       []internal.RepoSpec{{URL: "https://github.com/pulp/pulp_python", Name: "pulp_python"}},
		Provider:    "anthropic",
		Model:       "claude-opus-4-6",
		Timeout:     600,
		BudgetUSD:   5.50,
		Debug:       true,
		Profiles:    []string{"strict"},
		Plugins:     []PluginSpec{{Name: "code-review", Source: "claude-plugins-official", Ref: "v1"}},
		Tools:       map[string]ToolConfig{"github": {Enabled: true, Repos: []string{"org/repo"}, Operations: []string{"read"}}},
		Credentials: map[string]string{"TOKEN": "my-svc"},
		DirectOutbound: true,
		CIGate:      &CIGate{MaxRetries: 3, Timeout: 900},
		DevContainer: &DevContainerSpec{Image: "quay.io/myorg/devenv:latest", NetworkAccess: "external"},
	}

	req := def.ToTaskRequest()

	// Verify each mapped field.
	if req.Prompt != def.Prompt {
		t.Errorf("Prompt: got %q, want %q", req.Prompt, def.Prompt)
	}
	if req.Executable == nil || req.Executable.URL != def.Executable.URL {
		t.Errorf("Executable: got %v, want URL %q", req.Executable, def.Executable.URL)
	}
	if len(req.Repos) != len(def.Repos) {
		t.Errorf("Repos length: got %d, want %d", len(req.Repos), len(def.Repos))
	} else if len(req.Repos) > 0 && req.Repos[0].URL != def.Repos[0].URL {
		t.Errorf("Repos[0].URL: got %q, want %q", req.Repos[0].URL, def.Repos[0].URL)
	}
	if req.Provider != def.Provider {
		t.Errorf("Provider: got %q, want %q", req.Provider, def.Provider)
	}
	if req.Model != def.Model {
		t.Errorf("Model: got %q, want %q", req.Model, def.Model)
	}
	if req.Timeout != def.Timeout {
		t.Errorf("Timeout: got %d, want %d", req.Timeout, def.Timeout)
	}
	if req.Budget != def.BudgetUSD {
		t.Errorf("Budget: got %f, want %f", req.Budget, def.BudgetUSD)
	}
	if !req.Debug {
		t.Error("Debug: expected true")
	}
	if len(req.Profiles) != 1 || req.Profiles[0] != "strict" {
		t.Errorf("Profiles: got %v, want [strict]", req.Profiles)
	}
	if len(req.Plugins) != 1 || req.Plugins[0].Name != "code-review" {
		t.Errorf("Plugins: got %v, want [{code-review ...}]", req.Plugins)
	}
	if len(req.Tools) != 1 {
		t.Errorf("Tools: expected 1, got %d", len(req.Tools))
	}
	if len(req.Credentials) != 1 || req.Credentials["TOKEN"] != "my-svc" {
		t.Errorf("Credentials: got %v, want {TOKEN:my-svc}", req.Credentials)
	}
	if !req.DirectOutbound {
		t.Error("DirectOutbound: expected true in TaskRequest")
	}
	if req.DevContainer == nil || req.DevContainer.Image != "quay.io/myorg/devenv:latest" {
		t.Errorf("DevContainer: got %v, want {Image: quay.io/myorg/devenv:latest}", req.DevContainer)
	}

	// CIGate is intentionally NOT mapped to TaskRequest (it is consumed by Bridge
	// workflow logic, not sent to Skiff), so verify it is absent.
	// There is no CIGate field on TaskRequest; this is a compile-time guarantee.
	// This comment documents the design decision.
}

// TestGetAgentDefinitionFieldCopyRoundTrip ensures that every parseable field
// (those with a yaml struct tag) survives the JSON marshal -> unmarshal -> field
// copy round-trip used by GetAgentDefinition. If a new field with a yaml tag is
// added to TaskDefinition but not added to the copy block in GetAgentDefinition,
// this test will fail.
func TestGetAgentDefinitionFieldCopyRoundTrip(t *testing.T) {
	// Step 1: Create a TaskDefinition with ALL yaml-tagged fields set to
	// non-zero values. Every parseable field must be populated here.
	original := TaskDefinition{
		Name:        "round-trip-agent",
		Description: "Tests that all fields survive the copy block",
		Prompt:      "Do the thing",
		Executable: &internal.ExecutableSpec{
			URL:  "https://example.com/binary",
			Args: []string{"--flag"},
			Env:  map[string]string{"KEY": "VAL"},
		},
		Repos:    []internal.RepoSpec{{URL: "https://github.com/org/repo", Name: "repo"}},
		Provider: "anthropic",
		Model:    "claude-opus-4-6",
		Timeout:  600,
		BudgetUSD: 5.50,
		Debug:    true,
		Profiles: []string{"strict", "permissive"},
		Plugins: []PluginSpec{
			{Name: "code-review", Source: "claude-plugins-official", Ref: "v1"},
		},
		Tools: map[string]ToolConfig{
			"github": {Enabled: true, Repos: []string{"org/repo"}, Operations: []string{"read"}},
		},
		Credentials: map[string]string{"TOKEN": "my-svc"},
		Schedule: &TaskDefSchedule{
			Cron:    "0 */6 * * *",
			Enabled: true,
		},
		Trigger: &EventTrigger{
			GitHub: &GitHubTrigger{
				Events:  []string{"push"},
				Actions: []string{"opened"},
			},
		},
		CIGate: &CIGate{
			MaxRetries: 3,
			Timeout:    900,
		},
		DirectOutbound: true,
		DevContainer: &DevContainerSpec{
			Image:         "quay.io/myorg/devenv:latest",
			NetworkAccess: "external",
		},
	}

	// Step 2: Marshal to JSON (simulates UpsertAgentDefinition storing parsed JSONB).
	parsedJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Step 3: Unmarshal (simulates reading the parsed column back from the DB).
	var parsed TaskDefinition
	if err := json.Unmarshal(parsedJSON, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Step 4: Simulate the field copy block from GetAgentDefinition.
	// This block MUST be kept in sync with the one in GetAgentDefinition.
	// If you add a field here, add it there too (and vice versa).
	//
	// Name and Description come from dedicated DB columns (via Scan), not
	// from the parsed JSONB copy block, so we pre-populate them here.
	td := TaskDefinition{
		Name:        original.Name,
		Description: original.Description,
	}
	td.Prompt = parsed.Prompt
	td.Executable = parsed.Executable
	td.Repos = parsed.Repos
	td.Provider = parsed.Provider
	td.Model = parsed.Model
	td.Timeout = parsed.Timeout
	td.BudgetUSD = parsed.BudgetUSD
	td.Debug = parsed.Debug
	td.Profiles = parsed.Profiles
	td.Tools = parsed.Tools
	td.Schedule = parsed.Schedule
	td.Trigger = parsed.Trigger
	td.Plugins = parsed.Plugins
	td.Credentials = parsed.Credentials
	td.DirectOutbound = parsed.DirectOutbound
	td.CIGate = parsed.CIGate
	td.DevContainer = parsed.DevContainer

	// Step 5: Use reflect to verify every yaml-tagged field matches the
	// original. Fields with a yaml tag are "parseable" (come from the YAML
	// agent definition, stored in the parsed JSONB column). If a new yaml
	// field is added to TaskDefinition but forgotten in the copy block above,
	// it will be zero in td but non-zero in original, and this loop catches it.
	origVal := reflect.ValueOf(original)
	tdVal := reflect.ValueOf(td)
	origType := origVal.Type()

	yamlFieldCount := 0
	for i := 0; i < origType.NumField(); i++ {
		field := origType.Field(i)
		if _, hasYAML := field.Tag.Lookup("yaml"); !hasYAML {
			continue // skip metadata-only fields (no yaml tag)
		}
		yamlFieldCount++
		origField := origVal.Field(i).Interface()
		tdField := tdVal.Field(i).Interface()
		if !reflect.DeepEqual(origField, tdField) {
			t.Errorf("field %q not preserved after copy: got %v, want %v\n"+
				"Hint: add td.%s = parsed.%s to the copy block in GetAgentDefinition",
				field.Name, tdField, origField, field.Name, field.Name)
		}
	}

	// Sanity check: make sure we actually checked a meaningful number of fields.
	// Update this count when adding new yaml-tagged fields to TaskDefinition.
	const expectedYAMLFields = 19
	if yamlFieldCount != expectedYAMLFields {
		t.Errorf("expected %d yaml-tagged fields in TaskDefinition, found %d; "+
			"update this test and the copy block in GetAgentDefinition",
			expectedYAMLFields, yamlFieldCount)
	}
}

func TestParseTaskDefinitionWithRepos(t *testing.T) {
	yamlData := `
name: Multi-Repo Agent
prompt: "Do something across repos"
repos:
  - url: https://github.com/org/repo1.git
    ref: main
  - url: https://github.com/org/repo2.git
    name: custom-name
`
	td, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(td.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(td.Repos))
	}
	// First repo: name derived from URL
	if td.Repos[0].Name != "repo1" {
		t.Errorf("Repos[0].Name: got %q, want %q", td.Repos[0].Name, "repo1")
	}
	if td.Repos[0].URL != "https://github.com/org/repo1.git" {
		t.Errorf("Repos[0].URL: got %q, want %q", td.Repos[0].URL, "https://github.com/org/repo1.git")
	}
	if td.Repos[0].Ref != "main" {
		t.Errorf("Repos[0].Ref: got %q, want %q", td.Repos[0].Ref, "main")
	}
	// Second repo: explicit name
	if td.Repos[1].Name != "custom-name" {
		t.Errorf("Repos[1].Name: got %q, want %q", td.Repos[1].Name, "custom-name")
	}
}

func TestParseTaskDefinitionReposDuplicateName(t *testing.T) {
	yamlData := `
name: Dup Names Agent
prompt: "test"
repos:
  - url: https://github.com/org/repo1.git
    name: same-name
  - url: https://github.com/org/repo2.git
    name: same-name
`
	_, err := ParseTaskDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for duplicate repo names")
	}
	if !strings.Contains(err.Error(), "duplicate repo name") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseTaskDefinitionReposNameDerivation(t *testing.T) {
	yamlData := `
name: Name Derivation Agent
prompt: "test"
repos:
  - url: https://github.com/org/my-project.git
  - url: https://gitlab.com/team/another-project
`
	td, err := ParseTaskDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if td.Repos[0].Name != "my-project" {
		t.Errorf("Repos[0].Name: got %q, want %q", td.Repos[0].Name, "my-project")
	}
	if td.Repos[1].Name != "another-project" {
		t.Errorf("Repos[1].Name: got %q, want %q", td.Repos[1].Name, "another-project")
	}
}

func TestParseTaskDefinitionReposMissingURL(t *testing.T) {
	yamlData := `
name: Missing URL Agent
prompt: "test"
repos:
  - name: some-repo
  - url: https://github.com/org/valid.git
`
	_, err := ParseTaskDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for repo with empty URL")
	}
	if !strings.Contains(err.Error(), "empty URL") {
		t.Errorf("unexpected error message: %v", err)
	}
}
