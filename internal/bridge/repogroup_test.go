package bridge

import "testing"

func TestParseRepoGroupDefinition(t *testing.T) {
	yamlData := `
name: pulp-stack
description: All Pulp platform repos
repos:
  - url: https://github.com/pulp/pulpcore
  - url: https://github.com/pulp/pulp_python
    name: pulp-python
`
	rg, err := ParseRepoGroupDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rg.Name != "pulp-stack" {
		t.Errorf("name = %q, want pulp-stack", rg.Name)
	}
	if len(rg.Repos) != 2 {
		t.Fatalf("repos count = %d, want 2", len(rg.Repos))
	}
	if rg.Repos[0].Name != "pulpcore" {
		t.Errorf("repos[0].name = %q, want pulpcore (derived from URL)", rg.Repos[0].Name)
	}
	if rg.Repos[1].Name != "pulp-python" {
		t.Errorf("repos[1].name = %q, want pulp-python (explicit)", rg.Repos[1].Name)
	}
}

func TestParseRepoGroupMissingName(t *testing.T) {
	_, err := ParseRepoGroupDefinition([]byte(`repos: [{url: "https://github.com/org/repo"}]`))
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestParseRepoGroupNoRepos(t *testing.T) {
	_, err := ParseRepoGroupDefinition([]byte(`name: empty-group`))
	if err == nil {
		t.Error("expected error for empty repos")
	}
}

func TestParseRepoGroupDuplicateNames(t *testing.T) {
	yamlData := `
name: dupes
repos:
  - url: https://github.com/org/repo
    name: same
  - url: https://github.com/org/repo2
    name: same
`
	_, err := ParseRepoGroupDefinition([]byte(yamlData))
	if err == nil {
		t.Error("expected error for duplicate repo names")
	}
}

func TestAgentDefinitionRepoGroupMutualExclusion(t *testing.T) {
	yamlData := `
name: Bad Agent
prompt: test
repos:
  - url: https://github.com/org/repo
repo_group: some-group
`
	_, err := ParseAgentDefinition([]byte(yamlData))
	if err == nil {
		t.Error("expected error when both repos and repo_group are set")
	}
}

func TestAgentDefinitionRepoGroupOnly(t *testing.T) {
	yamlData := `
name: Good Agent
prompt: test
repo_group: pulp-stack
`
	td, err := ParseAgentDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if td.RepoGroup != "pulp-stack" {
		t.Errorf("repo_group = %q, want pulp-stack", td.RepoGroup)
	}
	req := td.ToTaskRequest()
	if req.RepoGroup != "pulp-stack" {
		t.Errorf("TaskRequest.RepoGroup = %q, want pulp-stack", req.RepoGroup)
	}
}
