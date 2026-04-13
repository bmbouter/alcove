package bridge

import "testing"

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
