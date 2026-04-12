package bridge

import "testing"

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
